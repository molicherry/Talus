package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientIPNoTrustProxy(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	r.RemoteAddr = "203.0.113.7:12345"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	if got := clientIP(r, false); got != "203.0.113.7" {
		t.Fatalf("expected RemoteAddr host, got %q", got)
	}
}

func TestClientIPTrustProxy(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	r.RemoteAddr = "10.0.0.1:12345"
	// The trusted proxy appends the real client IP (rightmost); the leftmost
	// entry is client-supplied and must not be trusted.
	r.Header.Set("X-Forwarded-For", "198.51.100.9, 203.0.113.5")
	if got := clientIP(r, true); got != "203.0.113.5" {
		t.Fatalf("expected rightmost XFF entry, got %q", got)
	}
}

func TestIPRateLimiterAllow(t *testing.T) {
	rl := NewIPRateLimiter(time.Minute, 2, false)
	if !rl.Allow("1.2.3.4") {
		t.Fatal("first request should be allowed")
	}
	if !rl.Allow("1.2.3.4") {
		t.Fatal("second request should be allowed")
	}
	if rl.Allow("1.2.3.4") {
		t.Fatal("third request should be rejected")
	}
	if !rl.Allow("5.6.7.8") {
		t.Fatal("a different IP should not be limited by another IP's count")
	}
}

func TestIPRateLimiterEvictsExpiredKeys(t *testing.T) {
	rl := NewIPRateLimiter(time.Minute, 10, false)
	// Seed an expired key and force the periodic sweep to run.
	rl.requests["stale"] = []time.Time{time.Now().Add(-time.Hour)}
	rl.lastSweep = time.Now().Add(-time.Minute)

	if !rl.Allow("1.2.3.4") {
		t.Fatal("request should be allowed")
	}
	if _, ok := rl.requests["stale"]; ok {
		t.Fatal("expired key should have been evicted")
	}
	if _, ok := rl.requests["1.2.3.4"]; !ok {
		t.Fatal("active key should still be present")
	}
}
