---
name: talus
description: >-
  Interact with a running Talus server management platform via REST API.
  Capabilities: list servers, execute remote commands over SSH,
  query live monitoring metrics, list credentials (metadata only),
  list/relay proxied services, create/update servers.
  Services are ONLY reachable through Talus relay — list them, read their
  usage guides, and proxy requests; never SSH around the platform or guess URLs.
  API key scoped — credential mutations, API key management,
  service creation, and secret reveal require the Talus Web UI.
  Use when user wants to manage servers, execute commands, check server metrics,
  list credentials, list or use proxied services, relay service requests,
  or add/update servers.
triggers: ["Talus", "manage server", "execute command on server", "check server metrics", "list credentials", "relay request", "list servers", "Talus 管理", "通过Talus执行命令", "service", "services", "proxied service", "service list", "service usage", "服务", "服务列表", "使用服务", "代理服务", "dokploy", "portainer"]
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
| List/get services | any valid key (no specific scope required) |
| Add/update server | `servers:write` |
| Open WebSocket terminal | `servers:terminal` |

**Default scopes** (when creating a key with no selection): `servers:read`, `servers:exec`, `servers:terminal`, `metrics:read`, `credentials:read`. `servers:write` and `services:relay` are **opt-in**.

### jwtOnly — hard-rejected regardless of scopes

Even a key with every scope does not help. These require the **Talus Web UI** (user JWT):
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

Every API endpoint requires authentication (`X-API-Key` header or user JWT) — there are
no unauthenticated routes. `GET /services` / `GET /services/{id}` work with any valid
key (no specific scope), but an invalid or missing credential returns 401.
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
Body: {"method": "GET", "path": "/api/endpoint", "headers": {...}, "body": {...}}

Credentials stored on the service are injected automatically.
Placeholder substitution: {{key}} in headers/body → credential value.
  e.g. {"headers": {"Authorization": "Bearer {{token}}"}}
```

**Building the relay request correctly** (learned the hard way — these two
mistakes silently break parameterized calls):

1. **`path` is forwarded verbatim** — query strings pass through as-is. Use the
   target service's own parameter style, e.g. Dokploy accepts direct query
   params (`/api/compose.one?composeId=xxx`), NOT the generic tRPC wrapper
   (`?input={"json":{...}}`). Check the service's usage_guide for the exact
   format before guessing.
2. **`body` must be a JSON object/array, not a string.** Passing a pre-serialized
   string (`"{\"composeId\":\"x\"}"`) forwards a quoted string to the target and
   its fields come back `undefined`. Pass the object itself:
   `{"method": "POST", "path": "/api/x", "body": {"composeId": "x"}}`.

The relay never adapts to the target — it forwards whatever you give it. Read the
service's usage_guide, then construct method/path/headers/body per the target API.

### "How do I use a specific service?"

Each registered service can carry its own usage guide (`usage_guide`, markdown) — the
per-service "skill" written by whoever registered it. Always check it before calling:

```
1. List available services: GET /api/v1/services
   → name, description, usage_guide_excerpt (first ~200 chars)
2. Pick the target and read its guide: GET /api/v1/services/{id}
   → usage_guide (full markdown)
3. If usage_guide exists, follow it to build the relay request
   (method/path/headers/body, {{key}} placeholder substitution).
4. If absent, infer from description + credential_hints, or ask the user.
```

The service directory (name + description + guide excerpt) is injected by the
standard platform plugin (see ai-integration/ in the repo, install via
install.sh) whenever the user mentions services — injection affects only the
current turn and is never persisted to history. If the plugin is not installed
(e.g. Cursor), the rules above still apply — always list services via the API
before calling one. The full usage_guide is fetched on demand and never embedded
in the system prompt.


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

1. **Services are ONLY reachable through Talus relay.** A service's `base_url` is an
   internal address you cannot reach directly, and its credentials are injected by
   Talus. Before using any service: (a) `GET /api/v1/services` to see the directory
   (name, description, usage_guide_excerpt); (b) `GET /api/v1/services/{id}` to read
   its `usage_guide`; (c) build the relay request per the guide:
   `POST /api/v1/services/{id}/relay`. Never SSH into a server to "find" a service
   or guess its URL — if you don't know what services exist, ask the API.
2. **Credentials NEVER appear in API responses** — list endpoints return metadata only. Secrets require the Talus Web UI (all reveal endpoints are jwtOnly).
3. **API key raw value shown ONCE** at creation via Web UI. No API-based retrieval.
4. **scopes and server_ids are orthogonal** — both must match. server_ids=[] means all servers.
5. **`services:relay` and `servers:write` are opt-in** — not in default scopes.
6. **Terminal** — pass API key via `X-API-Key` header during WebSocket upgrade.
