# API Key scope-based permission control

## Goal

Restrict API Key permissions by resource-level scopes. Currently JWT and API Key share identical full access — a leaked API key can delete servers, change passwords, and manage credentials. Scopes allow API keys to be granted only the minimum permissions needed.

## Requirements

1. **Scope Model** — Resource-level scopes in `resource:action` format. Each API key carries a list of scopes. Wildcard `*` grants all permissions (backward compatible).

2. **Scope Definitions** — the following scopes map to specific API endpoints:

   | Scope | Endpoints Covered |
   |---|---|
   | `servers:read` | `GET /api/v1/servers`, `GET /api/v1/servers/{id}` |
   | `servers:write` | `POST /api/v1/servers`, `PUT /api/v1/servers/{id}` |
   | `servers:exec` | `POST /api/v1/servers/{id}/exec` |
   | `servers:terminal` | `GET /api/v1/servers/{id}/terminal` (WebSocket) |
   | `metrics:read` | `GET /api/v1/servers/{id}/metrics` |
   | `credentials:read` | `GET /api/v1/credentials` |

   **JWT-only endpoints** (API key rejected regardless of scope):
   - `DELETE /api/v1/servers/{id}` — server deletion
   - `POST/PUT/DELETE /api/v1/credentials` — credential mutation
   - `GET/POST/DELETE /api/v1/api-keys` — API key management
   - `GET /api/v1/auth/profile`, `PUT /api/v1/auth/password` — user account

3. **JWT-Only Restrictions** — The following endpoints accept JWT only. API keys are rejected with 403 regardless of scope:
   - All credential mutation: `POST/PUT/DELETE /api/v1/credentials` and `/{id}`
   - All API key management: `GET/POST /api/v1/api-keys`, `DELETE /api/v1/api-keys/{id}`
   - User account: `GET /api/v1/auth/profile`, `PUT /api/v1/auth/password`

4. **Backward Compatibility** — Existing API keys without a `scopes` column default to `["*"]` (full access). The migration adds a nullable column; the application treats null as wildcard.

5. **Scope Storage** — Scopes stored as JSON array in a `scopes` column (PostgreSQL JSONB, Go `[]string` with GORM `serializer:json`).

6. **Create / List Update** — `POST /api/v1/api-keys` accepts optional `scopes` field. `GET /api/v1/api-keys` returns `scopes` in each key.

## Acceptance Criteria

- [ ] API key with `servers:read` can GET servers but fails on POST/PUT/DELETE with 403
- [ ] API key with `servers:read, servers:exec` can list servers and execute commands, but cannot create/update/delete servers
- [ ] API key rejected (403) on ALL of: `DELETE /servers/{id}`, `POST/PUT/DELETE /credentials`, `GET/POST /api-keys`, `DELETE /api-keys/{id}`, `GET /auth/profile`, `PUT /auth/password`
- [ ] JWT (admin login) retains unrestricted access to all endpoints including JWT-only ones
- [ ] `POST /api/v1/api-keys` with `{"name":"ci","scopes":["servers:read","servers:exec"]}` creates a scoped key
- [ ] `GET /api/v1/api-keys` response includes `scopes` field (JWT only)
- [ ] Existing database rows with null `scopes` are treated as `["servers:read","servers:write","servers:exec","servers:terminal","metrics:read","credentials:read"]`
- [ ] Migration runs without data loss
