package middleware

import (
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/config"
)

func TestRuntimeLoginLimitPreservesWindowCounts(t *testing.T) {
	r := config.NewRuntime(config.Config{LoginRateLimit: 2})
	limiter := NewIPRateLimiter(time.Minute, 10, false)
	limiter.SetRuntimeConfig(r)
	if !limiter.Allow("client") || !limiter.Allow("client") || limiter.Allow("client") {
		t.Fatal("initial runtime limit ignored")
	}
	r.Store(config.Config{LoginRateLimit: 3})
	if !limiter.Allow("client") || limiter.Allow("client") {
		t.Fatal("limit update reset counts or did not apply")
	}
	r.Store(config.Config{LoginRateLimit: 1})
	if limiter.Allow("client") || !limiter.Allow("another-client") {
		t.Fatal("lowered limit ignored or clients shared counts")
	}
}

func TestRuntimeSettingsDenyWildcardAPIKeys(t *testing.T) {
	for _, method := range []string{"GET", "PUT"} {
		if allowed, _ := hasScope(method, "/api/v1/settings", []string{"*"}); allowed {
			t.Fatalf("%s accepted wildcard API key", method)
		}
	}
}
