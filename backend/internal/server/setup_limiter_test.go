package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/server/middleware"
)

func setupLimiterRequest(router http.Handler, method, route, remote, forwarded string) int {
	req := httptest.NewRequest(method, route, nil)
	req.RemoteAddr = remote
	if forwarded != "" {
		req.Header.Set("X-Forwarded-For", forwarded)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code
}

func TestSetupRateLimitIsIndependentFromLogin(t *testing.T) {
	loginCalls, setupCalls := 0, 0
	login := func(w http.ResponseWriter, _ *http.Request) {
		loginCalls++
		w.WriteHeader(http.StatusOK)
	}
	setup := func(w http.ResponseWriter, _ *http.Request) {
		setupCalls++
		w.WriteHeader(http.StatusOK)
	}
	router := NewRouter(RouteConfig{
		LoginLimiter:     middleware.NewIPRateLimiter(time.Hour, 2, false),
		SetupLimiter:     middleware.NewIPRateLimiter(time.Hour, 4, false),
		LoginHandler:     login,
		SetupHandler:     setup,
		BootstrapHandler: setup,
	})
	request := func(method, route string, want int) {
		t.Helper()
		if got := setupLimiterRequest(router, method, route, "198.51.100.10:1234", ""); got != want {
			t.Fatalf("%s %s status=%d, want %d", method, route, got, want)
		}
	}
	for range 2 {
		request(http.MethodPost, "/api/v1/auth/login", http.StatusOK)
	}
	request(http.MethodPost, "/api/v1/auth/login", http.StatusTooManyRequests)
	// Both discovery and submission remain usable after the login budget is gone.
	request(http.MethodGet, "/api/v1/auth/setup", http.StatusOK)
	request(http.MethodPost, "/api/v1/auth/setup", http.StatusOK)
	request(http.MethodGet, "/api/v1/auth/setup", http.StatusOK)
	request(http.MethodPost, "/api/v1/auth/setup", http.StatusOK)
	request(http.MethodGet, "/api/v1/auth/setup", http.StatusTooManyRequests)
	request(http.MethodPost, "/api/v1/auth/setup", http.StatusTooManyRequests)
	request(http.MethodPost, "/api/v1/auth/login", http.StatusTooManyRequests)
	if loginCalls != 2 || setupCalls != 4 {
		t.Fatalf("rate-limited calls reached handlers: login=%d setup=%d", loginCalls, setupCalls)
	}
	if got := setupLimiterRequest(router, http.MethodPost, "/api/v1/auth/setup", "198.51.100.11:1234", ""); got != http.StatusOK {
		t.Fatalf("another client inherited the exhausted setup quota: %d", got)
	}
}

func TestSetupRateLimitFallbackIsBoundedAndIndependent(t *testing.T) {
	noop := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	router := NewRouter(RouteConfig{LoginHandler: noop, SetupHandler: noop, BootstrapHandler: noop})
	request := func(method, route string, want int) {
		t.Helper()
		if got := setupLimiterRequest(router, method, route, "198.51.100.20:1234", ""); got != want {
			t.Fatalf("%s %s status=%d, want %d", method, route, got, want)
		}
	}
	for range 10 {
		request(http.MethodPost, "/api/v1/auth/login", http.StatusNoContent)
	}
	request(http.MethodPost, "/api/v1/auth/login", http.StatusTooManyRequests)
	for i := 0; i < SetupRequestsPerMinute; i++ {
		method := http.MethodGet
		if i%2 != 0 {
			method = http.MethodPost
		}
		request(method, "/api/v1/auth/setup", http.StatusNoContent)
	}
	request(http.MethodPost, "/api/v1/auth/setup", http.StatusTooManyRequests)
}

func TestSetupRateLimitUsesConfiguredProxyPolicy(t *testing.T) {
	for _, trustProxy := range []bool{false, true} {
		name := "direct"
		if trustProxy {
			name = "trusted_proxy"
		}
		t.Run(name, func(t *testing.T) {
			noop := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
			router := NewRouter(RouteConfig{
				SetupLimiter:     middleware.NewIPRateLimiter(time.Minute, 1, trustProxy),
				SetupHandler:     noop,
				BootstrapHandler: noop,
			})
			request := func(forwarded string, want int) {
				t.Helper()
				got := setupLimiterRequest(router, http.MethodPost, "/api/v1/auth/setup", "192.0.2.1:1234", forwarded)
				if got != want {
					t.Fatalf("forwarded=%q status=%d, want %d", forwarded, got, want)
				}
			}
			request("spoofed-one, 198.51.100.30", http.StatusNoContent)
			// Changing the caller-supplied prefix cannot change the trusted identity.
			request("spoofed-two, 198.51.100.30", http.StatusTooManyRequests)
			want := http.StatusTooManyRequests
			if trustProxy {
				want = http.StatusNoContent
			}
			request("spoofed-two, 198.51.100.31", want)
		})
	}
}
