package middleware

import (
	"net/http"
	"sync"
	"time"
)

// RateLimiter implements a simple per-user in-memory rate limiter.
type RateLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	maxReqs  int
	requests map[uint][]time.Time
}

// NewRateLimiter creates a rate limiter with the given window and max requests.
func NewRateLimiter(window time.Duration, maxReqs int) *RateLimiter {
	return &RateLimiter{
		window:   window,
		maxReqs:  maxReqs,
		requests: make(map[uint][]time.Time),
	}
}

// Allow checks if the user is within the rate limit and records the request.
func (rl *RateLimiter) Allow(userID uint) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	times := rl.requests[userID]
	valid := make([]time.Time, 0, len(times))
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.maxReqs {
		rl.requests[userID] = valid
		return false
	}

	rl.requests[userID] = append(valid, now)
	return true
}

// Limit returns a middleware that rate-limits requests per user.
// User ID is extracted from the request context via GetUserClaims.
func (rl *RateLimiter) Limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetUserClaims(r.Context())
		if claims == nil {
			next.ServeHTTP(w, r)
			return
		}
		if !rl.Allow(claims.UserID) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"code":429,"message":"too many requests"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
