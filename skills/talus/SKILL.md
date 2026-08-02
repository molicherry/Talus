---
name: talus
description: >-
  Interact with a running Talus server management platform via REST API.
  Capabilities: list servers, execute remote commands over SSH,
  query live monitoring metrics, list credentials (metadata only),
  list/relay proxied services, create/update servers.
  API key scoped — credential mutations, API key management,
  service creation, and secret reveal require the Talus Web UI.
  Use when user wants to manage servers, execute commands, check server metrics,
  list credentials, relay service requests, or add/update servers.
triggers: ["Talus", "manage server", "execute command on server", "check server metrics", "list credentials", "relay request", "list servers", "Talus 管理", "通过Talus执行命令"]
---

# Talus Skill — Agent Operation Guide

Talus is a self-hosted VPS management platform: Go backend, React frontend, PostgreSQL. Servers connect over SSH — no agent installed on targets.

## Quick Start

**Configuration is read from environment variables — no manual setup needed if already configured.**

```bash
# Defaults — override via env vars or /etc/environment
TALUS_URL="${TALUS_URL:-http://localhost:8080}"
TALUS_API_KEY="${TALUS_API_KEY:-}"
```

If `TALUS_URL` is not set, default to `http://localhost:8080`.  
If `TALUS_API_KEY` is set, use it automatically via `X-API-Key` header.  
If neither is configured, **ask the user** for the URL and credentials.

Response envelope: `{"data": <payload>}`. Errors: `{"error": {"code": <int>, "message": <string>}}`.

## Auth

All requests use `X-API-Key` header. **Auto-auth**: If `TALUS_API_KEY` is set, use it. If not, ask the user.

### What you can do

| Action | Scope needed |
| -------- | -------------- |
| List servers, get detail | `servers:read` |
| Execute command | `servers:exec` |
| Query metrics | `metrics:read` |
| List credentials (no secrets) | `credentials:read` |
| List/get services | (unrestricted) |
| Relay service request | `services:relay` |
| Add/update server | `servers:write` |
| Open WebSocket terminal | `servers:terminal` |

**Default scopes** (when creating a key with no selection): `servers:read`, `servers:exec`, `servers:terminal`, `metrics:read`, `credentials:read`. `servers:write` and `services:relay` are **opt-in**.

### jwtOnly — hard-rejected regardless of scopes

Even `"*"` wildcard scope does not help. These require the **Talus Web UI**:

| Route | Operation |
| ------- | ----------- |
| `DELETE /servers/{id}` | Delete server |
| `POST /credentials` | Create credential |
| `PUT /credentials/{id}` | Update credential |
| `DELETE /credentials/{id}` | Delete credential |
| `GET /credentials/{id}/reveal` | Reveal credential secret |
| `GET /api-keys` | List API keys |
| `POST /api-keys` | Create API key |
| `DELETE /api-keys/{id}` | Revoke API key |
| `GET /api-keys/{id}/reveal` | Reveal API key raw value |
| `POST /services` | Create service |
| `PUT /services/{id}` | Update service |
| `DELETE /services/{id}` | Delete service |
| `GET /services/{id}/credentials` | Get service credentials |
| `GET /auth/profile`, `PUT /auth/password` | Auth operations |

Any endpoint not in the scope table or jwtOnly table is **unrestricted** — e.g. `GET /services`, `GET /services/{id}` work with any valid key.

## Decision Trees

### "What servers do I have?" / "Show me server X"

```
GET /api/v1/servers           → list all with status + latest_metrics
GET /api/v1/servers/summary   → lightweight list (id, name, description, host,
                                 credential_id, status) — no metrics
GET /api/v1/servers/{id}      → full detail with credential info
```

Key fields: `status` ("online"|"offline"|"checking"|"unknown"), `latest_metrics` {cpu_percent, memory_percent, disk_percent}, `os`, `cpu_model`, `uptime_seconds`.

### "Run a command on server X"

```
1. Know the server ID?
   ├─ Yes → POST /api/v1/servers/{id}/exec  {"command": "...", "timeout": 60}
   └─ No  → GET /api/v1/servers to find it, then exec
2. Default timeout 30s, max 300s.
3. Response: {"data": {"stdout": "...", "stderr": "...", "exit_code": 0}}
```

### "Check metrics on server X"

```
GET /api/v1/servers/{id}/metrics?from=2026-07-27T00:00:00Z&to=2026-07-27T06:00:00Z&interval=5m

Parameters: from, to (ISO 8601), interval (1m|5m|15m|1h)
Response: array of {time, cpu_percent, memory_percent, disk_percent,
                    load_1, load_5, load_15, swap_percent,
                    net_recv_rate, net_sent_rate, disk_read_rate, disk_write_rate}
```

### "Proxy a request through service X"

```
POST /api/v1/services/{id}/relay
Body: {"method": "GET", "path": "/api/endpoint", "headers": {...}, "body": "..."}

Credentials stored on the service are injected automatically.
Placeholder substitution: {{key}} in headers/body → credential value.
  e.g. {"headers": {"Authorization": "Bearer {{token}}"}}
```

### "Something is returning 403"

Two checks, in order:

1. **jwtOnly route?** → The table above. Solution: use Talus Web UI.
2. **Right scope?** `servers:read|write|exec|terminal`, `metrics:read`, `credentials:read`, `services:relay` — match endpoint to scope. Solution: re-create key with correct scopes.
3. **Right server?** `server_ids=[]` = all; `[1,3]` = only 1 and 3. Solution: re-create key.

## Multi-Step Workflows

### Full server setup

```
1. Create credential (Web UI) — credential mutations are jwtOnly:
   Talus Web UI → Credentials → Add Credential
2. Register server (requires servers:write):
   POST /api/v1/servers  {"name":"prod-db","host":"10.0.1.10","port":22,"credential_id":<id>}
3. Verify (requires servers:exec):
   POST /api/v1/servers/{id}/exec  {"command":"whoami"}
   → Expect stdout: "root", exit_code: 0
```

### Register a proxied service

```
1. Create service (Web UI) — service creation is jwtOnly:
   Talus Web UI → Services → Add Service (name, base_url, credentials)
2. Relay via API (requires services:relay):
   POST /api/v1/services/{id}/relay  {"method":"GET","path":"/api/dashboards/home"}
```

### Create an API key (Web UI only)

Guide user to **Talus Web UI → API Keys**:

- **Defaults**: `servers:read`, `exec`, `terminal`, `metrics:read`, `credentials:read`
- **Opt-in**: `servers:write`, `services:relay`
- **Server restriction**: optional server_ids, omit for full access

## Critical Rules

1. **Credentials NEVER appear in API responses** — list endpoints return metadata only. Secrets require the Talus Web UI (all reveal endpoints are jwtOnly).
2. **API key raw value shown ONCE** at creation via Web UI. No API-based retrieval.
3. **scopes and server_ids are orthogonal** — both must match. server_ids=[] means all servers.
4. **`services:relay` and `servers:write` are opt-in** — not in default scopes.
5. **Terminal** — pass API key via `X-API-Key` header during WebSocket upgrade.
