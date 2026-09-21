package nodelease

import (
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestNewDefaults(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	t.Cleanup(func() { _ = client.Close() })

	lease, err := New(Config{ServiceName: "orders", Redis: client})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if lease.config.minWorkerID != 0 || lease.config.maxWorkerID != 1023 {
		t.Fatalf("default range = [%d, %d], want [0, 1023]", lease.config.minWorkerID, lease.config.maxWorkerID)
	}
	if lease.config.ttl != 30*time.Second {
		t.Fatalf("default TTL = %s, want 30s", lease.config.ttl)
	}
	if lease.config.renewInterval != 10*time.Second {
		t.Fatalf("default renew interval = %s, want 10s", lease.config.renewInterval)
	}
	if !lease.config.autoRenew {
		t.Fatal("automatic renewal is disabled by default")
	}
	if lease.config.namespace != "nodelease" {
		t.Fatalf("default namespace = %q, want nodelease", lease.config.namespace)
	}
}

func TestNewAcceptsSingleZeroIDRange(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	t.Cleanup(func() { _ = client.Close() })

	lease, err := New(Config{
		ServiceName: "orders",
		Redis:       client,
		Range:       &IDRange{Min: 0, Max: 0},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if lease.config.minWorkerID != 0 || lease.config.maxWorkerID != 0 {
		t.Fatalf("range = [%d, %d], want [0, 0]", lease.config.minWorkerID, lease.config.maxWorkerID)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	t.Cleanup(func() { _ = client.Close() })

	tests := []struct {
		name   string
		config Config
	}{
		{name: "empty service", config: Config{Redis: client}},
		{name: "control in service", config: Config{ServiceName: "orders\n", Redis: client}},
		{name: "nil Redis", config: Config{ServiceName: "orders"}},
		{name: "negative minimum", config: Config{ServiceName: "orders", Redis: client, Range: &IDRange{Min: -1, Max: 1}}},
		{name: "reversed range", config: Config{ServiceName: "orders", Redis: client, Range: &IDRange{Min: 2, Max: 1}}},
		{name: "range too large", config: Config{ServiceName: "orders", Redis: client, Range: &IDRange{Min: 0, Max: maxRangeSize}}},
		{name: "TTL below Redis precision", config: Config{ServiceName: "orders", Redis: client, TTL: time.Microsecond}},
		{name: "negative renew interval", config: Config{ServiceName: "orders", Redis: client, RenewInterval: -time.Second}},
		{name: "renew interval equals TTL", config: Config{ServiceName: "orders", Redis: client, TTL: time.Second, RenewInterval: time.Second}},
		{name: "unsafe namespace", config: Config{ServiceName: "orders", Redis: client, Namespace: "node{lease}"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.config)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New() error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}
