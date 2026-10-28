package middleware

import "net/http"

// SecurityHeaders keeps the management UI outside untrusted browsing contexts.
// frame-ancestors must be delivered as an HTTP header, not an HTML meta tag.
// This deliberately does not impose script/style restrictions on the SPA.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
