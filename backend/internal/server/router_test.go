package server

import (
	"context"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/token"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
)

// stubKeyValidator is a minimal APIKeyValidator that always succeeds.
type stubKeyValidator struct{}

func (stubKeyValidator) Validate(ctx context.Context, rawKey string) (uint, string, string, []string, []uint, error) {
	return 1, "stub", "admin", nil, nil, nil
}

// TestSummaryRouteDoesNotConflictWithIDRoute guards the chi route ordering:
// /api/v1/servers/summary is a static segment that must dispatch to the
// summary handler, never to the /{id} parameter route (and vice versa).
func TestSummaryRouteDoesNotConflictWithIDRoute(t *testing.T) {
	jwtSvc := token.NewJWTService("test-secret", 24*time.Hour)
	tok, err := jwtSvc.GenerateToken(1, "admin", "admin")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	var summaryHit, getHit, listHit bool
	router := NewRouter(RouteConfig{
		JWTService:    jwtSvc,
		APIKeyAuth:    stubKeyValidator{},
		RevealLimiter: mw.NewRateLimiter(time.Minute, 5),
		ListServersHandler: func(w http.ResponseWriter, r *http.Request) {
			listHit = true
		},
		ListServerSummariesHandler: func(w http.ResponseWriter, r *http.Request) {
			summaryHit = true
		},
		GetServerHandler: func(w http.ResponseWriter, r *http.Request) {
			getHit = true
		},
	})

	auth := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	// GET /api/v1/servers/summary must hit the summary handler, not {id}.
	rr := auth("/api/v1/servers/summary")
	if !summaryHit {
		t.Fatalf("expected /servers/summary to dispatch to ListServerSummariesHandler, got status %d", rr.Code)
	}
	if getHit {
		t.Fatal("/servers/summary wrongly matched the {id} route")
	}
	if listHit {
		t.Fatal("/servers/summary wrongly matched the list route")
	}

	// GET /api/v1/servers/42 must still hit the {id} handler.
	getHit = false
	summaryHit = false
	rr = auth("/api/v1/servers/42")
	if !getHit {
		t.Fatalf("expected /servers/42 to dispatch to GetServerHandler, got status %d", rr.Code)
	}
	if summaryHit {
		t.Fatal("/servers/42 wrongly matched the summary route")
	}
}

// TestSPAFallbackServesIndexForRoutesOnly pins the static-fallback contract:
// extensionless paths are SPA routes and get index.html, while a missing
// hashed asset must 404 so a client with a stale index.html sees a clean
// failure instead of HTML served under a .js/.css content type.
func TestSPAFallbackServesIndexForRoutesOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>spa"), 0o600); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o750); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app-abc123.js"), []byte("console.log(1)"), 0o600); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "favicon.svg"), []byte("<svg></svg>"), 0o600); err != nil {
		t.Fatalf("write favicon: %v", err)
	}

	handler := spaFallback(dir)
	cases := []struct {
		name             string
		path             string
		wantStatus       int
		wantMediaType    string
		wantCacheControl string
		wantBodyHas      string
	}{
		{
			name: "spa route falls back", path: "/servers/8",
			wantStatus: http.StatusOK, wantMediaType: "text/html",
			wantCacheControl: revalidateCacheControl, wantBodyHas: "spa",
		},
		{
			// The entry document must be revalidated on every navigation: a
			// cached copy is what points a client at deleted chunk names.
			name: "nested route falls back", path: "/api-keys",
			wantStatus: http.StatusOK, wantMediaType: "text/html",
			wantCacheControl: revalidateCacheControl, wantBodyHas: "spa",
		},
		{
			name: "trailing slash route falls back", path: "/servers/",
			wantStatus: http.StatusOK, wantMediaType: "text/html",
			wantCacheControl: revalidateCacheControl, wantBodyHas: "spa",
		},
		{
			// Hashed build output is immutable by construction.
			name: "existing hashed asset is served immutably", path: "/assets/app-abc123.js",
			wantStatus: http.StatusOK, wantMediaType: "text/javascript",
			wantCacheControl: immutableCacheControl, wantBodyHas: "console.log(1)",
		},
		{
			// Unhashed files can change between deploys, so they revalidate too.
			name: "existing unhashed file revalidates", path: "/favicon.svg",
			wantStatus: http.StatusOK, wantMediaType: "image/svg+xml",
			wantCacheControl: revalidateCacheControl, wantBodyHas: "<svg",
		},
		{
			name: "missing asset 404s", path: "/assets/app-gone.js",
			wantStatus: http.StatusNotFound, wantMediaType: "text/plain",
			wantCacheControl: "", wantBodyHas: "404",
		},
		{
			name: "missing root file 404s", path: "/favicon-gone.svg",
			wantStatus: http.StatusNotFound, wantMediaType: "text/plain",
			wantCacheControl: "", wantBodyHas: "404",
		},
		{
			// Regression: /assets/ with no extension used to fall back to
			// index.html and then inherit the immutable /assets/ policy, so the
			// entry document was cached for a year under a bogus URL.
			name: "extensionless miss under /assets 404s", path: "/assets/nonexistent",
			wantStatus: http.StatusNotFound, wantMediaType: "text/plain",
			wantCacheControl: "", wantBodyHas: "404",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			handler(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
			if body := rr.Body.String(); !strings.Contains(body, tc.wantBodyHas) {
				t.Fatalf("body = %q, want it to contain %q", body, tc.wantBodyHas)
			}
			// Assert the media type exactly, not just "is not javascript": a 404
			// must be text/plain (http.NotFound), never text/html — serving the
			// entry document under a failed asset URL is the bug being fixed.
			ct := rr.Header().Get("Content-Type")
			mediaType, _, err := mime.ParseMediaType(ct)
			if err != nil {
				t.Fatalf("unparsable Content-Type %q: %v", ct, err)
			}
			if mediaType != tc.wantMediaType {
				t.Fatalf("Content-Type = %q (media type %q), want %q", ct, mediaType, tc.wantMediaType)
			}
			if cc := rr.Header().Get("Cache-Control"); cc != tc.wantCacheControl {
				t.Fatalf("Cache-Control = %q, want %q", cc, tc.wantCacheControl)
			}
		})
	}
}
