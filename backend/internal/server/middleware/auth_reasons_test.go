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
