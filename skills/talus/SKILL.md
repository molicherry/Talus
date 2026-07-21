---
name: talus
description: Interact with a running Talus instance via its REST API to manage Linux servers, execute remote commands over SSH, open interactive terminals (WebSocket), query live monitoring metrics, manage SSH credentials, create API keys, register external services with encrypted credentials, and proxy authenticated requests through the relay endpoint. Talus is a self-hosted VPS management platform (Go backend + React frontend + PostgreSQL). Use when user wants to manage servers via Talus API, execute commands on remote hosts, check server metrics, manage SSH credentials programmatically, automate VPS operations through Talus, register proxied services, or relay API calls to external services. Triggers: "Talus API", "manage server via Talus", "execute command on server", "check server metrics Talus", "add SSH credential", "create API key", "relay request", "register service", "Talus 管理", "通过Talus执行命令".
---

# Talus API

Interact with a Talus instance through its REST API. Talus connects to Linux servers over SSH — manage servers, credentials, commands, terminals, and monitoring from a central hub.

Full endpoint reference and data models: [REFERENCE.md](REFERENCE.md).

## Quick Connect

```bash
TALUS_URL="http://localhost:8080"
```

All responses: `{"data": <payload>}`. Errors: `{"error": {"code": <int>, "message": <string>}}`.

## Authentication

| Method | Header | How to obtain | Privilege |
|--------|--------|---------------|-----------|
| JWT (Bearer) | `Authorization: Bearer <token>` | `POST /api/v1/auth/login` | Full access |
| API Key | `X-API-Key: <key>` | `POST /api/v1/api-keys` (JWT only) | Scoped by scopes + server_ids |

**JWT-only** (API keys always rejected): `DELETE /servers/{id}`, all credential mutations, all API key management, service management (`POST/PUT/DELETE` + `GET /{id}/credentials`), auth endpoints.

### Setup & Login

```bash
# Check if first-time setup needed
curl -s $TALUS_URL/api/v1/auth/setup  # → {"data":{"needed":true}}

# Login (creates admin on first call)
TOKEN=$(curl -s -X POST $TALUS_URL/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"your-password"}' | jq -r '.data.token')
```

## Common Workflows

### Add a server and run a command

```bash
# 1. Create SSH credential
curl -s -X POST $TALUS_URL/api/v1/credentials \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"my-key","username":"root","auth_type":"private_key","private_key":"-----BEGIN OPENSSH PRIVATE KEY-----\n...\n-----END OPENSSH PRIVATE KEY-----"}'

# 2. Register server
curl -s -X POST $TALUS_URL/api/v1/servers \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"prod-db","host":"10.0.1.10","port":22,"credential_id":1}'

# 3. Execute command
curl -s -X POST $TALUS_URL/api/v1/servers/1/exec \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"command":"uptime"}'
```

### Query metrics

```bash
FROM=$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)
TO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
curl -s "$TALUS_URL/api/v1/servers/1/metrics?from=$FROM&to=$TO&interval=1m" \
  -H "Authorization: Bearer $TOKEN"
```

### WebSocket Terminal

Auth via `Authorization: Bearer` header (browsers can't set WS headers — use server-side client or proxy).

**Node.js:**
```js
const WebSocket = require('ws');
const ws = new WebSocket('ws://localhost:8080/api/v1/servers/1/terminal', {
  headers: { 'Authorization': `Bearer ${token}` }
});
ws.on('message', (data) => {
  const msg = JSON.parse(data);  // type: "connected"|"output"|"error"|"disconnected"
  if (msg.type === 'output') console.log(msg.data);
});
ws.send(JSON.stringify({ type: 'input', data: 'ls -la\n' }));
ws.send(JSON.stringify({ type: 'resize', cols: 120, rows: 40 }));
```

**CLI:**
```bash
websocat -H="Authorization: Bearer $TOKEN" "ws://localhost:8080/api/v1/servers/1/terminal"
```

### Create a scoped API key

```bash
# Create key (JWT required)
KEY_RESP=$(curl -s -X POST $TALUS_URL/api/v1/api-keys \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"monitoring","scopes":["servers:read","metrics:read"]}')
API_KEY=$(echo $KEY_RESP | jq -r '.data.key')

# Use it
curl -s $TALUS_URL/api/v1/servers -H "X-API-Key: $API_KEY"

# Missing scope → 403
curl -s -X POST $TALUS_URL/api/v1/servers -H "X-API-Key: $API_KEY" \
  -H "Content-Type: application/json" -d '{"name":"test","host":"10.0.0.1","port":22}'
# → {"error":{"code":403,"message":"insufficient scope: requires servers:write"}}
```

### Register a service and relay requests

```bash
# 1. Register service with credentials (JWT required)
curl -s -X POST $TALUS_URL/api/v1/services \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"grafana","display_name":"Grafana","base_url":"http://localhost:3000",
       "credentials":{"token":"glsa_abc..."},"credential_hints":{"token":"API token"}}'

# 2. Relay request — credentials injected automatically
curl -s -X POST $TALUS_URL/api/v1/services/1/relay \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"method":"GET","path":"/api/dashboards/home"}'

# 3. Relay with {{key}} placeholder substitution in path/headers/body
curl -s -X POST $TALUS_URL/api/v1/services/1/relay \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"method":"GET","path":"/api/search","headers":{"Authorization":"Bearer {{token}}"}}'
```

## Security Notes

- SSH and service credentials encrypted with AES-256-GCM at rest — **never returned** in API responses
- First login to empty instance creates admin account
- API keys: scoped (7 scope types + optional server_ids), shown once at creation, JWT-only endpoints always reject
- SSH host keys: TOFU (Trust On First Use) verification
