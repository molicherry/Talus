package middleware

import (
	"testing"
)

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		expected string
	}{
		{"servers list", "GET", "/api/v1/servers", "GET /api/v1/servers"},
		{"servers list trailing slash", "GET", "/api/v1/servers/", "GET /api/v1/servers"},
		{"server by id", "GET", "/api/v1/servers/42", "GET /api/v1/servers/{id}"},
		{"server update", "PUT", "/api/v1/servers/7", "PUT /api/v1/servers/{id}"},
		{"server exec", "POST", "/api/v1/servers/1/exec", "POST /api/v1/servers/{id}/exec"},
		{"server exec trailing slash", "POST", "/api/v1/servers/1/exec/", "POST /api/v1/servers/{id}/exec"},
		{"server metrics", "GET", "/api/v1/servers/99/metrics", "GET /api/v1/servers/{id}/metrics"},
		{"server terminal", "GET", "/api/v1/servers/3/terminal", "GET /api/v1/servers/{id}/terminal"},
		{"credentials list", "GET", "/api/v1/credentials", "GET /api/v1/credentials"},
		{"credential update", "PUT", "/api/v1/credentials/5", "PUT /api/v1/credentials/{id}"},
		{"api keys list", "GET", "/api/v1/api-keys", "GET /api/v1/api-keys"},
		{"api keys delete", "DELETE", "/api/v1/api-keys/2", "DELETE /api/v1/api-keys/{id}"},
		{"auth profile", "GET", "/api/v1/auth/profile", "GET /api/v1/auth/profile"},
		{"auth password", "PUT", "/api/v1/auth/password", "PUT /api/v1/auth/password"},
		{"non-numeric segment preserved", "GET", "/api/v1/servers/abc", "GET /api/v1/servers/abc"},
		{"health check untouched", "GET", "/healthz", "GET /healthz"},
		{"version endpoint", "GET", "/api/v1/", "GET /api/v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizePath(tt.method, tt.path)
			if got != tt.expected {
				t.Errorf("normalizePath(%q, %q) = %q, want %q", tt.method, tt.path, got, tt.expected)
			}
		})
	}
}

func TestHasScope(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		userScopes  []string
		wantAllowed bool
		wantMsg     string
	}{
		// Scope-gated: granted
		{"read servers with scope", "GET", "/api/v1/servers", []string{"servers:read"}, true, ""},
		{"write servers with scope", "POST", "/api/v1/servers", []string{"servers:write"}, true, ""},
		{"exec with scope", "POST", "/api/v1/servers/1/exec", []string{"servers:exec"}, true, ""},
		{"metrics with scope", "GET", "/api/v1/servers/1/metrics", []string{"metrics:read"}, true, ""},
		{"credentials read with scope", "GET", "/api/v1/credentials", []string{"credentials:read"}, true, ""},
		{"wildcard grants all scope-gated", "POST", "/api/v1/servers", []string{"*"}, true, ""},
		{"multi-scope keys", "GET", "/api/v1/servers", []string{"servers:read", "servers:exec"}, true, ""},

		// Scope-gated: denied
		{"read scope missing for write", "POST", "/api/v1/servers", []string{"servers:read"}, false, "servers:write"},
		{"no server scope", "POST", "/api/v1/servers/1/exec", []string{"servers:read"}, false, "servers:exec"},
		{"wrong scope entirely", "POST", "/api/v1/servers", []string{"metrics:read"}, false, "servers:write"},
		{"empty scopes denied", "GET", "/api/v1/servers", []string{}, false, "servers:read"},

		// JWT-only: always denied
		{"jwt-only delete server", "DELETE", "/api/v1/servers/1", []string{"servers:write"}, false, ""},
		{"jwt-only create credential", "POST", "/api/v1/credentials", []string{"servers:write", "credentials:read"}, false, ""},
		{"jwt-only list api keys", "GET", "/api/v1/api-keys", []string{"*"}, false, ""},
		{"jwt-only create api key", "POST", "/api/v1/api-keys", []string{"*"}, false, ""},
		{"jwt-only delete api key", "DELETE", "/api/v1/api-keys/1", []string{"*"}, false, ""},
		{"jwt-only auth profile", "GET", "/api/v1/auth/profile", []string{"*"}, false, ""},
		{"jwt-only auth password", "PUT", "/api/v1/auth/password", []string{"*"}, false, ""},

		// Unmapped routes: always allowed
		{"unmapped route allowed", "GET", "/healthz", []string{}, true, ""},
		{"version endpoint allowed", "GET", "/api/v1/", []string{}, true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed, msg := hasScope(tt.method, tt.path, tt.userScopes)
			if allowed != tt.wantAllowed {
				t.Errorf("hasScope(%q, %q, %v) allowed=%v, want %v", tt.method, tt.path, tt.userScopes, allowed, tt.wantAllowed)
			}
			if msg != tt.wantMsg {
				t.Errorf("hasScope(%q, %q, %v) msg=%q, want %q", tt.method, tt.path, tt.userScopes, msg, tt.wantMsg)
			}
		})
	}
}

func TestValidateScopes(t *testing.T) {
	tests := []struct {
		name    string
		scopes  []string
		wantLen int
	}{
		{"all valid", []string{"servers:read", "servers:write"}, 0},
		{"single valid", []string{"servers:exec"}, 0},
		{"empty is valid", []string{}, 0},
		{"nil is valid", nil, 0},
		{"one invalid", []string{"servers:read", "fake:scope"}, 1},
		{"all invalid", []string{"bad", "wrong"}, 2},
		{"unknown resource", []string{"users:read"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invalid := ValidateScopes(tt.scopes)
			if len(invalid) != tt.wantLen {
				t.Errorf("ValidateScopes(%v) returned %d invalid scopes, want %d: %v", tt.scopes, len(invalid), tt.wantLen, invalid)
			}
		})
	}
}
