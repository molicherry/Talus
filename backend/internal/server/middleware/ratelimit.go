package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vpsmanager/backend/internal/usage"
)

// RateLimiter distinguishes JWT users from API key callers and their owners.
type RateLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	maxReqs  int
	requests map[string][]time.Time
}

// NewRateLimiter creates a rate limiter with the given window and max requests.
func NewRateLimiter(window time.Duration, maxReqs int) *RateLimiter {
	return &RateLimiter{
		window:   window,
		maxReqs:  maxReqs,
		requests: make(map[string][]time.Time),
	}
}

// Allow checks if the user is within the rate limit and records the request.
func (rl *RateLimiter) Allow(userID uint) bool {
	return rl.allow("jwt:" + strconv.FormatUint(uint64(userID), 10))
}

func (rl *RateLimiter) allow(identity string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	times := rl.requests[identity]
	valid := make([]time.Time, 0, len(times))
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.maxReqs {
		rl.requests[identity] = valid
		return false
	}

	rl.requests[identity] = append(valid, now)
	return true
}

// Limit uses the verified key ID for key callers, including legacy keys whose
// owner is unknown. Keys and users with equal numeric IDs have separate quotas.
func (rl *RateLimiter) Limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := GetPrincipal(r.Context())
		identity := ""
		if principal.AuthType == "api_key" && principal.APIKeyID != nil {
			identity = "api_key:" + strconv.FormatUint(uint64(*principal.APIKeyID), 10)
		} else if principal.AuthType == "jwt" && principal.UserID != nil {
			identity = "jwt:" + strconv.FormatUint(uint64(*principal.UserID), 10)
		}
		if identity == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !rl.allow(identity) {
			if op := usage.FromContext(r.Context()); op != nil {
				op.SetResult("rejected", "rate_limited")
			}
			writeRateLimitError(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeRateLimitError(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 429, "reason": "rate_limited", "message": "too many requests", "request_id": GetRequestID(r.Context())}})
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
			writeRateLimitError(w, r)
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
