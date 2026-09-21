package nodelease

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, client
}

func newTestLease(t *testing.T, client redis.UniversalClient, service string, workerRange IDRange) *Lease {
	t.Helper()
	lease, err := New(Config{
		ServiceName:  service,
		Redis:        client,
		Range:        &workerRange,
		TTL:          time.Second,
		DisableRenew: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return lease
}

func TestAcquireReturnsHeldIDAndReleaseIsFinal(t *testing.T) {
	server, client := newTestRedis(t)
	lease := newTestLease(t, client, "orders", IDRange{Min: 7, Max: 7})

	workerID, err := lease.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if workerID != 7 {
		t.Fatalf("Acquire() ID = %d, want 7", workerID)
	}
	second, err := lease.Acquire(context.Background())
	if err != nil || second != workerID {
		t.Fatalf("second Acquire() = (%d, %v), want (%d, nil)", second, err, workerID)
	}
	if current, ok := lease.WorkerID(); !ok || current != 7 {
		t.Fatalf("WorkerID() = (%d, %v), want (7, true)", current, ok)
	}

	key := lease.keyFor(7)
	if !server.Exists(key) {
		t.Fatalf("Redis key %q does not exist", key)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if server.Exists(key) {
		t.Fatalf("Redis key %q still exists after release", key)
	}
	if _, ok := lease.WorkerID(); ok {
		t.Fatal("WorkerID() reports ownership after release")
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}
	if _, err := lease.Acquire(context.Background()); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("Acquire() after release error = %v, want ErrLeaseClosed", err)
	}
}

func TestAcquireExhaustedRange(t *testing.T) {
	_, client := newTestRedis(t)
	first := newTestLease(t, client, "orders", IDRange{Min: 3, Max: 3})
	second := newTestLease(t, client, "orders", IDRange{Min: 3, Max: 3})

	if _, err := first.Acquire(context.Background()); err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	t.Cleanup(func() { _ = first.Release(context.Background()) })

	if _, err := second.Acquire(context.Background()); !errors.Is(err, ErrNoWorkerIDAvailable) {
		t.Fatalf("second Acquire() error = %v, want ErrNoWorkerIDAvailable", err)
	}
}

func TestServicesUseIndependentIDSpaces(t *testing.T) {
	_, client := newTestRedis(t)
	orders := newTestLease(t, client, "orders", IDRange{Min: 5, Max: 5})
	payments := newTestLease(t, client, "payments", IDRange{Min: 5, Max: 5})

	for name, lease := range map[string]*Lease{"orders": orders, "payments": payments} {
		if workerID, err := lease.Acquire(context.Background()); err != nil || workerID != 5 {
			t.Fatalf("%s Acquire() = (%d, %v), want (5, nil)", name, workerID, err)
		}
		t.Cleanup(func() { _ = lease.Release(context.Background()) })
	}
	if orders.keyFor(5) == payments.keyFor(5) {
		t.Fatal("different services generated the same Redis key")
	}
}

func TestConcurrentAcquisitionIsUnique(t *testing.T) {
	_, client := newTestRedis(t)
	const count = 16

	leases := make([]*Lease, count)
	for index := range leases {
		leases[index] = newTestLease(t, client, "workers", IDRange{Min: 0, Max: count - 1})
	}

	type result struct {
		id  int64
		err error
	}
	results := make(chan result, count)
	var group sync.WaitGroup
	for _, lease := range leases {
		group.Add(1)
		go func(lease *Lease) {
			defer group.Done()
			id, err := lease.Acquire(context.Background())
			results <- result{id: id, err: err}
		}(lease)
	}
	group.Wait()
	close(results)

	seen := make(map[int64]struct{}, count)
	for result := range results {
		if result.err != nil {
			t.Fatalf("Acquire() error = %v", result.err)
		}
		if _, exists := seen[result.id]; exists {
			t.Fatalf("worker ID %d was allocated more than once", result.id)
		}
		seen[result.id] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("allocated %d unique IDs, want %d", len(seen), count)
	}

	for _, lease := range leases {
		if err := lease.Release(context.Background()); err != nil {
			t.Fatalf("Release() error = %v", err)
		}
	}
}

func TestRenewExtendsTTL(t *testing.T) {
	server, client := newTestRedis(t)
	lease := newTestLease(t, client, "orders", IDRange{Min: 9, Max: 9})
	if _, err := lease.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	t.Cleanup(func() { _ = lease.Release(context.Background()) })

	key := lease.keyFor(9)
	server.FastForward(800 * time.Millisecond)
	if err := lease.Renew(context.Background()); err != nil {
		t.Fatalf("Renew() error = %v", err)
	}
	server.FastForward(300 * time.Millisecond)
	if !server.Exists(key) {
		t.Fatal("lease expired despite successful renewal")
	}
}

func TestWrongTokenCannotRenewOrRelease(t *testing.T) {
	server, client := newTestRedis(t)
	lease := newTestLease(t, client, "orders", IDRange{Min: 11, Max: 11})
	if _, err := lease.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	key := lease.keyFor(11)
	server.Set(key, "new-owner-token")
	if err := lease.Renew(context.Background()); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Renew() error = %v, want ErrLeaseLost", err)
	}
	select {
	case err := <-lease.Lost():
		if !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("Lost() error = %v, want ErrLeaseLost", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Lost() did not report token mismatch")
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release() after loss error = %v", err)
	}
	value, err := server.Get(key)
	if err != nil || value != "new-owner-token" {
		t.Fatalf("replacement key = (%q, %v), want new-owner-token", value, err)
	}
}

func TestReleaseDoesNotDeleteReplacementOwner(t *testing.T) {
	server, client := newTestRedis(t)
	lease := newTestLease(t, client, "orders", IDRange{Min: 13, Max: 13})
	if _, err := lease.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	key := lease.keyFor(13)
	server.Set(key, "replacement")
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	value, err := server.Get(key)
	if err != nil || value != "replacement" {
		t.Fatalf("replacement key = (%q, %v), want replacement", value, err)
	}
}

func TestAutomaticRenewalReportsLostLease(t *testing.T) {
	server, client := newTestRedis(t)
	lease, err := New(Config{
		ServiceName:   "orders",
		Redis:         client,
		Range:         &IDRange{Min: 15, Max: 15},
		TTL:           300 * time.Millisecond,
		RenewInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := lease.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	server.Set(lease.keyFor(15), "replacement")

	select {
	case err := <-lease.Lost():
		if !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("Lost() error = %v, want ErrLeaseLost", err)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic renewal did not report token mismatch")
	}
	if _, ok := lease.WorkerID(); ok {
		t.Fatal("WorkerID() reports ownership after automatic lease loss")
	}
}

func TestAutomaticRenewalExtendsRedisTTL(t *testing.T) {
	server, client := newTestRedis(t)
	lease, err := New(Config{
		ServiceName:   "orders",
		Redis:         client,
		Range:         &IDRange{Min: 16, Max: 16},
		TTL:           100 * time.Millisecond,
		RenewInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := lease.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	t.Cleanup(func() { _ = lease.Release(context.Background()) })

	key := lease.keyFor(16)
	server.FastForward(80 * time.Millisecond)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(2 * time.Millisecond)
	defer poll.Stop()
	for {
		if ttl := server.TTL(key); ttl > 50*time.Millisecond {
			break
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("automatic renewal did not extend TTL; remaining TTL = %s", server.TTL(key))
		}
	}
	server.FastForward(30 * time.Millisecond)
	if !server.Exists(key) {
		t.Fatal("lease expired after automatic renewal")
	}
}

func TestDisabledRenewalReportsLocalExpiration(t *testing.T) {
	_, client := newTestRedis(t)
	lease, err := New(Config{
		ServiceName:  "orders",
		Redis:        client,
		Range:        &IDRange{Min: 17, Max: 17},
		TTL:          20 * time.Millisecond,
		DisableRenew: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := lease.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	select {
	case err := <-lease.Lost():
		if !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("Lost() error = %v, want ErrLeaseLost", err)
		}
	case <-time.After(time.Second):
		t.Fatal("local expiration was not reported")
	}
}

func TestAcquireHonorsCanceledContext(t *testing.T) {
	_, client := newTestRedis(t)
	lease := newTestLease(t, client, "orders", IDRange{Min: 0, Max: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := lease.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire() error = %v, want context.Canceled", err)
	}
	if _, ok := lease.WorkerID(); ok {
		t.Fatal("WorkerID() reports ownership after canceled acquisition")
	}
}

func TestAcquireReturnsRedisError(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: time.Millisecond})
	t.Cleanup(func() { _ = client.Close() })
	lease := newTestLease(t, client, "orders", IDRange{Min: 0, Max: 0})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := lease.Acquire(ctx); err == nil || errors.Is(err, ErrNoWorkerIDAvailable) {
		t.Fatalf("Acquire() error = %v, want Redis connection error", err)
	}
}

func TestKeyEncodingIsUnambiguous(t *testing.T) {
	_, client := newTestRedis(t)
	tests := []string{"orders", "orders:blue", "订单服务", "a{b}"}
	seen := make(map[string]string, len(tests))
	for _, service := range tests {
		lease := newTestLease(t, client, service, IDRange{Min: 1, Max: 1})
		key := lease.keyFor(1)
		if previous, exists := seen[key]; exists {
			t.Fatalf("services %q and %q generated key %q", previous, service, key)
		}
		seen[key] = service
		if key == fmt.Sprintf("nodelease:{%s}:worker:1", service) && service != "orders" {
			t.Fatalf("service %q was not encoded in Redis key", service)
		}
	}
}

func TestServiceKeysShareRedisClusterHashTag(t *testing.T) {
	_, client := newTestRedis(t)
	lease := newTestLease(t, client, "orders:blue{primary}", IDRange{Min: 1, Max: 2})

	firstTag := clusterHashTag(t, lease.keyFor(1))
	secondTag := clusterHashTag(t, lease.keyFor(2))
	if firstTag != secondTag {
		t.Fatalf("cluster hash tags differ: %q != %q", firstTag, secondTag)
	}
	if strings.ContainsAny(firstTag, "{}:") {
		t.Fatalf("encoded service hash tag %q contains a key delimiter", firstTag)
	}
}

func clusterHashTag(t *testing.T, key string) string {
	t.Helper()
	start := strings.IndexByte(key, '{')
	end := strings.IndexByte(key, '}')
	if start < 0 || end <= start+1 {
		t.Fatalf("key %q does not contain a non-empty Redis Cluster hash tag", key)
	}
	return key[start+1 : end]
}
