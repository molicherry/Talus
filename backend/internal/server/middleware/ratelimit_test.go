package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/usage"
)

func TestRevealLimiterSeparatesKeysFromOwnerAndUsers(t *testing.T) {
	rl := NewRateLimiter(time.Minute, 1)
	owner, key1, key2 := uint(10), uint(10), uint(11)
	check := func(p usage.Principal) int {
		r := httptest.NewRequest("GET", "/api/v1/credentials/1/reveal", nil)
		r = r.WithContext(WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		rl.Limit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })).ServeHTTP(w, r)
		return w.Code
	}
	first := usage.Principal{AuthType: "api_key", APIKeyID: &key1, UserID: &owner}
	if check(first) != 200 || check(first) != 429 {
		t.Fatal("key quota not enforced")
	}
	if check(usage.Principal{AuthType: "api_key", APIKeyID: &key2, UserID: &owner}) != 200 {
		t.Fatal("keys sharing an owner must have separate quotas")
	}
	if check(usage.Principal{AuthType: "jwt", UserID: &owner}) != 200 {
		t.Fatal("user and key numeric IDs must not collide")
	}
	unknownKey := uint(12)
	if check(usage.Principal{AuthType: "api_key", APIKeyID: &unknownKey}) != 200 {
		t.Fatal("legacy key owner must not collapse into user zero")
	}
}

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
