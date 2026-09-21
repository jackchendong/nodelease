package nodelease_test

import (
	"context"
	"time"

	"github.com/jackchendong/nodelease"
	"github.com/redis/go-redis/v9"
)

func Example() {
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer client.Close()

	lease, err := nodelease.New(nodelease.Config{
		ServiceName: "order-service",
		Redis:       client,
		Range:       &nodelease.IDRange{Min: 0, Max: 1023},
		TTL:         30 * time.Second,
	})
	if err != nil {
		return
	}

	workerID, err := lease.Acquire(context.Background())
	if err != nil {
		return
	}
	_ = workerID // Pass the ID to Snowflake or another distributed component.

	_ = lease.Release(context.Background())
}
