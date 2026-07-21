package middleware

import (
	"strings"

	"github.com/vpsmanager/backend/internal/pkg/token"
)

// routeScopes maps normalized "METHOD /api/v1/..." patterns to required scopes.
var routeScopes = map[string]string{
	"GET /api/v1/servers":               "servers:read",
	"POST /api/v1/servers":              "servers:write",
	"GET /api/v1/servers/{id}":          "servers:read",
	"PUT /api/v1/servers/{id}":          "servers:write",
	"POST /api/v1/servers/{id}/exec":    "servers:exec",
	"GET /api/v1/servers/{id}/terminal": "servers:terminal",
	"GET /api/v1/servers/{id}/metrics":  "metrics:read",
	"GET /api/v1/credentials":           "credentials:read",
	"POST /api/v1/services/{id}/relay":  "services:relay",
}

// jwtOnlyRoutes defines routes where API keys are always rejected.
var jwtOnlyRoutes = map[string]bool{
	"DELETE /api/v1/servers/{id}":       true,
	"POST /api/v1/credentials":          true,
	"PUT /api/v1/credentials/{id}":      true,
	"DELETE /api/v1/credentials/{id}":   true,
	"GET /api/v1/credentials/{id}/reveal": true,
	"GET /api/v1/api-keys":              true,
	"POST /api/v1/api-keys":             true,
	"DELETE /api/v1/api-keys/{id}":      true,
	"GET /api/v1/auth/profile":          true,
	"PUT /api/v1/auth/password":         true,
	"POST /api/v1/services":                    true,
	"PUT /api/v1/services/{id}":                true,
	"DELETE /api/v1/services/{id}":             true,
	"GET /api/v1/services/{id}/credentials":    true,
}

// hasScope checks whether an API key with the given userScopes is permitted
// to access the route identified by method and path.
//
// Returns (true, "") when access is allowed.
// Returns (false, requiredScope) when a specific scope is missing.
// Returns (false, "") when the route is JWT-only and API keys are never allowed.
func hasScope(method, path string, userScopes []string) (bool, string) {
	normalized := normalizePath(method, path)

	if jwtOnlyRoutes[normalized] {
		return false, ""
	}

	required, ok := routeScopes[normalized]
	if !ok {
		return true, ""
	}

	for _, s := range userScopes {
		if s == "*" || s == required {
			return true, ""
		}
	}
	return false, required
}

// normalizePath converts a real request path into a pattern string by
// replacing numeric segments with "{id}".
func normalizePath(method, path string) string {
	raw := strings.Split(strings.TrimRight(path, "/"), "/")
	segments := make([]string, 0, len(raw))
	for _, seg := range raw {
		if seg != "" {
			segments = append(segments, seg)
		}
	}
	for i, seg := range segments {
		if isNumeric(seg) {
			segments[i] = "{id}"
		}
	}
	return method + " /" + strings.Join(segments, "/")
}

// isNumeric reports whether s consists entirely of decimal digits.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// validScopes is the set of all scope strings that a user may request.
var validScopes = map[string]bool{
	"servers:read":       true,
	"servers:write":      true,
	"servers:exec":       true,
	"servers:terminal":   true,
	"metrics:read":       true,
	"credentials:read":   true,
	"services:relay":     true,
}

// CheckServerAccess returns true if the claims have full access (nil or empty ServerIDs)
// or if the target serverID is present in the claims' ServerIDs list.
func CheckServerAccess(claims *token.Claims, serverID uint) bool {
	if claims == nil || len(claims.ServerIDs) == 0 {
		return true
	}
	for _, id := range claims.ServerIDs {
		if id == serverID {
			return true
		}
	}
	return false
}

// ValidateScopes checks that every scope in scopes is a known valid scope.
// Returns a list of invalid scope strings (empty if all are valid).
func ValidateScopes(scopes []string) []string {
	var invalid []string
	for _, s := range scopes {
		if !validScopes[s] {
			invalid = append(invalid, s)
		}
	}
	return invalid
}
