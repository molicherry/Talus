# Implement: API Key Scope-Based Permission Control

## Execution Order

### 1. Model + Migration — `backend/internal/model/apikey.go`

- [ ] Add `Scopes []string` field: `gorm:"type:jsonb;serializer:json;default:'[\"servers:read\",\"servers:write\",\"servers:exec\",\"servers:terminal\",\"metrics:read\",\"credentials:read\"]'" json:"scopes"`
- [ ] Add `UserID uint` field: `gorm:"not null;index" json:"-"`
- [ ] Create migration file: `backend/migrations/001_add_api_key_scopes.sql` with same default

**Validation**: Run auto-migrate; verify column exists with `\d api_keys` in psql.

### 2. Repository — `backend/internal/repository/apikey.go`

- [ ] No changes needed — GORM auto-loads JSONB into `[]string`

**Validation**: `go build ./...` passes.

### 3. Service — `backend/internal/service/apikey.go`

- [ ] Update `Create(ctx, name)` → `Create(ctx, name string, scopes []string)`
- [ ] If `scopes` is nil/empty, default to all scope-gated scopes (not `*`)
- [ ] Validate scopes: reject any unknown/invalid scope string with error
- [ ] Set `k.Scopes = scopes` before calling repo.Create
- [ ] `Validate()` returns `*model.APIKey` — scopes come through automatically

**Validation**: Create key with `["servers:read"]`, verify scopes persisted.

### 4. Auth Middleware Interface — `backend/internal/server/middleware/auth.go`

- [ ] Update `APIKeyValidator` interface: add `scopes []string` to return signature
- [ ] Update `APIKeyValidatorFunc` type signature to match
- [ ] Add `hasScope()` call in the API key branch of `Auth()`
- [ ] On scope failure: return `403 Forbidden` with message `"insufficient scope: requires <scope>"`

**Validation**: `go build ./...` passes. Interface change propagates to main.go.

### 5. Scope Mapping — `backend/internal/server/middleware/scope.go` (NEW)

- [ ] Define `var routeScopes` mapping `"METHOD /path/pattern"` → required scope string
- [ ] Define `var jwtOnlyRoutes` set of `"METHOD /path/pattern"` that reject API keys entirely
- [ ] Define `func hasScope(method, path string, userScopes []string) bool`:
  - First check `jwtOnlyRoutes` → if match, return false (API key rejected regardless of scope)
  - Then check `routeScopes` → compare required scope against userScopes
- [ ] Route matching: strip `/api/v1/` prefix, replace `{id}` segments with wildcard for pattern matching

**Validation**: Table-driven test: `["servers:read"]` passes GET /servers, fails POST /servers, fails on jwtOnlyRoutes.

### 6. Handler — `backend/internal/handler/apikey.go`

- [ ] Update `createAPIKeyRequest`: add `Scopes []string json:"scopes,omitempty"`
- [ ] Pass `req.Scopes` to `h.svc.Create()`
- [ ] Return scopes in create response (already in model, so `result.APIKey.Scopes` is populated)
- [ ] List returns scopes (already in model)

**Validation**: `curl POST /api/v1/api-keys -d '{"name":"test","scopes":["servers:read"]}'` → response includes scopes.

### 7. Wiring — `backend/cmd/server/main.go`

- [ ] Update `apiKeyAuth` validator func: return `k.Scopes` as 5th return value
- [ ] Signature: `func(ctx, rawKey) (uint, string, string, []string, error)`

**Validation**: `go build ./cmd/server/...` passes.

### 8. JWT-Only Endpoint Restriction — `backend/internal/server/middleware/scope.go`

- [ ] Populate `jwtOnlyRoutes` with all credential-mutation, api-key management, and auth endpoints
- [ ] `hasScope()` returns false for any match in `jwtOnlyRoutes` — API key gets 403

**Validation**: API key blocked on `GET /auth/profile`, `POST /credentials`, `GET /api-keys`, `DELETE /api-keys/{id}`.

### 9. Integration Test

- [ ] Start server, create admin via `/api/v1/auth/login`
- [ ] Create scoped API key: `["servers:read", "servers:write", "servers:exec"]`
- [ ] GET /servers → 200 ✅
- [ ] POST /servers → 200 ✅
- [ ] DELETE /servers/{id} → 403 ✅ (JWT-only)
- [ ] GET /credentials → 200 ✅ (metadata only)
- [ ] POST /credentials → 403 ✅ (JWT-only)
- [ ] GET /api-keys → 403 ✅ (JWT-only)
- [ ] GET /auth/profile → 403 ✅ (JWT-only)
- [ ] JWT token: all endpoints work ✅

## Rollback

```bash
# Revert migration
psql -c "ALTER TABLE api_keys DROP COLUMN IF EXISTS scopes;"
# Revert code
git revert <commit>
```
