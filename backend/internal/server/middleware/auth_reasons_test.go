package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/server"
	"github.com/vpsmanager/backend/internal/server/middleware"
)

// The auth middleware writes its own error envelope and cannot import the server
// package, so its reason strings are duplicated. This drives the REAL middleware
// (a frontend mock cannot cover it) and pins those responses to the constants the
// rest of the API and the UI translations use.
func TestAuthMiddlewareReasons(t *testing.T) {
	jwtSvc := token.NewJWTService("test-secret", time.Hour)
	validToken, err := jwtSvc.GenerateToken(1, "admin", "admin")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	// A key with no scopes is authenticated but not allowed anywhere scoped.
	noScope := middleware.APIKeyValidatorFunc(func(context.Context, string) (uint, string, string, []string, []uint, error) {
		return 1, "admin", "admin", nil, nil, nil
	})
	badKey := middleware.APIKeyValidatorFunc(func(context.Context, string) (uint, string, string, []string, []uint, error) {
		return 0, "", "", nil, nil, errors.New("nope")
	})

	handler := func(next middleware.APIKeyValidator) http.Handler {
		return middleware.Auth(jwtSvc, next)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	}

	cases := []struct {
		name       string
		validator  middleware.APIKeyValidator
		apiKey     string
		bearer     string
		wantStatus int
		wantReason string
	}{
		{"no credentials", noScope, "", "", http.StatusUnauthorized, server.ReasonUnauthorized},
		{"malformed bearer", noScope, "", "not-a-jwt", http.StatusUnauthorized, server.ReasonUnauthorized},
		{"rejected api key", badKey, "tk_bad", "", http.StatusUnauthorized, server.ReasonUnauthorized},
		// GET /api/v1/servers requires servers:read, which this key lacks.
		{"api key without the scope", noScope, "tk_ok", "", http.StatusForbidden, server.ReasonForbidden},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
			if c.apiKey != "" {
				req.Header.Set("X-API-Key", c.apiKey)
			}
			if c.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+c.bearer)
			}
			rec := httptest.NewRecorder()
			handler(c.validator).ServeHTTP(rec, req)

			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, c.wantStatus, rec.Body)
			}
			want := `"reason":"` + c.wantReason + `"`
			if !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(rec.Body.String()) {
				t.Errorf("body %s does not carry %s", rec.Body, want)
			}
		})
	}

	t.Run("a valid token passes through", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
		req.Header.Set("Authorization", "Bearer "+validToken)
		rec := httptest.NewRecorder()
		handler(noScope).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
		}
	})
}

// TestWebSocketUpgradeOnlyBypassesAuthForTerminal guards the auth bypass: the
// middleware used to skip authentication for ANY request carrying
// `Upgrade: websocket`, so an unauthenticated client could reach every
// protected endpoint by setting that header. Only the terminal handshake may
// pass through (it authenticates the first WebSocket message).
func TestWebSocketUpgradeOnlyBypassesAuthForTerminal(t *testing.T) {
	jwtSvc := token.NewJWTService("test-secret", time.Hour)
	keyValidator := middleware.APIKeyValidatorFunc(func(context.Context, string) (uint, string, string, []string, []uint, error) {
		return 1, "admin", "admin", []string{"*"}, nil, nil
	})

	reached := false
	handler := middleware.Auth(jwtSvc, keyValidator)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{"servers list with upgrade header", http.MethodGet, "/api/v1/servers", http.StatusUnauthorized},
		{"trust route with upgrade header", http.MethodPost, "/api/v1/servers/5/host-key/trust", http.StatusUnauthorized},
		{"exec with upgrade header", http.MethodPost, "/api/v1/servers/5/exec", http.StatusUnauthorized},
		{"terminal handshake passes through", http.MethodGet, "/api/v1/servers/5/terminal", http.StatusOK},
		{"non-terminal websocket path passes through only if it is terminal", http.MethodGet, "/api/v1/servers/5/metrics", http.StatusUnauthorized},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(c.method, c.path, nil)
			req.Header.Set("Upgrade", "websocket")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, c.wantStatus)
			}
			if c.wantStatus == http.StatusOK && !reached {
				t.Fatal("terminal handshake should reach the handler")
			}
			if c.wantStatus != http.StatusOK && reached {
				t.Fatal("unauthenticated request reached a protected handler")
			}
		})
	}
}
