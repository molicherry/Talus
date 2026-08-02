package server

import (
	"context"
	"net/http"
	"net/http/httptest"
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
