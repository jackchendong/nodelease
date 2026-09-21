package nodelease

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

var renewScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("pexpire", KEYS[1], ARGV[2])
end
return 0
`)

var releaseScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("del", KEYS[1])
end
return 0
`)

func (lease *Lease) keyFor(workerID int64) string {
	serviceTag := base64.RawURLEncoding.EncodeToString([]byte(lease.config.serviceName))
	return lease.config.namespace + ":{" + serviceTag + "}:worker:" + strconv.FormatInt(workerID, 10)
}

func (lease *Lease) renewToken(ctx context.Context, key, token string) (bool, error) {
	result, err := renewScript.Run(
		ctx,
		lease.config.redis,
		[]string{key},
		token,
		lease.config.ttl.Milliseconds(),
	).Int64()
	if err != nil {
		return false, fmt.Errorf("renew Redis lease: %w", err)
	}
	return result == 1, nil
}

func (lease *Lease) releaseToken(ctx context.Context, key, token string) error {
	if _, err := releaseScript.Run(ctx, lease.config.redis, []string{key}, token).Int64(); err != nil {
		return fmt.Errorf("release Redis lease: %w", err)
	}
	return nil
}

func redisTTL(ttl time.Duration) time.Duration {
	return time.Duration(ttl.Milliseconds()) * time.Millisecond
}
