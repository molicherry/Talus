package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/vpsmanager/backend/internal/pkg/token"
)

// contextUserKey is a private context key for storing authenticated claims.
type contextUserKey string

const userKey contextUserKey = "user"

// APIKeyValidator validates raw API key strings.
type APIKeyValidator interface {
	Validate(ctx context.Context, rawKey string) (userID uint, username string, role string, scopes []string, err error)
}

// APIKeyValidatorFunc wraps a function as an APIKeyValidator.
type APIKeyValidatorFunc func(ctx context.Context, rawKey string) (uint, string, string, []string, error)

func (f APIKeyValidatorFunc) Validate(ctx context.Context, rawKey string) (uint, string, string, []string, error) {
	return f(ctx, rawKey)
}

// Auth returns middleware that validates a JWT Bearer token or X-API-Key header.
func Auth(jwtSvc *token.JWTService, keyValidator APIKeyValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Try API key first
			if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
				uid, username, role, scopes, err := keyValidator.Validate(r.Context(), apiKey)
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
					UserID:   uid,
					Username: username,
					Role:     role,
				}
				ctx := context.WithValue(r.Context(), userKey, claims)
				next.ServeHTTP(w, r.WithContext(ctx))
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

// writeAuthError sends a structured JSON error response.
func writeAuthError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	body, _ := json.Marshal(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    statusCode,
			"message": message,
		},
	})
	w.Write(body)
}
