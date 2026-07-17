--- 
name: talus
description: Interact with a running Talus instance via its REST API to manage Linux servers, execute remote commands over SSH, open interactive terminals (WebSocket), query live monitoring metrics, manage SSH credentials, create API keys, register external services with encrypted credentials, and proxy authenticated requests through the relay endpoint. Talus is a self-hosted VPS management platform (Go backend + React frontend + PostgreSQL). Use when user wants to manage servers via Talus API, execute commands on remote hosts, check server metrics, manage SSH credentials programmatically, automate VPS operations through Talus, register proxied services, or relay API calls to external services. Triggers: "Talus API", "manage server via Talus", "execute command on server", "check server metrics Talus", "add SSH credential", "create API key", "relay request", "register service", "Talus 管理", "通过Talus执行命令".
---

# Talus API Usage

Interact with a Talus instance through its REST API. Talus connects to Linux servers over SSH — you manage servers, credentials, commands, terminals, and monitoring from a central hub.

## Quick Connect

```bash
# Base URL for a local Talus instance
TALUS_URL="http://localhost:8080"
```

All API responses follow `{ "data": <payload> }`. Errors follow `{ "error": { "code": <int>, "message": <string> } }`.

## Authentication

Two auth methods with different privilege levels:

| Method | Header | How to obtain | Privilege |
|--------|--------|---------------|-----------|
| JWT (Bearer) | `Authorization: Bearer <token>` | `POST /api/v1/auth/login` | Full access — all endpoints |
| API Key | `X-API-Key: <key>` | `POST /api/v1/api-keys` (JWT only) | Scoped — limited by assigned scopes; JWT-only endpoints return 403 |

**JWT-only endpoints** (API key always rejected): `DELETE /servers/{id}`, all credential mutations (`POST/PUT/DELETE`), all API key management (`GET/POST/DELETE`), service mutations (`POST/PUT/DELETE /api/v1/services`), auth endpoints (`GET/PUT /auth/*`).

### First-Time Setup (no users exist yet)

```bash
# 1. Check if setup is needed
curl -s $TALUS_URL/api/v1/auth/setup
# → {"data":{"needed":true}}

# 2. Create admin account (first login = automatic setup)
curl -s -X POST $TALUS_URL/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"your-password"}'
# → {"data":{"token":"eyJhbGciOi..."}}

# Save token
TOKEN="eyJhbGciOi..."
```

### Regular Login (after setup)

```bash
curl -s -X POST $TALUS_URL/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"your-password"}'
```

## Complete API Reference

All protected endpoints require `Authorization: Bearer <token>` or `X-API-Key: <key>`.

### Auth & Profile — **JWT only**

| Method | Path | Description | Body |
|--------|------|-------------|------|
| `GET` | `/api/v1/auth/setup` | Check if first-time setup needed | — |
| `POST` | `/api/v1/auth/login` | Login (or create first admin) | `{username, password}` |
| `GET` | `/api/v1/auth/profile` | Get current user profile | — |
| `PUT` | `/api/v1/auth/password` | Change password | `{current_password, new_password}` |

### Servers

| Method | Path | Description | Body |
|--------|------|-------------|------|
| `GET` | `/api/v1/servers` | List all servers (with latest_metrics, credential, status) | — |
| `POST` | `/api/v1/servers` | Register a server | `{name, host, port, description?, credential_id?}` |
| `GET` | `/api/v1/servers/{id}` | Get server detail | — |
| `PUT` | `/api/v1/servers/{id}` | Update server | Partial `{name?, host?, port?, description?, credential_id?}` |
| `DELETE` | `/api/v1/servers/{id}` | Remove server — **JWT only** | — |
| `POST` | `/api/v1/servers/{id}/exec` | Execute command over SSH | `{command, timeout?}` |
| `GET` | `/api/v1/servers/{id}/metrics` | Query monitoring metrics | Query: `from`, `to` (ISO 8601), `interval` (1m/5m/15m/1h) |
| `GET` | `/api/v1/servers/{id}/terminal` | **WebSocket** interactive PTY terminal | Query: `?token=<jwt>` |

### SSH Credentials

| Method | Path | Description | Body |
|--------|------|-------------|------|
| `GET` | `/api/v1/credentials` | List credentials (secrets NOT returned) | — |
| `POST` | `/api/v1/credentials` | Create credential — **JWT only** | `{username, auth_type, name?, password? \| private_key?}` |
| `PUT` | `/api/v1/credentials/{id}` | Update credential — **JWT only** | `{username?, password? \| private_key?}` |
| `DELETE` | `/api/v1/credentials/{id}` | Delete credential — **JWT only** | — |

`auth_type` is `"password"` or `"private_key"`. Provide exactly one of `password` or `private_key` matching the type.

### Services

| Method | Path | Description | Body |
|--------|------|-------------|------|
| `GET` | `/api/v1/services` | List all services (credential hints only, no secrets) | Query: `?server_id=<id>` (optional filter) |
| `POST` | `/api/v1/services` | Register a service — **JWT only** | `{name, base_url, credentials, credential_hints?, display_name?, description?, server_id?}` |
| `GET` | `/api/v1/services/{id}` | Get a single service | — |
| `PUT` | `/api/v1/services/{id}` | Update service (full replacement) — **JWT only** | Same body as POST |
| `DELETE` | `/api/v1/services/{id}` | Remove service — **JWT only** | — |
| `POST` | `/api/v1/services/{id}/relay` | Proxy a request through the service | `{method, path, headers?, body?}` |

Service credentials are encrypted with AES-256-GCM and **never returned** in API responses (`credentials` map is masked in all GET responses). The `credential_hints` map provides human-readable labels for each credential key (e.g. `"token": "Portainer API token"`).

On update (PUT), credentials are **fully replaced** — re-enter all key-value pairs (existing values cannot be read back).

Relay supports `{{key}}` placeholder substitution in path, headers, and body — placeholders are replaced with decrypted credential values before the request is proxied.

### API Keys — **JWT only** (API key rejected with 403)

| Method | Path | Description | Body |
|--------|------|-------------|------|
| `GET` | `/api/v1/api-keys` | List API keys (prefixes + scopes) | — |
| `POST` | `/api/v1/api-keys` | Create scoped API key (full key returned ONCE) | `{name, scopes?}` |
| `DELETE` | `/api/v1/api-keys/{id}` | Revoke API key | — |

`scopes` field: optional array of `resource:action` strings. Valid values: `servers:read`, `servers:write`, `servers:exec`, `servers:terminal`, `metrics:read`, `credentials:read`, `services:relay`. Defaults to the first five (`servers:read`, `servers:exec`, `servers:terminal`, `metrics:read`, `credentials:read`) if omitted; `servers:write` and `services:relay` must be explicitly requested (opt-in).

API keys can never access: credential mutation, API key management, auth endpoints, service management (`POST/PUT/DELETE /api/v1/services`), or `DELETE /servers/{id}`.

### Health

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/healthz` | Liveness check → `{"status":"ok"}` |
| `GET` | `/api/v1/` | Version → `{"version":"0.1.0"}` |

## Data Models

### Server (from `GET /api/v1/servers`)
```json
{
  "id": 1, "name": "web-01", "host": "10.0.1.5", "port": 22,
  "description": "Production web server",
  "status": "online",           // "online" | "offline" | "checking" | "unknown"
  "last_seen": "2026-07-10T...",
  "os": "Ubuntu 24.04", "cpu_model": "AMD EPYC",
  "uptime_seconds": 864000,
  "latest_metrics": {           // included in list endpoint
    "cpu_percent": 12.5, "memory_percent": 45.2, "disk_percent": 68.1
  },
  "credential": { "id": 1, "name": "admin-key", "auth_type": "private_key", "username": "root" }
}
```

### SSHCredential
```json
{
  "id": 1, "name": "admin-key", "auth_type": "private_key",
  "username": "root", "key_fingerprint": "SHA256:abc...",
  "created_at": "2026-07-10T..."
}
```
Secrets (password / private_key) are **never returned** in API responses.

### ExecResponse
```json
{ "stdout": "total 24\ndrwxr-xr-x ...", "stderr": "", "exit_code": 0, "duration_ms": 342 }
```

### MetricPoint
```json
{
  "time": "2026-07-10T12:00:00Z",
  "cpu_percent": 12.5, "memory_percent": 45.2, "disk_percent": 68.1,
  "load_1": 0.5, "load_5": 0.8, "load_15": 1.2,
  "swap_percent": 0.0,
  "net_recv_rate": 1024000, "net_sent_rate": 512000,
  "disk_read_rate": 2048000, "disk_write_rate": 1024000
}
```

### APIKey
```json
// List response (prefix + scopes only, no raw key):
{ "id": 1, "name": "ci-pipeline", "key_prefix": "tv1-abc123...", "scopes": ["servers:read","servers:exec"], "created_at": "2026-07-10T..." }
// Create response (full key shown ONCE):
{ "id": 1, "name": "ci-pipeline", "key": "tv1-abc123def456...", "scopes": ["servers:read","servers:exec"], "created_at": "2026-07-10T..." }
```

### Service (from `GET /api/v1/services`)
```json
{
  "id": 1, "name": "grafana", "display_name": "Grafana Dashboard",
  "base_url": "http://localhost:3000",
  "credential_hints": { "token": "API token for Portainer" },
  "description": "Monitoring dashboards",
  "server_id": null,
  "created_at": "2026-07-17T..."
}
```

Credentials (keys/values) and encryption salt are **never returned** in API responses — only `credential_hints` is visible for labeling.

## Common Workflows

### Add a server and run a command

```bash
# 1. Create credential (private key)
curl -s -X POST $TALUS_URL/api/v1/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-key","username":"root","auth_type":"private_key","private_key":"-----BEGIN OPENSSH PRIVATE KEY-----\n...\n-----END OPENSSH PRIVATE KEY-----"}'

# 2. Register server with that credential
curl -s -X POST $TALUS_URL/api/v1/servers \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"prod-db","host":"10.0.1.10","port":22,"credential_id":1}'

# 3. Execute uptime
curl -s -X POST $TALUS_URL/api/v1/servers/1/exec \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"command":"uptime"}'
```

### Query metrics (last hour, 1-minute intervals)

```bash
FROM=$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)
TO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
curl -s "$TALUS_URL/api/v1/servers/1/metrics?from=$FROM&to=$TO&interval=1m" \
  -H "Authorization: Bearer $TOKEN"
```

### WebSocket Terminal (JavaScript)

```js
const ws = new WebSocket(`ws://localhost:8080/api/v1/servers/1/terminal?token=${token}`);
ws.onmessage = (e) => {
  const msg = JSON.parse(e.data);
  // msg.type: "connected" | "output" | "error" | "disconnected"
  if (msg.type === "output") console.log(msg.data);
};
// Send input
ws.send(JSON.stringify({ type: "input", data: "ls -la\n" }));
// Resize terminal
ws.send(JSON.stringify({ type: "resize", cols: 120, rows: 40 }));
```

### Create a scoped API key and use it

```bash
# 1. Create scoped API key (JWT auth required)
KEY_RESP=$(curl -s -X POST $TALUS_URL/api/v1/api-keys \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"monitoring","scopes":["servers:read","metrics:read"]}')
API_KEY=$(echo $KEY_RESP | jq -r '.data.key')

# 2. Use API key — scoped to servers:read + metrics:read
curl -s $TALUS_URL/api/v1/servers \
  -H "X-API-Key: $API_KEY"

# 3. Attempting POST /servers with this key → 403 (missing servers:write)
curl -s -X POST $TALUS_URL/api/v1/servers \
  -H "X-API-Key: $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"test","host":"10.0.0.1","port":22}'
# → {"error":{"code":403,"message":"insufficient scope: requires servers:write"}}
```

### Register a service and relay a request

```bash
# 1. Register a Grafana service with an API token credential
curl -s -X POST $TALUS_URL/api/v1/services \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "grafana",
    "display_name": "Grafana Dashboard",
    "base_url": "http://localhost:3000",
    "credentials": {"token": "glsa_abc123..."},
    "credential_hints": {"token": "Service account token"},
    "description": "Monitoring dashboards"
  }'

# 2. Relay a GET request through the service (token injected from credentials)
curl -s -X POST $TALUS_URL/api/v1/services/1/relay \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"method": "GET", "path": "/api/dashboards/home"}'

# 3. Relay with placeholder substitution — {{token}} is replaced with the credential value
curl -s -X POST $TALUS_URL/api/v1/services/1/relay \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "method": "GET",
    "path": "/api/search?query=health",
    "headers": {"Authorization": "Bearer {{token}}"}
  }'

# 4. Update service credentials (full replacement — re-enter all keys)
curl -s -X PUT $TALUS_URL/api/v1/services/1 \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "grafana",
    "base_url": "http://localhost:3000",
    "credentials": {"token": "glsa_newtoken456..."},
    "credential_hints": {"token": "Rotated service account token"}
  }'

# 5. Delete a service
curl -s -X DELETE $TALUS_URL/api/v1/services/1 \
  -H "Authorization: Bearer $TOKEN"
```

## Security Notes

- SSH credentials (passwords, private keys) are encrypted with AES-256-GCM at rest
- Service credentials are encrypted with AES-256-GCM at rest, same encryption scheme
- Secrets are **never** returned in API responses (credential GET lists show only metadata; service GET shows only credential_hints)
- First login to an empty instance creates the admin account automatically
- API keys are scoped — assign only the minimum permissions needed (see scope list above)
- API keys are shown in full only once at creation — save immediately
- JWT-only endpoints (credential mutations, API key management, service management, server deletion, auth) reject API keys with 403 regardless of scope
- SSH host keys use TOFU (Trust On First Use) verification
