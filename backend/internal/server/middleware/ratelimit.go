package middleware

import (
	"net"
	"net/http"
	"strings"
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

// IPRateLimiter implements a simple per-client-IP in-memory rate limiter.
// Unlike RateLimiter (keyed by authenticated user ID), it keys on the client
// IP address so unauthenticated endpoints such as login can be protected.
type IPRateLimiter struct {
	mu         sync.Mutex
	window     time.Duration
	maxReqs    int
	requests   map[string][]time.Time
	trustProxy bool
	lastSweep  time.Time
}

// NewIPRateLimiter creates a per-IP rate limiter. Set trustProxy only when
// running behind a reverse proxy that overwrites the X-Forwarded-For header;
// otherwise XFF can be spoofed to bypass the limit.
func NewIPRateLimiter(window time.Duration, maxReqs int, trustProxy bool) *IPRateLimiter {
	return &IPRateLimiter{
		window:     window,
		maxReqs:    maxReqs,
		requests:   make(map[string][]time.Time),
		trustProxy: trustProxy,
	}
}

// Allow checks if the client IP is within the rate limit and records the request.
func (rl *IPRateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Periodically evict fully-expired keys so the map cannot grow without
	// bound: keys are attacker-controlled IPs on a public endpoint.
	if now.Sub(rl.lastSweep) >= rl.window {
		for k, ts := range rl.requests {
			if len(ts) == 0 || ts[len(ts)-1].Before(cutoff) {
				delete(rl.requests, k)
			}
		}
		rl.lastSweep = now
	}

	times := rl.requests[ip]
	valid := make([]time.Time, 0, len(times))
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.maxReqs {
		rl.requests[ip] = valid
		return false
	}

	rl.requests[ip] = append(valid, now)
	return true
}

// Limit returns middleware that rate-limits requests by client IP address.
func (rl *IPRateLimiter) Limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.Allow(clientIP(r, rl.trustProxy)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"code":429,"message":"too many requests"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP extracts the client IP from the request. With trustProxy, the
// rightmost X-Forwarded-For entry is used — the one appended by the trusted
// reverse proxy immediately in front of us. Earlier entries may be
// client-supplied and spoofable. Otherwise the RemoteAddr host is used, the
// only value that cannot be spoofed when the server is exposed directly.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.LastIndexByte(xff, ','); i >= 0 {
				xff = xff[i+1:]
			}
			if ip := strings.TrimSpace(xff); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
