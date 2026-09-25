package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/vpsmanager/backend/internal/pkg/token"
)

// terminalPathPattern matches GET /api/v1/servers/{id}/terminal, the only route
// allowed to complete authentication after the WebSocket handshake.
var terminalPathPattern = regexp.MustCompile(`^/api/v1/servers/\d+/terminal/?$`)

// isTerminalWebSocketHandshake reports whether this request is a WebSocket
// handshake for the terminal route. The middleware only skips auth for that
// exact route, so an unauthenticated client cannot reach other endpoints by
// setting `Upgrade: websocket`.
func isTerminalWebSocketHandshake(r *http.Request) bool {
	return r.Method == http.MethodGet &&
		r.Header.Get("Upgrade") == "websocket" &&
		terminalPathPattern.MatchString(r.URL.Path)
}

// contextUserKey is a private context key for storing authenticated claims.
type contextUserKey string

const userKey contextUserKey = "user"

// APIKeyValidator validates raw API key strings.
type APIKeyValidator interface {
	Validate(ctx context.Context, rawKey string) (userID uint, username string, role string, scopes []string, serverIDs []uint, err error)
}

// APIKeyValidatorFunc wraps a function as an APIKeyValidator.
type APIKeyValidatorFunc func(ctx context.Context, rawKey string) (uint, string, string, []string, []uint, error)

func (f APIKeyValidatorFunc) Validate(ctx context.Context, rawKey string) (uint, string, string, []string, []uint, error) {
	return f(ctx, rawKey)
}

// Auth returns middleware that validates a JWT Bearer token or X-API-Key header.
func Auth(jwtSvc *token.JWTService, keyValidator APIKeyValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Try API key first
			if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
				uid, username, role, scopes, serverIDs, err := keyValidator.Validate(r.Context(), apiKey)
				if err != nil {
					writeAuthError(w, http.StatusUnauthorized, "invalid api key")
					return
				}

				allowed, required := hasScope(r.Method, r.URL.Path, scopes)
				if !allowed {
					msg := "api key not permitted on this endpoint"
					if required != "" {
						msg = "insufficient scope: requires " + required
					}
					writeAuthError(w, http.StatusForbidden, msg)
					return
				}

				claims := &token.Claims{
					UserID:    uid,
					Username:  username,
					Role:      role,
					ServerIDs: serverIDs,
				}
				ctx := context.WithValue(r.Context(), userKey, claims)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// WebSocket handshakes: browsers cannot set custom headers, so the
			// terminal endpoint authenticates via the first WebSocket message
			// (see TerminalHandler). Restrict this pass-through to that one route:
			// a bare `Upgrade: websocket` header must never disable auth elsewhere.
			if isTerminalWebSocketHandshake(r) {
				next.ServeHTTP(w, r)
				return
			}

			tokenStr, ok := extractBearerToken(r)
			if !ok {
				writeAuthError(w, http.StatusUnauthorized, "missing or invalid authorization header")
				return
			}

			claims, err := jwtSvc.ValidateToken(tokenStr)
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), userKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetUserClaims extracts the authenticated user's JWT claims from the context.
func GetUserClaims(ctx context.Context) *token.Claims {
	if claims, ok := ctx.Value(userKey).(*token.Claims); ok {
		return claims
	}
	return nil
}

// extractBearerToken pulls the raw token from an Authorization: Bearer <value> header.
func extractBearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", false
	}
	return token, true
}

// Reason identifiers, mirrored from internal/server/errors.go. The middleware
// cannot import that package (the server imports the middleware), so the strings
// are duplicated here; TestAuthMiddlewareReasons drives this middleware for real
// and asserts the responses carry the same values the server package declares.
const (
	reasonUnauthorized = "unauthorized"
	reasonForbidden    = "forbidden"
)

// writeAuthError sends the same error envelope the rest of the API uses, with a
// stable reason. Without it a 401 reached the UI as a bare message and every
// translation fell back to a generic "request failed with status 401".
func writeAuthError(w http.ResponseWriter, statusCode int, message string) {
	reason := reasonUnauthorized
	if statusCode == http.StatusForbidden {
		reason = reasonForbidden
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	body, _ := json.Marshal(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    statusCode,
			"reason":  reason,
			"message": message,
		},
	})
	_, _ = w.Write(body)
}
