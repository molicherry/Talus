package middleware

import (
	"net/http"
	"strings"
	"time"
)

// Write budgets. A single global http.Server.WriteTimeout cannot serve both
// ordinary CRUD (short protection) and long operations (Exec up to 300s, bounded
// Relay streams up to 300s), so the global timeout is disabled and each request
// gets a budget here.
const (
	// DefaultWriteBudget protects ordinary requests, matching the historical 15s.
	DefaultWriteBudget = 15 * time.Second
	// ServerDeleteWriteBudget covers the deletion's three bounded phases --
	// 20s to prepare/pin the route, 10s to commit, 20s to uninstall the agent --
	// plus 5s of credential cleanup and response margin.
	ServerDeleteWriteBudget = 60 * time.Second
	// ExecWriteBudget covers the 300s Exec cap plus result-return margin.
	ExecWriteBudget = 310 * time.Second
	// RelayWriteBudget covers a 300s bounded_stream plus margin.
	RelayWriteBudget = 330 * time.Second
)

// writeBudgetFor returns the write budget for a request. Long routes are matched
// by method and path shape, independent of router internals.
func writeBudgetFor(r *http.Request) time.Duration {
	p := r.URL.Path
	if r.Method == http.MethodGet && p == "/api/v1/agent/connect" {
		return 0
	}
	if r.Method == http.MethodDelete {
		if id, ok := strings.CutPrefix(strings.TrimSuffix(p, "/"), "/api/v1/servers/"); ok && id != "" && !strings.Contains(id, "/") {
			return ServerDeleteWriteBudget
		}
	}
	if strings.HasPrefix(p, "/api/v1/servers/") && strings.Contains(p, "/sftp/") {
		if strings.Contains(p, "/sftp/uploads/") {
			return 10*time.Minute + 10*time.Second
		}
		if (r.Method == http.MethodPost && strings.HasSuffix(p, "/upload")) || (r.Method == http.MethodGet && (strings.HasSuffix(p, "/download") || strings.HasSuffix(p, "/archive"))) {
			return 10*time.Minute + 10*time.Second
		}
		return 35 * time.Second
	}
	if r.Method == http.MethodPost {
		if strings.HasSuffix(p, "/exec") && strings.Contains(p, "/servers/") {
			return ExecWriteBudget
		}
		if strings.HasSuffix(p, "/relay") && strings.Contains(p, "/services/") {
			return RelayWriteBudget
		}
	}
	return DefaultWriteBudget
}

// WriteDeadline sets a per-request write deadline. It is a no-op when the
// ResponseWriter does not support deadlines (e.g. HTTP/2), where the server's
// own configuration applies. It must be installed early in the stack so it
// covers the whole handler chain.
func WriteDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		budget := writeBudgetFor(r)
		if budget > 0 {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(budget))
		}
		next.ServeHTTP(w, r)
	})
}
