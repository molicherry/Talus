package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeadersBeforeResponse(t *testing.T) {
	for _, route := range []string{"/", "/login", "/setup", "/servers", "/index.html", "/missing.js"} {
		t.Run(route, func(t *testing.T) {
			rr := httptest.NewRecorder()
			SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			})).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, route, nil))
			response := rr.Result()
			defer response.Body.Close()
			if got := response.Header.Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
				t.Fatalf("CSP = %q", got)
			}
			if got := response.Header.Get("X-Frame-Options"); got != "DENY" {
				t.Fatalf("X-Frame-Options = %q", got)
			}
		})
	}
}
