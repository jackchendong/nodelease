# nodelease

[简体中文](README.md) | English

`nodelease` is a Redis-backed `worker_id` lease library for Go. Given a service name and a Redis client, it starts at a random position in a configured range, atomically acquires an available ID, maintains ownership through renewal, and lets Redis reclaim the ID after renewal stops.

It is designed for Snowflake ID generators, distributed workers, and other systems that need a small integer identifier for each running instance.

## Features

- Prevents active instances of one service from sharing a `worker_id`
- Isolates ID spaces by service name
- Supports an inclusive custom range; the default is `[0, 1023]`
- Uses a cryptographically random start and bounded full traversal to spread contention and reliably detect exhaustion
- Supports automatic renewal, manual renewal, and explicit release
- Uses random ownership tokens so stale instances cannot renew or delete a newer owner's lease
- Uses Redis Cluster-friendly hash-tagged keys
- Is concurrency-safe and supports `context.Context`

## Requirements

- Go 1.21 or later
- Redis 6.0 or later
- [`github.com/redis/go-redis/v9`](https://github.com/redis/go-redis)

## Installation

Install the stable version directly from GitHub with Go Modules; no separate package registry is required:

```bash
go get github.com/jackchendong/nodelease@v0.1.0
```

To follow the latest commit instead:

```bash
go get github.com/jackchendong/nodelease@latest
```

Then import it:

```go
import "github.com/jackchendong/nodelease"
```

## Quick start

```go
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackchendong/nodelease"
	"github.com/redis/go-redis/v9"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer rdb.Close()

	lease, err := nodelease.New(nodelease.Config{
		ServiceName: "order-service",
		Redis:       rdb,
		Range: &nodelease.IDRange{
			Min: 0,
			Max: 1023,
		},
		TTL:           30 * time.Second,
		RenewInterval: 10 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}

	workerID, err := lease.Acquire(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("worker_id: %d", workerID)

	// Pass workerID to Snowflake or another component that needs an instance ID.

	select {
	case <-ctx.Done():
	case err := <-lease.Lost():
		if errors.Is(err, nodelease.ErrLeaseLost) {
			log.Printf("worker_id lease lost: %v", err)
		}
	}

	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := lease.Release(releaseCtx); err != nil {
		log.Printf("release lease: %v", err)
	}
}
```

`nodelease` never closes the supplied Redis client; the caller owns its lifecycle.

## API

### `New(config Config) (*Lease, error)`

Creates a single-lifecycle lease and validates its configuration. `New` does not access Redis; allocation begins when `Acquire` is called.

```go
type IDRange struct {
	Min int64
	Max int64
}

type Config struct {
	ServiceName   string
	Redis         redis.UniversalClient
	Range         *IDRange
	TTL           time.Duration
	RenewInterval time.Duration
	DisableRenew  bool
	Namespace     string
}
```

| Field | Required | Default | Description |
|---|---:|---|---|
| `ServiceName` | yes | — | Service-level ID space; at most 256 bytes and no control characters |
| `Redis` | yes | — | An initialized go-redis client |
| `Range` | no | `[0, 1023]` | Inclusive, with at most 65,536 IDs; use `&IDRange{Min: 0, Max: 0}` for ID 0 only |
| `TTL` | no | `30s` | Redis lease lifetime; minimum `1ms` |
| `RenewInterval` | no | `TTL / 3` | Must be greater than zero and less than TTL |
| `DisableRenew` | no | `false` | Disables Redis renewal; local expiration is still reported at the deadline |
| `Namespace` | no | `nodelease` | Redis key prefix; letters, digits, `-_.:`; at most 128 bytes |

Invalid configuration returns an error matching `nodelease.ErrInvalidConfig` through `errors.Is`.

### `(*Lease).Acquire(ctx context.Context) (int64, error)`

Starts at a cryptographically random point in the range and uses Redis `SET NX PX` to atomically acquire the first available ID.

- Repeated or concurrent calls on a held `Lease` return the same ID.
- An exhausted range returns `ErrNoWorkerIDAvailable`.
- Redis and context errors retain their original cause.
- Canceling the acquisition context after success does not terminate the lease; `Release`, loss, or TTL controls its lifecycle.

### `(*Lease).Renew(ctx context.Context) error`

Manually extends the active lease by one full TTL. A Lua script first verifies the ownership token. A missing key or token mismatch returns `ErrLeaseLost`.

### `(*Lease).Release(ctx context.Context) error`

Stops the lifecycle goroutine and deletes the Redis key through a token-checking Lua script. It is idempotent, and a stale lease cannot delete a newer owner's key.

If the release command encounters a network error, the local lease still terminates and Redis reclaims the key no later than its TTL.

### `(*Lease).WorkerID() (int64, bool)`

Returns the current ID and whether the lease still considers itself the owner. The caller must stop using a previously returned ID once `ok` is false.

### `(*Lease).Lost() <-chan error`

Returns the lease-loss notification channel. It receives at most one error containing `ErrLeaseLost` after a token change, local TTL expiration, or failure to confirm automatic renewal before the deadline. A clean `Release` does not close the channel.

A successfully acquired `Lease` is single-use. After release or loss, `Acquire` returns `ErrLeaseClosed`; call `New` when another ID is needed.

## Errors

```go
workerID, err := lease.Acquire(ctx)
switch {
case err == nil:
	log.Printf("worker_id: %d", workerID)
case errors.Is(err, nodelease.ErrNoWorkerIDAvailable):
	log.Print("worker_id range is exhausted")
case errors.Is(err, context.Canceled):
	log.Print("acquisition canceled")
default:
	log.Printf("acquire worker_id: %v", err)
}
```

- `ErrInvalidConfig`: invalid configuration
- `ErrNoWorkerIDAvailable`: every ID in the range is occupied
- `ErrLeaseLost`: the lease expired, ownership changed, or renewal could not be confirmed in time
- `ErrLeaseClosed`: the single-use lease was already released or lost

## Redis keys and atomicity

Keys use this format:

```text
{namespace}:{base64url(serviceName)}:worker:{workerID}
```

ID `37` for `order-service` uses this default key:

```text
nodelease:{b3JkZXItc2VydmljZQ}:worker:37
```

The braces form a Redis Cluster hash tag, putting keys for one service in the same slot. The service name uses unpadded Base64 URL encoding to avoid delimiter collisions.

- Acquire: `SET key token NX PX ttl`
- Renew: atomically compare the token and run `PEXPIRE` in Lua
- Release: atomically compare the token and run `DEL` in Lua

The library does not use `KEYS` or `SCAN` to find available IDs.

## Guarantees and limitations

- Uniqueness depends on the target Redis deployment retaining successful writes.
- A network timeout can leave acquisition outcome uncertain; such an unrenewed key expires automatically after its TTL.
- Redis data loss, asynchronous replication, or failover can roll lease state back. Strict uniqueness requirements also need suitable Redis persistence and high availability.
- Long GC/scheduler pauses and network partitions can cause lease loss. Callers must monitor `Lost()` and stop using an old ID after loss.
- All instances sharing a `ServiceName` should use the same range, namespace, and lease policy.

## Development and testing

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

## License

[MIT License](LICENSE). Anyone may use, copy, modify, merge, publish, distribute, sublicense, and sell this software, provided that the copyright and license notice is retained.
