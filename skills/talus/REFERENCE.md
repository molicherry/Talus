# Talus API Reference

Complete endpoint tables and data models. See [SKILL.md](SKILL.md) for authentication setup and usage workflows.

All API responses follow `{ "data": <payload> }`. Errors follow `{ "error": { "code": <int>, "message": <string>, "request_id": <string> } }`.

## Endpoints

All protected endpoints require `Authorization: Bearer <token>` or `X-API-Key: <key>`.

### Auth & Profile — JWT only

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
| `POST` | `/api/v1/servers` | Register a server | `{name, host, port, description?, notes?, credential_id?}` |
| `GET` | `/api/v1/servers/{id}` | Get server detail | — |
| `PUT` | `/api/v1/servers/{id}` | Update server | Partial `{name?, host?, port?, description?, notes?, credential_id?}` |
| `DELETE` | `/api/v1/servers/{id}` | Remove server — **JWT only** | — |
| `POST` | `/api/v1/servers/{id}/exec` | Execute command over SSH | `{command, timeout?}` |
| `GET` | `/api/v1/servers/{id}/metrics` | Query monitoring metrics | Query: `from`, `to` (ISO 8601), `interval` (1m/5m/15m/1h) |
| `GET` | `/api/v1/servers/{id}/terminal` | **WebSocket** interactive PTY terminal | `Authorization: Bearer` header |

> **Services on this server**: Use `GET /api/v1/services?server_id={id}` to list services bound to a server.

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
| `GET` | `/api/v1/services/{id}/credentials` | Get decrypted service credentials — **JWT only** | — |

**Server binding** (`server_id`): A service can optionally be bound to a server. When bound, relay requests are gated by the API key's server access list. An unbound service (`server_id: null`) is accessible to all API keys. Bind when you want per-server isolation (e.g. bind Portainer to `web-01`).

Service credentials are encrypted with AES-256-GCM and **never returned** in API responses. The `credential_hints` map provides labels (e.g. `"token": "Portainer API token"`). On update (PUT), credentials are **fully replaced**. Relay supports `{{key}}` placeholder substitution.

### API Keys — JWT only

| Method | Path | Description | Body |
|--------|------|-------------|------|
| `GET` | `/api/v1/api-keys` | List API keys (prefixes + scopes) | — |
| `POST` | `/api/v1/api-keys` | Create scoped API key (full key returned ONCE) | `{name, scopes?, server_ids?}` |
| `DELETE` | `/api/v1/api-keys/{id}` | Revoke API key | — |

**Scopes** (optional, `resource:action`): `servers:read`, `servers:write`, `servers:exec`, `servers:terminal`, `metrics:read`, `credentials:read`, `services:relay`. Defaults to `servers:read,exec,terminal` + `metrics:read` + `credentials:read`. `servers:write` and `services:relay` are opt-in.

**Server IDs** (`server_ids`): Optional array of server IDs. When set, the key is restricted to those servers — all `{id}` endpoints are gated. Omit for full access.

API keys can never access: credential mutation, API key management, auth, service management (`POST/PUT/DELETE` + `GET /{id}/credentials`), or `DELETE /servers/{id}`.

### Health

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/healthz` | Liveness check → `{"status":"ok"}` |
| `GET` | `/api/v1/` | Version → `{"version":"0.1.0"}` |
| `GET` | `/api/v1/version` | Version (alias) |

## Data Models

### Server (from `GET /api/v1/servers`)
```json
{
  "id": 1, "name": "web-01", "host": "10.0.1.5", "port": 22,
  "description": "Production web server", "notes": null,
  "status": "online",           // "online" | "offline" | "checking" | "unknown"
  "last_seen": "2026-07-10T...",
  "os": "Ubuntu 24.04", "cpu_model": "AMD EPYC",
  "uptime_seconds": 864000,
  "latest_metrics": {
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
Secrets (password / private_key) are **never returned**.

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
{ "id": 1, "name": "ci-pipeline", "key_prefix": "a1b2c3d4", "scopes": ["servers:read","servers:exec"], "server_ids": [1, 2], "created_at": "2026-07-10T..." }
// Create response (full key shown ONCE, in {"data":{"key":"...","api_key":{...}}}):
{ "key": "a1b2c3d4e5f6...", "api_key": { "id": 1, "name": "ci-pipeline", "key_prefix": "a1b2c3d4", "scopes": ["servers:read","servers:exec"], "server_ids": [1, 2], "created_at": "2026-07-10T..." } }
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
Credentials and encryption salt are **never returned** — only `credential_hints` is visible.

## Security

- SSH credentials and service credentials encrypted with AES-256-GCM at rest
- Secrets **never** returned in API responses
- First login to an empty instance creates the admin account
- API keys scoped — assign minimum permissions; full key shown only once at creation
- JWT-only endpoints reject API keys with 403
- SSH host keys use TOFU (Trust On First Use) verification
