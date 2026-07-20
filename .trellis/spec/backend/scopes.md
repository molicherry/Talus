# Scope Registration

> How API key scopes are defined and enforced.

---

## Overview

API keys carry a JSONB `scopes` array. Each scope is a `resource:action` string (e.g. `servers:read`, `services:relay`). Three maps in `internal/server/middleware/scope.go` control what each scope can access.

---

## The Three Maps

| Map | Location | Purpose |
|-----|----------|---------|
| `validScopes` | `scope.go` | Set of all known scope strings. Unknown scopes are rejected at API key creation. |
| `routeScopes` | `scope.go` | Maps `METHOD /path/{id}/...` to required scope. Missing scope → 403. |
| `jwtOnlyRoutes` | `scope.go` | Routes where API keys are **always** rejected, regardless of scope. |

```go
// internal/server/middleware/scope.go

var validScopes = map[string]bool{
    "servers:read":       true,
    "servers:write":      true,
    "servers:exec":       true,
    "servers:terminal":   true,
    "metrics:read":       true,
    "credentials:read":   true,
    "services:relay":     true,
}

var routeScopes = map[string]string{
    "GET /api/v1/servers":               "servers:read",
    "POST /api/v1/servers":              "servers:write",
    // ...
    "POST /api/v1/services/{id}/relay":  "services:relay",
}

var jwtOnlyRoutes = map[string]bool{
    "DELETE /api/v1/servers/{id}":       true,
    "POST /api/v1/services":             true,
    "PUT /api/v1/services/{id}":         true,
    "DELETE /api/v1/services/{id}":      true,
    // ...
}
```

> Service management (create/update/delete) is JWT-only; `GET /api/v1/services` and `GET /api/v1/services/{id}` are intentionally registered in neither map, so any authenticated principal (JWT or any API key) can read them — responses never include credential material (`json:"-"`). Only `services:relay` is a scope-gated service route.

---

## How to Add a New Scope

When adding a new scope-gated endpoint, update three places:

### Step 1: `validScopes` — declare the scope

```go
var validScopes = map[string]bool{
    // existing...
    "widgets:read": true,   // NEW
    "widgets:write": true,  // NEW
}
```

This makes the scope valid at API key creation time.

### Step 2: `routeScopes` — map route to scope

```go
var routeScopes = map[string]string{
    // existing...
    "GET /api/v1/widgets":       "widgets:read",
    "POST /api/v1/widgets":      "widgets:write",
    "GET /api/v1/widgets/{id}":  "widgets:read",
}
```

The `{id}` placeholder matches numeric segments via `normalizePath()`.

### Step 3: `jwtOnlyRoutes` (optional) — block API keys entirely

If the endpoint should NEVER be accessible via API key (credential creation, deletion, user management), add it to `jwtOnlyRoutes`:

```go
var jwtOnlyRoutes = map[string]bool{
    "DELETE /api/v1/widgets/{id}": true,
}
```

Routes in `jwtOnlyRoutes` bypass the scope check entirely. API key requests receive 403 regardless of scopes.

---

## `AllScopeGatedScopes` — default scopes for new API keys

`internal/model/apikey.go` defines the default scope set assigned to newly created API keys:

```go
var AllScopeGatedScopes = []string{
    "servers:read",
    "servers:write",
    "servers:exec",
    "servers:terminal",
    "metrics:read",
    "credentials:read",
    // services:relay is NOT included here — relay access must be opt-in.
}
```

### When to Exclude a Scope from Defaults

Exclude a scope from `AllScopeGatedScopes` when:

- The scope grants **proxy/forwarding access** to external systems (e.g. `services:relay`).
- The scope is **dangerous by default** and should be granted explicitly.
- The scope is **new** and existing API keys shouldn't auto-acquire it.

---

## Route Path Normalization

`normalizePath()` converts real request paths like `/api/v1/servers/42/exec` into pattern strings like `GET /api/v1/servers/{id}/exec`. It strips trailing slashes and replaces numeric-only segments with `{id}`.

This means route patterns use `{id}` for ANY numeric segment, not just the server/credential ID:

```go
// Both of these map correctly:
//   GET /api/v1/servers/5/terminal    → "GET /api/v1/servers/{id}/terminal"
//   POST /api/v1/services/3/relay      → "POST /api/v1/services/{id}/relay"
```

---

## Authorization Flow

```
Request arrives
  ↓
Auth middleware extracts JWT or API key
  ↓
jwtOnlyRoutes check → if match and API key → 403 "api key not permitted"
  ↓
routeScopes check → if route has required scope:
  ├─ User scope list contains required scope → allow
  ├─ User scope list contains "*" (super-scope) → allow
  └─ Neither → 403 "insufficient scope: requires <name>"
  ↓
Handler executes
```

---

## Common Mistakes

- ❌ Adding a scope to `validScopes` but forgetting `routeScopes` → endpoint is unprotected.
- ❌ Adding a route to `routeScopes` but forgetting `validScopes` → scope can't be selected at API key creation.
- ❌ Adding `services:relay` to `AllScopeGatedScopes` → every existing API key silently gains proxy access.
- ❌ Using a real numeric value in `routeScopes` instead of `{id}` → normalization won't match.
- ❌ Forgetting to add a DELETE endpoint to `jwtOnlyRoutes` → API keys can delete resources.

---

## Server-Level Access Control (Two-Dimensional Scoping)

> **Added**: 2026-07. API keys now have a second dimension: `ServerIDs []uint` controls **which servers** the key can access, independent of scopes which control **what actions**.

### Data Model

```go
// model/apikey.go
type APIKey struct {
    Scopes    []string `gorm:"type:jsonb;serializer:json" json:"scopes"`
    ServerIDs []uint   `gorm:"type:jsonb;serializer:json" json:"server_ids,omitempty"`
}

// pkg/token/jwt.go
type Claims struct {
    ServerIDs []uint `json:"server_ids,omitempty"`  // nil/empty = full access
}
```

- `ServerIDs = nil` or `[]` → full access (JWT users, unrestricted keys)
- `ServerIDs = [1, 3]` → restricted to those server IDs
- Scopes × ServerIDs intersect: both dimensions must grant access

### CheckServerAccess Helper

```go
// middleware/scope.go
func CheckServerAccess(claims *token.Claims, serverID uint) bool {
    if claims == nil || len(claims.ServerIDs) == 0 {
        return true  // nil/empty = full access (JWT user or unrestricted key)
    }
    for _, id := range claims.ServerIDs {
        if id == serverID {
            return true
        }
    }
    return false
}
```

### Enforcement Patterns

**Pattern 1: List endpoints** — filter results by ServerIDs

```go
// handler/server.go — List()
func (h *ServerHandler) List(w http.ResponseWriter, r *http.Request) {
    claims := mw.GetUserClaims(r.Context())
    var servers []model.Server
    var err error
    if claims != nil && len(claims.ServerIDs) > 0 {
        servers, err = h.svc.ListFiltered(r.Context(), claims.ServerIDs)
    } else {
        servers, err = h.svc.List(r.Context())
    }
    // ...
}
```

**Pattern 2: Single-server endpoints** — 403 on denied server

```go
// handler/server.go — Get(), Update(); handler/exec.go — Execute(); handler/metrics.go — Get()
id, _ := parseIDParam(r)
claims := mw.GetUserClaims(r.Context())
if !mw.CheckServerAccess(claims, id) {
    server.WriteError(w, r, server.NewAppError(http.StatusForbidden,
        "access denied: api key does not have access to this server"))
    return
}
```

**Pattern 3: Service relay** — check Service.ServerID against Claims

```go
// handler/service.go — Relay()
claims := mw.GetUserClaims(r.Context())
svc, getErr := h.svc.Get(r.Context(), uint(id))
if getErr == nil && svc.ServerID != nil {
    if !mw.CheckServerAccess(claims, *svc.ServerID) {
        server.WriteError(w, r, server.NewAppError(http.StatusForbidden,
            "access denied: api key does not have access to the server this service is bound to"))
        return
    }
}
```

### Server ID Validation on API Key Creation

```go
// service/apikey.go — Create()
if len(serverIDs) > 0 {
    existing, err := s.serverRepo.FindByIDs(ctx, serverIDs)
    if len(existing) != len(serverIDs) {
        return nil, server.NewAppError(http.StatusBadRequest,
            fmt.Sprintf("invalid server_ids: servers not found: %v", missing))
    }
}
```

### Auth Middleware — ServerIDs Propagation

```go
// middleware/auth.go — APIKeyValidator interface
type APIKeyValidator interface {
    Validate(ctx context.Context, rawKey string) (userID uint, username string, role string, scopes []string, serverIDs []uint, err error)
}

// main.go — adapter
apiKeyAuth := mw.APIKeyValidatorFunc(func(ctx context.Context, rawKey string) (uint, string, string, []string, []uint, error) {
    k, err := apiKeySvc.Validate(ctx, rawKey)
    return k.ID, k.Name, "admin", k.Scopes, k.ServerIDs, nil
})
```

### Terminal WebSocket — Auth Middleware Integration

The terminal WebSocket endpoint was moved **inside** the auth middleware group. The handler replaced query-param token validation with `mw.GetUserClaims(r.Context())`.

### Common Mistakes — Server-Level

- ❌ Forgetting to add `CheckServerAccess` to a new single-server endpoint → unauthorized server access.
- ❌ Using `List()` instead of `ListFiltered()` for server-scoped keys → leaking servers.
- ❌ Not checking `claims == nil` before accessing `claims.ServerIDs` → nil pointer panic.
- ❌ Not validating server IDs on API key creation → keys with non-existent server IDs stored.
- ❌ Adding `server_ids` to request body but not passing to `service.Create()` → silently ignored.
