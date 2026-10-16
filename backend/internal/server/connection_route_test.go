package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/token"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
)

func TestConnectionRouteRejectsAPIKeysAndLimitsFreshProbes(t *testing.T) {
	jwtSvc := token.NewJWTService("test-secret", time.Hour)
	tok, err := jwtSvc.GenerateToken(1, "admin", "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	router := NewRouter(RouteConfig{
		JWTService: jwtSvc, APIKeyAuth: stubKeyValidator{}, RevealLimiter: mw.NewRateLimiter(time.Minute, 5),
		TestServerConnectionHandler: func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusNoContent) },
	})
	for _, path := range []string{"/api/v1/servers/42/connection/test", "/api/v1/servers/bad/connection/test"} {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.Header.Set("X-API-Key", "key")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || calls != 0 {
			t.Fatalf("API key reached test route: status=%d calls=%d", w.Code, calls)
		}
	}
	for i := 0; i < 6; i++ {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/servers/42/connection/test", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		want := http.StatusNoContent
		if i == 5 {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("probe %d: got %d want %d", i, w.Code, want)
		}
	}
	if calls != 5 {
		t.Fatalf("dial handler called %d times", calls)
	}
}
