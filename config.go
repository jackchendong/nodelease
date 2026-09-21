package nodelease

import (
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/redis/go-redis/v9"
)

const (
	defaultMinWorkerID = int64(0)
	defaultMaxWorkerID = int64(1023)
	defaultTTL         = 30 * time.Second
	defaultNamespace   = "nodelease"
	maxRangeSize       = int64(65_536)
	maxServiceNameLen  = 256
	maxNamespaceLen    = 128
)

// IDRange is an inclusive worker ID range. A nil Config.Range uses 0 through
// 1023. Use &IDRange{Min: 0, Max: 0} to request a single-ID range.
type IDRange struct {
	Min int64
	Max int64
}

// Config configures a Lease.
type Config struct {
	// ServiceName identifies an independent worker ID space.
	ServiceName string

	// Redis is an initialized go-redis client. The caller retains ownership.
	Redis redis.UniversalClient

	// Range is inclusive. Nil uses the default range 0 through 1023.
	Range *IDRange

	// TTL is the lease lifetime. Zero uses 30 seconds.
	TTL time.Duration

	// RenewInterval controls automatic renewal. Zero uses TTL / 3.
	RenewInterval time.Duration

	// DisableRenew disables Redis renewal. Expiration is still reported through
	// Lost and WorkerID stops reporting ownership after the local deadline.
	DisableRenew bool

	// Namespace prefixes Redis keys. Empty uses "nodelease".
	Namespace string
}

type normalizedConfig struct {
	serviceName   string
	redis         redis.UniversalClient
	minWorkerID   int64
	maxWorkerID   int64
	ttl           time.Duration
	renewInterval time.Duration
	autoRenew     bool
	namespace     string
}

func normalizeConfig(config Config) (normalizedConfig, error) {
	if strings.TrimSpace(config.ServiceName) == "" {
		return normalizedConfig{}, invalidConfig("service name must not be empty")
	}
	if len(config.ServiceName) > maxServiceNameLen {
		return normalizedConfig{}, invalidConfig("service name exceeds %d bytes", maxServiceNameLen)
	}
	for _, r := range config.ServiceName {
		if unicode.IsControl(r) {
			return normalizedConfig{}, invalidConfig("service name must not contain control characters")
		}
	}
	if isNil(config.Redis) {
		return normalizedConfig{}, invalidConfig("Redis client must not be nil")
	}

	workerRange := IDRange{Min: defaultMinWorkerID, Max: defaultMaxWorkerID}
	if config.Range != nil {
		workerRange = *config.Range
	}
	if workerRange.Min < 0 {
		return normalizedConfig{}, invalidConfig("minimum worker ID must not be negative")
	}
	if workerRange.Max < workerRange.Min {
		return normalizedConfig{}, invalidConfig("maximum worker ID must be greater than or equal to minimum")
	}
	if workerRange.Max-workerRange.Min >= maxRangeSize {
		return normalizedConfig{}, invalidConfig("worker ID range must contain at most %d IDs", maxRangeSize)
	}

	ttl := config.TTL
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl < time.Millisecond {
		return normalizedConfig{}, invalidConfig("TTL must be at least %s", time.Millisecond)
	}

	renewInterval := config.RenewInterval
	if renewInterval == 0 {
		renewInterval = ttl / 3
		if renewInterval == 0 {
			renewInterval = ttl / 2
		}
	}
	if renewInterval <= 0 || renewInterval >= ttl {
		return normalizedConfig{}, invalidConfig("renew interval must be greater than zero and less than TTL")
	}

	namespace := config.Namespace
	if namespace == "" {
		namespace = defaultNamespace
	}
	if err := validateNamespace(namespace); err != nil {
		return normalizedConfig{}, err
	}

	return normalizedConfig{
		serviceName:   config.ServiceName,
		redis:         config.Redis,
		minWorkerID:   workerRange.Min,
		maxWorkerID:   workerRange.Max,
		ttl:           ttl,
		renewInterval: renewInterval,
		autoRenew:     !config.DisableRenew,
		namespace:     namespace,
	}, nil
}

func validateNamespace(namespace string) error {
	if len(namespace) > maxNamespaceLen {
		return invalidConfig("namespace exceeds %d bytes", maxNamespaceLen)
	}
	for _, r := range namespace {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' || r == ':'
		if !valid {
			return invalidConfig("namespace contains unsupported character %q", r)
		}
	}
	return nil
}

func invalidConfig(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
