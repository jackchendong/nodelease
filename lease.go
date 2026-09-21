package nodelease

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type leaseState uint8

const (
	stateIdle leaseState = iota
	stateAcquiring
	stateHeld
	stateReleasing
	stateLost
	stateReleased
)

// Lease owns at most one worker ID. It is safe for concurrent use but is
// single-use after a successful acquisition: once released or lost, create a
// new Lease to acquire another ID.
type Lease struct {
	config normalizedConfig

	mu            sync.Mutex
	state         leaseState
	generation    uint64
	operationDone chan struct{}
	workerID      int64
	hasWorkerID   bool
	key           string
	token         string
	expiresAt     time.Time
	lifecycleStop context.CancelFunc
	lifecycleDone chan struct{}
	lifecycleWake chan struct{}
	lost          chan error
}

// New creates a Lease and validates config without accessing Redis.
func New(config Config) (*Lease, error) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	return &Lease{
		config: normalized,
		state:  stateIdle,
		lost:   make(chan error, 1),
	}, nil
}

// Acquire randomly chooses an available worker ID from the configured range.
// Concurrent calls wait for the in-flight acquisition and then return the same
// held ID. The context controls only acquisition; the lease lifecycle remains
// active after Acquire returns.
func (lease *Lease) Acquire(ctx context.Context) (int64, error) {
	if ctx == nil {
		return 0, fmt.Errorf("acquire worker ID: nil context")
	}

	generation, err := lease.beginAcquire(ctx)
	if err != nil {
		return 0, err
	}
	if generation == 0 {
		lease.mu.Lock()
		workerID := lease.workerID
		lease.mu.Unlock()
		return workerID, nil
	}

	token, err := randomToken()
	if err != nil {
		lease.failAcquire(generation)
		return 0, err
	}

	rangeSize := lease.config.maxWorkerID - lease.config.minWorkerID + 1
	start, err := randomOffset(rangeSize)
	if err != nil {
		lease.failAcquire(generation)
		return 0, err
	}

	for attempt := int64(0); attempt < rangeSize; attempt++ {
		if err := ctx.Err(); err != nil {
			lease.failAcquire(generation)
			return 0, fmt.Errorf("acquire worker ID: %w", err)
		}

		workerID := lease.config.minWorkerID + (start+attempt)%rangeSize
		key := lease.keyFor(workerID)
		acquired, setErr := lease.config.redis.SetNX(ctx, key, token, redisTTL(lease.config.ttl)).Result()
		if setErr != nil {
			lease.failAcquire(generation)
			return 0, fmt.Errorf("acquire worker ID %d: %w", workerID, setErr)
		}
		if !acquired {
			continue
		}

		lease.completeAcquire(ctx, generation, workerID, key, token)
		return workerID, nil
	}

	lease.failAcquire(generation)
	return 0, ErrNoWorkerIDAvailable
}

func (lease *Lease) beginAcquire(ctx context.Context) (uint64, error) {
	for {
		lease.mu.Lock()
		switch lease.state {
		case stateHeld:
			lease.mu.Unlock()
			return 0, nil
		case stateAcquiring, stateReleasing:
			done := lease.operationDone
			lease.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return 0, fmt.Errorf("wait to acquire worker ID: %w", ctx.Err())
			}
		case stateLost, stateReleased:
			lease.mu.Unlock()
			return 0, ErrLeaseClosed
		case stateIdle:
			lease.generation++
			generation := lease.generation
			lease.state = stateAcquiring
			lease.operationDone = make(chan struct{})
			lease.mu.Unlock()
			return generation, nil
		default:
			lease.mu.Unlock()
			return 0, fmt.Errorf("acquire worker ID: unknown lease state")
		}
	}
}

func (lease *Lease) completeAcquire(
	ctx context.Context,
	generation uint64,
	workerID int64,
	key string,
	token string,
) {
	lifecycleCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	wake := make(chan struct{}, 1)

	lease.mu.Lock()
	lease.state = stateHeld
	lease.workerID = workerID
	lease.hasWorkerID = true
	lease.key = key
	lease.token = token
	lease.expiresAt = time.Now().Add(redisTTL(lease.config.ttl))
	lease.lifecycleStop = cancel
	lease.lifecycleDone = done
	lease.lifecycleWake = wake
	close(lease.operationDone)
	lease.operationDone = nil
	lease.mu.Unlock()

	go lease.lifecycle(lifecycleCtx, generation, done, wake, key, token)
}

func (lease *Lease) failAcquire(generation uint64) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.state != stateAcquiring || lease.generation != generation {
		return
	}
	lease.state = stateIdle
	close(lease.operationDone)
	lease.operationDone = nil
}

// Renew extends the active lease by the configured TTL.
func (lease *Lease) Renew(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("renew lease: nil context")
	}

	lease.mu.Lock()
	if lease.state != stateHeld {
		lease.mu.Unlock()
		return ErrLeaseLost
	}
	generation := lease.generation
	key := lease.key
	token := lease.token
	lease.mu.Unlock()

	renewed, err := lease.renewToken(ctx, key, token)
	if err != nil {
		return err
	}
	if !renewed {
		lease.markLost(generation, ErrLeaseLost)
		return ErrLeaseLost
	}
	if !lease.recordRenewal(generation, true) {
		return ErrLeaseLost
	}
	return nil
}

func (lease *Lease) recordRenewal(generation uint64, wake bool) bool {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.state != stateHeld || lease.generation != generation {
		return false
	}
	lease.expiresAt = time.Now().Add(redisTTL(lease.config.ttl))
	if wake {
		select {
		case lease.lifecycleWake <- struct{}{}:
		default:
		}
	}
	return true
}

// Release stops the lease lifecycle and atomically removes the Redis key when
// its ownership token still matches. It is safe to call more than once.
func (lease *Lease) Release(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("release lease: nil context")
	}

	for {
		lease.mu.Lock()
		switch lease.state {
		case stateIdle, stateLost, stateReleased:
			lease.mu.Unlock()
			return nil
		case stateAcquiring, stateReleasing:
			done := lease.operationDone
			lease.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return fmt.Errorf("wait to release lease: %w", ctx.Err())
			}
		case stateHeld:
			key := lease.key
			token := lease.token
			stop := lease.lifecycleStop
			done := lease.lifecycleDone
			lease.state = stateReleasing
			lease.hasWorkerID = false
			lease.generation++
			lease.operationDone = make(chan struct{})
			lease.mu.Unlock()

			stop()
			select {
			case <-done:
			case <-ctx.Done():
				lease.finishRelease()
				return fmt.Errorf("stop lease renewal: %w", ctx.Err())
			}

			err := lease.releaseToken(ctx, key, token)
			lease.finishRelease()
			return err
		default:
			lease.mu.Unlock()
			return fmt.Errorf("release lease: unknown lease state")
		}
	}
}

func (lease *Lease) finishRelease() {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.state = stateReleased
	lease.key = ""
	lease.token = ""
	lease.lifecycleStop = nil
	lease.lifecycleDone = nil
	lease.lifecycleWake = nil
	close(lease.operationDone)
	lease.operationDone = nil
}

// WorkerID returns the current worker ID and whether the lease still considers
// itself the owner. Callers must stop using an ID as soon as ok becomes false.
func (lease *Lease) WorkerID() (workerID int64, ok bool) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.workerID, lease.hasWorkerID
}

// Lost returns a channel that receives at most one lease-loss error. The
// channel is not closed after a clean Release.
func (lease *Lease) Lost() <-chan error {
	return lease.lost
}

func (lease *Lease) lifecycle(
	ctx context.Context,
	generation uint64,
	done chan<- struct{},
	wake <-chan struct{},
	key string,
	token string,
) {
	defer close(done)

	for {
		expiresAt, active := lease.currentExpiration(generation)
		if !active {
			return
		}
		remaining := time.Until(expiresAt)
		if remaining <= 0 {
			lease.markLost(generation, ErrLeaseLost)
			return
		}

		delay := remaining
		if lease.config.autoRenew && lease.config.renewInterval < delay {
			delay = lease.config.renewInterval
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case <-wake:
			stopTimer(timer)
			continue
		case <-timer.C:
		}

		expiresAt, active = lease.currentExpiration(generation)
		if !active {
			return
		}
		if !time.Now().Before(expiresAt) {
			lease.markLost(generation, ErrLeaseLost)
			return
		}
		if !lease.config.autoRenew {
			continue
		}

		renewCtx, cancel := context.WithDeadline(ctx, expiresAt)
		renewed, err := lease.renewToken(renewCtx, key, token)
		cancel()
		if err == nil && renewed {
			if !lease.recordRenewal(generation, false) {
				return
			}
			continue
		}
		if err == nil {
			lease.markLost(generation, ErrLeaseLost)
			return
		}

		expiresAt, active = lease.currentExpiration(generation)
		if active && !time.Now().Before(expiresAt) {
			lease.markLost(generation, errors.Join(ErrLeaseLost, err))
			return
		}
	}
}

func (lease *Lease) currentExpiration(generation uint64) (time.Time, bool) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.state != stateHeld || lease.generation != generation {
		return time.Time{}, false
	}
	return lease.expiresAt, true
}

func (lease *Lease) markLost(generation uint64, cause error) {
	if !errors.Is(cause, ErrLeaseLost) {
		cause = errors.Join(ErrLeaseLost, cause)
	}

	lease.mu.Lock()
	if lease.state != stateHeld || lease.generation != generation {
		lease.mu.Unlock()
		return
	}
	stop := lease.lifecycleStop
	lease.state = stateLost
	lease.hasWorkerID = false
	lease.key = ""
	lease.token = ""
	lease.lifecycleStop = nil
	lease.lifecycleWake = nil
	lease.mu.Unlock()

	if stop != nil {
		stop()
	}
	select {
	case lease.lost <- cause:
	default:
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
