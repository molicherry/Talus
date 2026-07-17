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
