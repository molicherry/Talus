# Design: API Key Scope-Based Permission Control

## Overview

Add scope-based access control to API keys without changing the JWT (admin) path. The auth middleware gains a scope-checking layer that runs only for API key requests. Endpoints are annotated with required scopes via a route-scope mapping.

## Architecture

```
Request → Auth Middleware
              ├── X-API-Key header? → Validate key → Check scope against route → Grant/Deny
              └── Bearer token?      → Validate JWT  → Full access (unchanged)
```

## Data Model

### APIKey (GORM model)

```go
type APIKey struct {
    ID           uint      `gorm:"primaryKey" json:"id"`
    UserID       uint      `gorm:"not null;index" json:"-"`
    Name         string    `gorm:"size:128" json:"name"`
    KeyHash      string    `gorm:"uniqueIndex;not null" json:"-"`
    KeyPrefix    string    `gorm:"size:12;not null" json:"key_prefix"`
    Scopes       []string  `gorm:"type:jsonb;serializer:json;default:'[\"*\"]'" json:"scopes"`
    LastUsedAt   *time.Time `json:"-"`
    CreatedAt    time.Time  `json:"created_at"`
}
```

## Migration

```sql
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scopes JSONB DEFAULT '["*"]';
```

## Route → Scope Mapping

Defined in `backend/internal/server/middleware/scope.go` as a static map. The middleware checks if the API key's scopes contain the required scope (or wildcard `*`).

### Scope-gated (API key needs matching scope)

| Route Pattern | Method | Required Scope |
|---|---|---|
| `/api/v1/servers` | GET | `servers:read` |
| `/api/v1/servers` | POST | `servers:write` |
| `/api/v1/servers/{id}` | GET | `servers:read` |
| `/api/v1/servers/{id}` | PUT | `servers:write` |
| `/api/v1/servers/{id}/exec` | POST | `servers:exec` |
| `/api/v1/servers/{id}/terminal` | GET | `servers:terminal` |
| `/api/v1/servers/{id}/metrics` | GET | `metrics:read` |
| `/api/v1/credentials` | GET | `credentials:read` |

### JWT-only (API key always rejected with 403)

| Route Pattern | Methods |
|---|---|
| `/api/v1/servers/{id}` | DELETE |
| `/api/v1/credentials` | POST |
| `/api/v1/credentials/{id}` | PUT, DELETE |
| `/api/v1/api-keys` | GET, POST |
| `/api/v1/api-keys/{id}` | DELETE |
| `/api/v1/auth/profile` | GET |
| `/api/v1/auth/password` | PUT |

**Mechanism**: `scope.go` uses a `jwtOnlyRoutes` set. When an API key request matches a JWT-only route, `hasScope()` returns false before checking scopes.

## Middleware Changes (`auth.go`)

```go
func Auth(...) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
                uid, username, role, scopes, err := keyValidator.Validate(r.Context(), apiKey)
                // ... error handling ...

                // Check scope against current route
                if !hasScope(r.Method, r.URL.Path, scopes) {
                    writeAuthError(w, http.StatusForbidden, "insufficient scope")
                    return
                }

                claims := &token.Claims{...}
                ctx := context.WithValue(r.Context(), userKey, claims)
                next.ServeHTTP(w, r.WithContext(ctx))
                return
            }
            // JWT path unchanged
        })
    }
}
```

### APIKeyValidator interface change

```go
type APIKeyValidator interface {
    // Added scopes return value
    Validate(ctx context.Context, rawKey string) (userID uint, username string, role string, scopes []string, err error)
}
```

## Handler Changes (`apikey.go`)

- `CreateAPIKey`: Accept optional `scopes` field in request body. Valid values are only scope-gated scopes; passing JWT-only scopes returns 400. Default to all scope-gated scopes if omitted.
- `ListAPIKeys`: Return `scopes` in each key object (JWT-only endpoint, unchanged).

## Files Changed

| File | Change |
|---|---|
| `backend/internal/model/apikey.go` | Add `Scopes []string` field |
| `backend/internal/server/middleware/auth.go` | Add scope check; update `APIKeyValidator` interface |
| `backend/internal/server/middleware/scope.go` | **New file**: route→scope mapping + `hasScope()` |
| `backend/internal/handler/apikey.go` | Accept/return scopes in create/list |
| `backend/internal/service/apikey.go` | Persist scopes on create; return scopes on validate |
| `backend/internal/repository/apikey.go` | Query returns scopes |
| Migration SQL | Add `scopes` column |

## Error Response

On scope denial, return `403 Forbidden`:
```json
{"error": {"code": 403, "message": "insufficient scope: requires servers:write"}}
```

## Rollback

- Revert migration: `ALTER TABLE api_keys DROP COLUMN scopes;`
- Revert code: scopes default to `["*"]`, so old behavior is restored
