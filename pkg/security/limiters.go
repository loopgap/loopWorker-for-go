package security

import (
	"time"
)

// TieredRateLimiter applies a stricter budget to unauthenticated traffic and a
// looser one to credentialed callers, keyed by a stable bucket id.
type TieredRateLimiter struct {
	anonymous     *RateLimiter
	authenticated *RateLimiter
}

// NewTieredRateLimiter builds both tiers. A non-positive window falls back to
// the defaults used by the shipped configuration.
func NewTieredRateLimiter(anonRate int, anonWindow time.Duration, authRate int, authWindow time.Duration) *TieredRateLimiter {
	return &TieredRateLimiter{
		anonymous:     NewRateLimiter(anonRate, anonWindow),
		authenticated: NewRateLimiter(authRate, authWindow),
	}
}

// Allow reports whether the bucket may perform one more request.
func (t *TieredRateLimiter) Allow(key string, authenticated bool) bool {
	return t.AllowCost(key, authenticated, 1)
}

// AllowCost consumes cost units from the bucket.
func (t *TieredRateLimiter) AllowCost(key string, authenticated bool, cost int) bool {
	if cost <= 0 {
		cost = 1
	}
	if authenticated {
		for i := 0; i < cost; i++ {
			if !t.authenticated.Allow(key) {
				return false
			}
		}
		return true
	}
	for i := 0; i < cost; i++ {
		if !t.anonymous.Allow(key) {
			return false
		}
	}
	return true
}

// RetryAfter reports how long to wait before the bucket is usable again.
func (t *TieredRateLimiter) RetryAfter(key string, authenticated bool) time.Duration {
	if authenticated {
		return t.authenticated.RetryAfter(key)
	}
	return t.anonymous.RetryAfter(key)
}

// Reset forgets a bucket in both tiers.
func (t *TieredRateLimiter) Reset(key string) {
	t.anonymous.Reset(key)
	t.authenticated.Reset(key)
}

// Stop terminates both cleanup goroutines.
func (t *TieredRateLimiter) Stop() {
	t.anonymous.Stop()
	t.authenticated.Stop()
}
