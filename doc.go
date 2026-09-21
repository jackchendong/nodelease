// Package nodelease allocates short-lived, service-scoped worker IDs through
// Redis. A Lease owns at most one worker ID and uses a random ownership token
// so that stale processes cannot renew or release a newer owner's lease.
//
// A Lease is single-use: after it is released or lost, create a new Lease to
// acquire another worker ID. The caller owns the Redis client passed to New;
// this package never closes it.
package nodelease
