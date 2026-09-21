# nodelease

English | [简体中文](README.md)

`nodelease` is a Redis-backed `worker_id` lease library for Go. Given a service name and a Redis client, it randomly acquires an available ID from a configurable range, renews the lease, and reclaims the ID after renewal stops.

It is designed for Snowflake ID generators, distributed workers, and other systems that need a small integer identifier for each running instance.

> This repository is at an initial stage. This document defines the proposed public API and behavior contract.

Repository: [github.com/jackchendong/nodelease](https://github.com/jackchendong/nodelease)

## Features

- Prevents active instances of one service from sharing an ID
- Isolates ID spaces by service name
- Supports configurable ranges and randomized allocation
- Supports automatic renewal, manual renewal, and explicit release
- Reclaims IDs after lease expiration
- Integrates with `context.Context`

## Installation

A Go library does not need a separate package registry. Once the code is in a Git repository, install it directly:

```bash
go get github.com/jackchendong/nodelease@latest
```

Then import it:

```go
import "github.com/jackchendong/nodelease"
```

The module path in this project's `go.mod` should be `github.com/jackchendong/nodelease`.

For a private repository, configure `GOPRIVATE` and authenticate Git using an SSH key, credential helper, or access token:

```bash
go env -w GOPRIVATE=git.example.com/your-team/*
go get git.example.com/your-team/nodelease@latest
```

For local development, add a temporary replacement to the consumer's `go.mod`:

```go
replace github.com/jackchendong/nodelease => ../nodelease
```

## Quick start

This example uses [`github.com/redis/go-redis/v9`](https://github.com/redis/go-redis):

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

	"github.com/redis/go-redis/v9"
	"github.com/jackchendong/nodelease"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer rdb.Close()

	lease, err := nodelease.New(nodelease.Config{
		ServiceName:   "order-service",
		Redis:         rdb,
		MinWorkerID:   0,
		MaxWorkerID:   1023,
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

	select {
	case <-ctx.Done():
	case err := <-lease.Lost():
		if !errors.Is(err, context.Canceled) {
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

## API

### `New(config Config) (*Lease, error)`

Creates a lease and validates its configuration. `New` does not access Redis; allocation starts when `Acquire` is called.

```go
type Config struct {
	ServiceName   string                // Required; service-level ID space
	Redis         redis.UniversalClient // Required; initialized go-redis client
	MinWorkerID   int64                 // Default: 0
	MaxWorkerID   int64                 // Default: 1023; inclusive
	TTL           time.Duration         // Default: 30 seconds
	RenewInterval time.Duration         // Default: TTL / 3
	DisableRenew  bool                  // Default: false (auto-renew enabled)
	Namespace     string                // Default: "nodelease"
}
```

Requirements: `ServiceName` and `Redis` are required; `0 <= MinWorkerID <= MaxWorkerID`; `TTL > 0`; and, when enabled, `0 < RenewInterval < TTL`.

### `(*Lease).Acquire(ctx) (int64, error)`

Randomly acquires an available ID. Success starts background renewal. Repeated calls return the currently held ID. An exhausted range returns `ErrNoWorkerIDAvailable`. Allocation must be atomic in Redis.

### `(*Lease).Renew(ctx) error`

Manually renews the lease. It validates the random lease token so only the owner can extend the TTL. An expired or replaced lease returns `ErrLeaseLost`.

### `(*Lease).Release(ctx) error`

Stops renewal and releases the ID. It validates the token so an old instance cannot delete a newer owner's lease. This method is idempotent.

### `(*Lease).WorkerID() (int64, bool)`

Returns the current ID and whether a lease is held.

### `(*Lease).Lost() <-chan error`

Returns the lease-loss notification channel. It reports `ErrLeaseLost` when renewal confirms expiration or changed ownership, or cannot be confirmed before the TTL expires. The caller must stop using the old ID.

## Ranges and isolation

Both range endpoints are inclusive. A range from `1` to `4` may return `1`, `2`, `3`, or `4`. Instances sharing a `ServiceName` should use the same range. Different services have independent spaces:

```text
order-service   -> worker_id 7
payment-service -> worker_id 7
```

## Lease semantics

Every allocation generates an unpredictable token:

```text
acquire ID -> hold lease -> renew periodically -> release explicitly
                              |
                              +-> process exits or disconnects -> TTL expires -> ID is reusable
```

Renewal cannot eliminate risks from long GC pauses, network partitions, or Redis failures. After `ErrLeaseLost`, stop using the old `worker_id` and either terminate or acquire a new lease according to application policy.

## Redis key design

Use one key per lease:

```text
{namespace}:{{serviceName}}:worker:{workerId}
nodelease:{order-service}:worker:37
```

The braces form a Redis Cluster hash tag. The value is a random token and the key TTL is the remaining lease lifetime.

- Acquire: `SET key token NX PX ttl`
- Renew: a Lua script verifies the token before `PEXPIRE`
- Release: a Lua script verifies the token before `DEL`

Renew and release must be atomic, not a `GET` followed by a separate write.

## Errors

The library should expose sentinel errors compatible with `errors.Is`:

- `ErrInvalidConfig`: invalid configuration
- `ErrNoWorkerIDAvailable`: every ID in the range is occupied
- `ErrLeaseLost`: the lease expired or ownership changed
- Redis errors retain their original cause for `errors.Is` and `errors.As`

## Releases

No package-registry upload is required. Create a semantic-version Git tag:

```bash
git tag v0.1.0
git push origin v0.1.0
go get github.com/jackchendong/nodelease@v0.1.0
```

A stable API conventionally starts at `v1.0.0`. Major versions `v2` and later require a module-path suffix, such as `github.com/jackchendong/nodelease/v2`.

## Operational guidance

- Use a different `Namespace` for each environment.
- Set `TTL` comfortably above expected network jitter, GC, and scheduling pauses.
- Keep the renewal interval at or below `TTL / 3`.
- Monitor ID exhaustion, renewal failures, and lease-loss events.
- Redis data loss or failover may roll lease state back. Strict uniqueness requirements also demand suitable Redis persistence and high availability.

## License

To be determined.
