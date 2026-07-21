---
name: talus
description: Interact with a running Talus instance via its REST API to manage Linux servers, execute remote commands over SSH, open interactive terminals (WebSocket), query live monitoring metrics, manage SSH credentials, create scoped API keys, register external services with encrypted credentials, and proxy authenticated requests through the relay endpoint. Use when user wants to manage servers, execute commands, check metrics, manage credentials, create API keys, relay requests, or reveal stored secrets.
triggers: "Talus", "manage server", "execute command on server", "check server metrics", "add SSH credential", "create API key", "relay request", "register service", "reveal credential", "copy API key", "Talus 管理", "通过Talus执行命令".
---

# Talus Skill — Agent Operation Guide

You are managing a live Talus instance. Talus is a self-hosted VPS management platform: Go backend, React frontend, PostgreSQL. Servers connect over SSH — no agent installed on targets.

Full endpoint specs and data models: [REFERENCE.md](REFERENCE.md).

## Quick Start

```bash
TALUS_URL="${TALUS_URL:-http://localhost:8080}"
```

Response envelope: `{"data": <payload>}`. Errors: `{"error": {"code": <int>, "message": <string>}}`.

### Auth

Two methods, different privilege levels:

| Auth | Header | How to get | Access |
|------|--------|-----------|--------|
| JWT | `Authorization: Bearer <token>` | `POST /api/v1/auth/login` | Full access — all endpoints |
| API Key | `X-API-Key: <key>` | `POST /api/v1/api-keys` (JWT only) | Scoped: limited by `scopes` + `server_ids` |

**Rule**: Always prefer JWT for admin operations. Use API keys for read-only or limited automation tasks. If an operation needs credential mutation, API key management, or server deletion — it's JWT-only regardless of scopes.

### First-time setup

```bash
# Check
curl -s $TALUS_URL/api/v1/auth/setup  # → {"data":{"needed":true}}

# First login creates admin
TOKEN=$(curl -s -X POST $TALUS_URL/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"your-password"}' | jq -r '.data.token')
```

## Decision Trees

### "I need to run a command on a server"

```
1. Do you already know the server ID?
   ├─ Yes → POST /api/v1/servers/{id}/exec
   └─ No  → GET /api/v1/servers to list and find it
2. Does the user need the output parsed?
   ├─ Yes → Parse stdout/stderr from response {stdout, stderr, exit_code}
   └─ No  → Return raw result
3. Command timed out? → Default 30s, max 300s. Pass `"timeout": 60` for long commands.
```

### "I need to create an API key for someone"

```
1. What should the key be able to do?
   ├─ Read-only monitoring → scopes: ["servers:read", "metrics:read"]
   ├─ Execute commands    → add "servers:exec"
   ├─ Terminal access     → add "servers:terminal"
   ├─ Relay services      → add "services:relay" (opt-in — NOT in defaults)
   └─ Full read access    → ["servers:read", "servers:exec", "servers:terminal", "metrics:read", "credentials:read"]
2. Which servers?
   ├─ All servers  → omit server_ids (default: unrestricted)
   └─ Specific     → pass server_ids: [1, 3, 5]
3. Create it (JWT required):
   POST /api/v1/api-keys -d '{"name":"my-key","scopes":[...],"server_ids":[...]}'
4. Response includes the raw key ONCE. Save it immediately.
5. If the user needs the key again later:
   ├─ Same session (no page refresh) → frontend copy button still works
   └─ Later or different session → GET /api/v1/api-keys/{id}/reveal (JWT, rate-limited: 5/min)
```

### "I need to see a credential password"

```
1. Are you on the credential list page?
   ├─ List only shows name, auth type, username, fingerprint — NOT values
   └─ Use GET /api/v1/credentials/{id}/reveal (JWT only, rate-limited: 5/min)
2. Reveal returns {password: "..."} or {private_key: "..."} depending on auth type
3. The value was encrypted with AES-256-GCM — it's decrypted server-side
4. This operation is AUDITED: `slog.Info("audit: credential revealed", user_id, credential_id, ip)`
```

### "I need to see service credentials"

```
1. Service list shows only credential_hints (descriptions) — NOT values
2. GET /api/v1/services/{id}/credentials (JWT only, rate-limited)
3. Returns {key1: "value1", key2: "value2"}
4. Also audited: logs user_id + service_id + ip
```

### "Something is returning 403"

```
Check in order:
1. Are you using an API key on a JWT-only endpoint?
   JWT-only: DELETE servers, all credential mutations, all API key management,
   service CRUD, /reveal endpoints, /credentials endpoints, auth endpoints.
   → Solution: switch to JWT Bearer token
2. Does the API key have the right scope?
   server:read = GET /servers, GET /servers/{id}
   server:write = POST /servers, PUT /servers/{id}
   server:exec = POST /servers/{id}/exec
   server:terminal = GET /servers/{id}/terminal
   metrics:read = GET /servers/{id}/metrics
   credentials:read = GET /credentials
   services:relay = POST /services/{id}/relay (NOT in default scopes!)
   → Solution: re-create key with correct scopes
3. Is the server in the key's server_ids?
   server_ids=[] → all servers
   server_ids=[1,3] → only servers 1 and 3
   → Solution: re-create key with correct server_ids
```

### "I'm getting 429 Too Many Requests"

```
Reveal endpoints are rate-limited: 5 requests per minute per user.
   ├─ Wait 60 seconds and retry
   ├─ If batching multiple reveals, space them 12+ seconds apart
   └─ Rate limit is per-user, not per-endpoint — all 3 reveal endpoints share the counter
```

## Multi-Step Workflows

### Full server setup

```
1. Create credential:
   POST /api/v1/credentials -d '{"name":"...","username":"root","auth_type":"password","password":"..."}'
   (or auth_type:"private_key" with "private_key":"-----BEGIN...")
2. Register server with credential:
   POST /api/v1/servers -d '{"name":"prod-db","host":"10.0.1.10","port":22,"credential_id":<id>}'
3. Verify connectivity:
   POST /api/v1/servers/{id}/exec -d '{"command":"whoami"}'
   → Expect stdout: "root", exit_code: 0
```

### Register a proxied service

```
1. Register service with credentials:
   POST /api/v1/services -d '{"name":"grafana","base_url":"http://localhost:3000","credentials":{"token":"glsa_..."},"credential_hints":{"token":"API token"}}'
2. Relay a request (credentials injected automatically):
   POST /api/v1/services/{id}/relay -d '{"method":"GET","path":"/api/dashboards/home"}'
3. Use {{key}} placeholders for credential substitution in headers:
   {"method":"GET","path":"/api/search","headers":{"Authorization":"Bearer {{token}}"}}
```

## Critical Rules

1. **Credentials NEVER appear in list responses** — `EncryptedPassword`, `EncryptedPrivateKey`, `EncryptedCredentials`, and `EncryptedRawKey` all have `json:"-"`. Use reveal endpoints to access them.

2. **API key raw value is shown ONCE** — at creation. The `/reveal` endpoint lets you retrieve it later, but it's rate-limited and audited. Also: existing keys created before v0.6.14 don't have an encrypted raw key stored — reveal will return 404.

3. **scopes and server_ids are orthogonal** — a key needs BOTH the right scope AND the right server access. server_ids=[] (or omitted) means all servers.

4. **services:relay is NOT in default scopes** — must be explicitly requested when creating a key. This prevents accidental proxy access.

5. **All credential mutations reject API keys** — always use JWT for creating, updating, or deleting credentials, services, and API keys.

6. **Terminal WebSocket auth** — use `Authorization: Bearer <token>` header during WebSocket upgrade. API key auth also works via `X-API-Key` header.