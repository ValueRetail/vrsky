package managementapi

import (
	"sync"

	"golang.org/x/time/rate"
)

// ConnectionRateLimiter holds per-connection rate limiters for the tenant data endpoint.
type ConnectionRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

// NewConnectionRateLimiter creates a new rate limiter registry.
func NewConnectionRateLimiter() *ConnectionRateLimiter {
	return &ConnectionRateLimiter{
		limiters: make(map[string]*rate.Limiter),
	}
}

// Allow checks whether a request for the given connection is within the rate limit.
// limitPerHour is the maximum number of requests per hour for this connection.
func (r *ConnectionRateLimiter) Allow(connectionID string, limitPerHour int) bool {
	r.mu.Lock()
	limiter, exists := r.limiters[connectionID]
	if !exists || limiter == nil {
		// Create a new limiter: rate = limitPerHour/3600 per second, burst = max(1, limitPerHour/60)
		rps := float64(limitPerHour) / 3600.0
		burst := limitPerHour / 60
		if burst < 1 {
			burst = 1
		}
		limiter = rate.NewLimiter(rate.Limit(rps), burst)
		r.limiters[connectionID] = limiter
	}
	r.mu.Unlock()

	return limiter.Allow()
}

// Remove deletes the rate limiter for a connection (e.g., on revocation).
func (r *ConnectionRateLimiter) Remove(connectionID string) {
	r.mu.Lock()
	delete(r.limiters, connectionID)
	r.mu.Unlock()
}
