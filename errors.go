package nodelease

import "errors"

var (
	// ErrInvalidConfig indicates that Config contains an invalid value or a
	// required dependency is missing.
	ErrInvalidConfig = errors.New("nodelease: invalid configuration")

	// ErrNoWorkerIDAvailable indicates that every worker ID in the configured
	// range was occupied when acquisition was attempted.
	ErrNoWorkerIDAvailable = errors.New("nodelease: no worker ID available")

	// ErrLeaseLost indicates that the lease expired, its ownership token no
	// longer matches, or renewal could not be confirmed before expiration.
	ErrLeaseLost = errors.New("nodelease: lease lost")

	// ErrLeaseClosed indicates that a single-use Lease was already released or
	// lost. Create a new Lease to acquire another worker ID.
	ErrLeaseClosed = errors.New("nodelease: lease closed")
)
