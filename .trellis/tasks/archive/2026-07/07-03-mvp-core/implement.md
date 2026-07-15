# MVP Core Platform — Implementation Plan

> Phased execution plan with ordered deliverables, validation gates, and rollback points.

---

## Phase Overview

| Phase | Name | Features | Depends On | Est. Effort |
|:-----:|------|----------|:----------:|:-----------:|
| 1 | Foundation | F1 (Auth), DB schema, project scaffolding | — | 1-2 days |
| 2 | Server & Credential Mgmt | F2 (Server CRUD), F3 (Credential encryption) | Phase 1 | 1-2 days |
| 3 | SSH Execution & Terminal | F4 (Exec), F5 (Terminal) | Phase 2 | 2-3 days |
| 4 | Monitoring Pipeline | F6 (Agent), F7 (Dashboard) | Phase 2 | 2-3 days |
| 5 | Integration & Deployment | F8 (Docker Compose), polish | Phase 3, 4 | 1 day |

### Dependency Graph

```
Phase 1 (Foundation)
    │
    ├──► Phase 2 (Server & Credential Mgmt)
    │        │
    │        ├──► Phase 3 (SSH Exec & Terminal)
    │        │
    │        └──► Phase 4 (Monitoring Pipeline)
    │
    └──► Phase 3 + Phase 4 ──► Phase 5 (Integration & Deploy)
```

Phases 3 and 4 are independent of each other after Phase 2. They can be developed in parallel by different developers (or sequentially in any order).

---

## Phase 1: Foundation

### Goal
Project scaffolding, database connectivity, user authentication. After Phase 1, the system boots up and a user can log in.

### Tasks

#### 1.1 Backend Scaffolding
- [ ] `go mod init github.com/vpsmanager/backend`
- [ ] Create `backend/cmd/server/main.go` — config loading, logger init, graceful shutdown skeleton
- [ ] Create `backend/internal/config/config.go` — env var loading with defaults, validation
- [ ] Create `backend/internal/model/base.go` — `BaseModel` struct
- [ ] Create `backend/internal/server/response.go` — `WriteJSON`, `WriteError` with JSON envelope
- [ ] Create `backend/internal/server/errors.go` — `AppError`, sentinel errors, `ValidationError`
- [ ] Create `backend/internal/server/middleware/` — `requestid.go`, `logging.go`, `cors.go`
- [ ] Create `backend/internal/server/router.go` — Chi router init with middleware stack, placeholder health endpoint

**Validation**:
```bash
cd backend && go build ./... && go vet ./...
curl http://localhost:8080/healthz  # → 200 {"data":{"status":"ok"}}
```

#### 1.2 Database Setup
- [ ] `docker compose up db` — start PostgreSQL+TimescaleDB container
- [ ] Create `backend/migrations/000001_create_users.up.sql` / `.down.sql`
- [ ] Create `backend/migrations/000002_create_servers.up.sql` / `.down.sql`
- [ ] Create `backend/migrations/000003_create_credentials.up.sql` / `.down.sql`
- [ ] Create `backend/migrations/000004_create_audit_logs.up.sql` / `.down.sql`
- [ ] Create `backend/migrations/000005_create_metrics.up.sql` / `.down.sql`
- [ ] Create `backend/Dockerfile.migrate` — golang-migrate image
- [ ] Run migrations against local DB

**Validation**:
```bash
docker compose up -d db
migrate -path backend/migrations -database "$DATABASE_URL" up
psql "$DATABASE_URL" -c "\dt"  # → users, servers, ssh_credentials, audit_logs, metrics
```

#### 1.3 User Model & Repository
- [ ] Create `backend/internal/model/user.go` — `User` struct (embeds `BaseModel`)
- [ ] Create `backend/internal/repository/user.go` — `FindByUsername()`, `Create()`, `FindByID()`
- [ ] Create `backend/internal/pkg/token/jwt.go` — `GenerateToken()`, `ValidateToken()`, JWT claims struct

**Validation**:
```bash
cd backend && go test ./internal/repository/...
```

#### 1.4 Auth Service & Handler (F1)
- [ ] Create `backend/internal/service/auth.go` — `Login(username, password)`, `RegisterFirstUser()` (seed if no users exist)
- [ ] Create `backend/internal/server/middleware/auth.go` — JWT validation middleware, user injection into context
- [ ] Create `backend/internal/handler/auth.go` — `POST /api/v1/auth/login`
- [ ] Register route in `router.go`
- [ ] Password hashing with `golang.org/x/crypto/bcrypt` (cost 12)

**Validation**:
```bash
# First login (auto-creates user if no users exist)
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123"}'
# → 200 {"data":{"token":"eyJ..."}}

# Protected endpoint test
curl http://localhost:8080/api/v1/servers -H "Authorization: Bearer $TOKEN"
# → 200 {"data":[]} (empty list, not 401)
```

#### 1.5 Frontend Scaffolding
- [ ] `npm create vite@latest frontend -- --template react-ts`
- [ ] Install deps: `react-router-dom`, `@tanstack/react-query`, `tailwindcss`, `@shadcn/ui`, `lucide-react`, `zod`, `react-hook-form`, `@hookform/resolvers`, `jwt-decode`
- [ ] Init shadcn/ui: add `button`, `input`, `label`, `card`, `table`, `dialog`, `dropdown-menu`, `toast` (sonner)
- [ ] Init Biome: `biome.json` with project config
- [ ] Create `src/lib/api-client.ts` — fetch wrapper with JWT header injection, error parsing
- [ ] Create `src/lib/auth.ts` — `getAuthToken()`, `setAuthToken()`, `clearAuthToken()`
- [ ] Create `src/hooks/use-auth.ts` — JWT decode, user info, `isAuthenticated`
- [ ] Create `src/app/query-client.ts` — TanStack Query client config
- [ ] Create `src/app/providers.tsx` — QueryClientProvider + BrowserRouter
- [ ] Create `src/app/router.tsx` — route definitions with `<ProtectedRoute>` wrapper
- [ ] Create `src/components/layout/` — `main-layout.tsx`, `sidebar.tsx`, `header.tsx`
- [ ] Create `src/features/auth/components/login-form.tsx` — login page with react-hook-form + zod
- [ ] Create `src/features/auth/hooks/use-login.ts` — TanStack mutation
- [ ] Create `src/features/auth/api.ts` — `login()`

**Validation**:
```bash
cd frontend && npm run dev
# Browser: http://localhost:5173/login
# Login with admin/admin123 → redirect to /
```

### Phase 1 Rollback
- Delete `backend/` directory, re-scaffold
- `docker compose down -v` (wipes DB volume)
- Delete `frontend/` directory, re-scaffold

### Phase 1 Completion Criteria
- [ ] Backend boots, serves health check
- [ ] Database migrations run cleanly
- [ ] `POST /api/v1/auth/login` works, returns valid JWT
- [ ] Protected routes reject unauthenticated requests (401)
- [ ] Frontend login page works end-to-end
- [ ] `go vet ./...` clean
- [ ] `biome check ./src` clean
- [ ] `tsc --noEmit` clean

### Phase 1 Test Scenarios

> Run these manually or automate as table-driven tests. Each scenario is independently verifiable.

**Auth — Login**

```
GIVEN no users exist in the database
 WHEN POST /api/v1/auth/login {"username":"admin","password":"admin123"}
 THEN response is 200, returns valid JWT token
  AND a user "admin" is created in the database with bcrypt-hashed password
  AND password hash in DB is NOT "admin123"

GIVEN user "admin" exists
 WHEN POST /api/v1/auth/login {"username":"admin","password":"wrong"}
 THEN response is 401 {"error":{"code":401,"message":"unauthorized"}}
  AND no token is returned

GIVEN user "admin" exists
 WHEN POST /api/v1/auth/login {"username":"admin","password":"admin123"}
 THEN response is 200, returns a DIFFERENT JWT than the previous login
  AND JWT decodes to {sub, username:"admin", role:"admin", exp}
```

**Auth — Token Validation**

```
GIVEN a valid JWT token
 WHEN GET /api/v1/servers with header "Authorization: Bearer <token>"
 THEN response is 200, returns data (even if empty array)

GIVEN an expired JWT token (exp < now)
 WHEN GET /api/v1/servers with header "Authorization: Bearer <expired-token>"
 THEN response is 401 {"error":{"code":401,"message":"unauthorized"}}

GIVEN no Authorization header
 WHEN GET /api/v1/servers
 THEN response is 401

GIVEN a JWT with tampered signature
 WHEN GET /api/v1/servers with header "Authorization: Bearer <tampered-token>"
 THEN response is 401
```

**Auth — Input Validation**

```
GIVEN any state
 WHEN POST /api/v1/auth/login {"username":""}
 THEN response is 422 with validation error details for "username"
  AND no user is created

GIVEN any state
 WHEN POST /api/v1/auth/login {"username":"admin"}  (missing password)
 THEN response is 422 with validation error details for "password"

GIVEN any state
 WHEN POST /api/v1/auth/login {"username":"a","password":"b"}  (too short)
 THEN response is 422
```

**Database — Migrations**

```
GIVEN a clean PostgreSQL instance
 WHEN running all migrations (up)
 THEN all 5 tables exist (users, servers, ssh_credentials, audit_logs, metrics)
  AND metrics is a TimescaleDB hypertable (chunk_time_interval = 1 day)
  AND running the same migrations again is idempotent (no errors)

GIVEN all migrations applied
 WHEN running down migrations in reverse order
 THEN all tables are dropped
  AND re-running up migrations succeeds
```

**Frontend — Routing**

```
GIVEN user is NOT authenticated (no token in localStorage)
 WHEN navigating to /
 THEN redirect to /login
 WHEN navigating to /servers
 THEN redirect to /login

GIVEN user IS authenticated (valid token in localStorage)
 WHEN navigating to /login
 THEN redirect to /
```

---



## Phase 2: Server & Credential Management

### Goal
Server CRUD + SSH credential management with encryption at rest. After Phase 2, the operator can add servers and configure their SSH credentials.

### Tasks

#### 2.1 Server Model, Repo, Service, Handler (F2)
- [ ] Create `backend/internal/model/server.go`
- [ ] Create `backend/internal/repository/server.go` — `FindAll()`, `FindByID()`, `Create()`, `Update()`, `Delete()`
- [ ] Create `backend/internal/service/server.go` — business logic, status check placeholder
- [ ] Create `backend/internal/handler/server.go` — `GET/POST /api/v1/servers`, `GET/PUT/DELETE /api/v1/servers/{id}`
- [ ] Register routes in `router.go`

#### 2.2 Encryption Package
- [ ] Create `backend/internal/pkg/crypto/key.go` — `NewMasterKey(hexKey)` with Argon2id derivation
- [ ] Create `backend/internal/pkg/crypto/aes.go` — `Encrypt(plaintext, key)`, `Decrypt(ciphertext, key)`
- [ ] Unit tests: round-trip encrypt/decrypt, wrong key fails, empty plaintext

**Validation**:
```bash
go test ./internal/pkg/crypto/... -v -count=1
```

#### 2.3 Credential Model, Repo, Service, Handler (F3)
- [ ] Create `backend/internal/model/credential.go` — `SSHCredential` with `json:"-"` on encrypted fields
- [ ] Create `backend/internal/repository/credential.go` — `FindByServerID()`, `Create()`, `Delete()`
- [ ] Create `backend/internal/service/credential.go` — encrypt on save, decrypt on read (never return plaintext to API)
- [ ] Create `backend/internal/handler/credential.go` — `POST/GET/DELETE /api/v1/credentials`
- [ ] Register routes

**Validation**:
```bash
# Create credential
curl -X POST http://localhost:8080/api/v1/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"server_id":1,"auth_type":"password","username":"root","password":"secret123"}'
# → 200 {"data":{"id":1,"server_id":1,"auth_type":"password","username":"root"}}
# Note: encrypted_password never appears in response

# Verify DB encryption
psql "$DATABASE_URL" -c "SELECT auth_type, encrypted_password FROM ssh_credentials;"
# → encrypted_password is base64 gibberish, NOT "secret123"
```

#### 2.4 Frontend — Server CRUD Pages
- [ ] Create `src/types/models.ts` — `Server`, `SSHCredential` types + Zod schemas
- [ ] Create `src/types/api.ts` — `ApiResponse<T>`, `ApiError`, `PaginationMeta`
- [ ] Create `src/features/servers/api.ts` — `getServers()`, `createServer()`, etc.
- [ ] Create `src/features/servers/hooks/` — `useServers`, `useCreateServer`, `useDeleteServer`
- [ ] Create `src/features/servers/components/` — `server-list.tsx`, `server-form.tsx`, `server-card.tsx`
- [ ] Create server list page + create form page + detail page

#### 2.5 Frontend — Credential Pages
- [ ] Create `src/features/credentials/api.ts`
- [ ] Create `src/features/credentials/hooks/` — useCredentials, useCreateCredential
- [ ] Create `src/features/credentials/components/` — credential form (password vs key toggle), credential list

### Phase 2 Completion Criteria
- [ ] Full server CRUD via API and UI
- [ ] Credentials encrypted at rest (verified in DB)
- [ ] Credentials never exposed in API responses
- [ ] `go test ./...` passes
- [ ] `biome check` + `tsc --noEmit` clean

### Phase 2 Test Scenarios

**Server CRUD**

```
GIVEN an authenticated user
 WHEN POST /api/v1/servers {"name":"web-01","host":"10.0.0.1"}
 THEN response is 201, returns server with id, name, host, port=22
  AND server appears in GET /api/v1/servers list

GIVEN server "web-01" exists
 WHEN POST /api/v1/servers {"name":"web-01","host":"10.0.0.2"}
 THEN response is 409 {"error":{"code":409,"message":"resource already exists"}}

GIVEN server id=1 exists
 WHEN PUT /api/v1/servers/1 {"name":"web-02","port":2222}
 THEN response is 200, name is now "web-02", port is 2222
  AND host unchanged if not in payload

GIVEN server id=1 exists
 WHEN DELETE /api/v1/servers/1
 THEN response is 200
  AND GET /api/v1/servers/1 returns 404
  AND server row has deleted_at IS NOT NULL (soft delete)

GIVEN server id=999 does not exist
 WHEN GET /api/v1/servers/999
 THEN response is 404

GIVEN any state
 WHEN POST /api/v1/servers {"name":""}
 THEN response is 422 with validation error for "name"
```

**Credential Encryption**

```
GIVEN server id=1 exists
 WHEN POST /api/v1/credentials {"server_id":1,"auth_type":"password","username":"root","password":"secret123"}
 THEN response is 201
  AND response body does NOT contain "secret123" or "password" field
  AND response body contains key_fingerprint: null (password type has no fingerprint)

GIVEN a credential saved with password
 WHEN querying ssh_credentials table directly
 THEN encrypted_password column is NOT "secret123"
  AND encrypted_password is non-empty base64 string

GIVEN credential id=1 exists (password type)
 WHEN decrypting encrypted_password with VPSMANAGER_MASTER_KEY
 THEN plaintext equals "secret123"

GIVEN wrong VPSMANAGER_MASTER_KEY at startup
 WHEN attempting to decrypt any credential
 THEN decryption fails with authentication error
  AND Hub logs ERROR but does not crash

GIVEN server id=1 exists
 WHEN POST /api/v1/credentials {"server_id":1,"auth_type":"private_key","username":"root","private_key":"-----BEGIN RSA..."}
 THEN response is 201
  AND key_fingerprint is non-empty SHA256 hex string
  AND encrypted_private_key is base64-encoded ciphertext
  AND private_key plaintext never appears in response

GIVEN server id=1 already has a credential
 WHEN POST /api/v1/credentials for same server_id
 THEN response is 409 (one credential per server)

GIVEN credential id=1 exists
 WHEN DELETE /api/v1/credentials/1
 THEN response is 200
  AND GET /api/v1/credentials returns empty list

GIVEN any state
 WHEN POST /api/v1/credentials {"server_id":1,"auth_type":"invalid","username":"root"}
 THEN response is 422 with validation error for "auth_type"

GIVEN any state
 WHEN POST /api/v1/credentials {"server_id":1,"auth_type":"password","username":"root"}
  (missing password field)
 THEN response is 422

GIVEN any state
 WHEN POST /api/v1/credentials {"server_id":1,"auth_type":"password","username":"root","password":"x","private_key":"y"}
  (both auth fields provided)
 THEN response is 422 — exactly one of password or private_key required
```

**Frontend — Server & Credential UI**

```
GIVEN authenticated user on /servers page
  AND no servers exist
 THEN page shows "No servers yet" empty state with "Add Server" button

GIVEN authenticated user on /servers/new page
 WHEN filling form with name="web-01", host="10.0.0.1", port=22
  AND clicking Submit
 THEN toast shows "Server created"
  AND redirected to /servers
  AND server appears in list

GIVEN server exists in list
 WHEN clicking delete button
 THEN confirm dialog appears
  WHEN clicking Confirm
  THEN server removed from list, toast shows "Server deleted"

GIVEN credential form for a server
 WHEN selecting auth_type="password" and entering password
 THEN "private key" field is hidden
 WHEN switching to auth_type="private_key"
 THEN "password" field is hidden, file upload field appears
```

### Phase 2 Rollback
- Revert to Phase 1 commit. Files touched: `model/server.go`, `model/credential.go`, `repository/`, `service/`, `handler/`, `pkg/crypto/`, frontend `features/servers/`, `features/credentials/`, `types/`.

---

## Phase 3: SSH Execution & Terminal

### Goal
Command execution via SSH and interactive WebSocket terminal. After Phase 3, the operator can run commands on servers and use an interactive terminal.

### Tasks

#### 3.1 SSH Connection Pool
- [ ] Create `backend/internal/pkg/sshpool/config.go` — connection config builder from credential + server
- [ ] Create `backend/internal/pkg/sshpool/pool.go` — `Pool` with `Get()`, `Release()`, eviction loop
- [ ] Create `backend/internal/pkg/sshpool/pool_test.go` — table-driven tests (use testcontainers SSH)
- [ ] Create `backend/internal/service/ssh.go` — `Exec(ctx, serverID, command, timeout)` using pool

**Validation**:
```bash
go test ./internal/pkg/sshpool/... -v -count=1
# Requires testcontainers SSH server or a mock SSH server
```

#### 3.2 Command Execution Handler (F4)
- [ ] Create `backend/internal/handler/exec.go` — `POST /api/v1/servers/{id}/exec`
- [ ] Request validation: command non-empty, timeout 1-300s
- [ ] Register route

**Validation**:
```bash
curl -X POST http://localhost:8080/api/v1/servers/1/exec \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"command":"uptime"}'
# → 200 {"stdout":"14:30:00 up 10 days...","stderr":"","exit_code":0,"duration_ms":342}
```

#### 3.3 Frontend — Command Execution UI
- [ ] Create `src/types/ssh.ts` — `ExecRequest`, `ExecResponse`
- [ ] Create `src/features/servers/api.ts` — `execCommand(serverId, command)`
- [ ] Create `src/features/servers/components/exec-panel.tsx` — command input + output panel
- [ ] Create `src/features/servers/hooks/use-exec.ts` — mutation hook
- [ ] Add exec page route `/servers/:id/exec` or integrate into server detail page

#### 3.4 Terminal Service (F5)
- [ ] Create `backend/internal/service/terminal.go` — `StartSession(ctx, serverID, wsConn)`
- [ ] Create `backend/internal/handler/terminal.go` — `WS /api/v1/servers/{id}/terminal`
- [ ] WebSocket upgrade with JWT validation via query param
- [ ] PTY request with `xterm-256color`, initial size 80×24
- [ ] Bidirectional relay: SSH stdout → WS, WS → SSH stdin
- [ ] Resize forwarding
- [ ] Graceful close: cancel context → close SSH session → close WS

**Validation**:
```bash
# Manual: use wscat to test
wscat -c "ws://localhost:8080/api/v1/servers/1/terminal?token=$TOKEN"
# → connected, type "ls" → see output
```

#### 3.5 Frontend — Interactive Terminal
- [ ] Install `@xterm/xterm`, `@xterm/addon-fit`, `@xterm/addon-web-links`
- [ ] Create `src/features/terminal/socket.ts` — WebSocket connection management
- [ ] Create `src/features/terminal/hooks/use-terminal.ts` — xterm.js Terminal lifecycle
- [ ] Create `src/features/terminal/components/terminal.tsx` — Terminal mount/unmount, resize observer
- [ ] Add terminal page route `/servers/:id/terminal`

### Phase 3 Completion Criteria
- [ ] `POST /exec` returns command output for a real target server
- [ ] SSH connection pool reuses connections, evicts idle
- [ ] WebSocket terminal works with resize, input, output
- [ ] Command timeout kills SSH session
- [ ] `go test ./internal/pkg/sshpool/...` passes
- [ ] `go test ./internal/service/...` passes (SSH-related)
- [ ] Frontend: command exec panel + xterm.js terminal functional

### Phase 3 Test Scenarios

**SSH Connection Pool**

```
GIVEN a server with valid SSH credentials
 WHEN Get(serverID) is called for the first time
 THEN a new SSH connection is dialed and stored in pool
  AND subsequent Get(serverID) returns the SAME connection

GIVEN an idle SSH connection (no sessions for > maxIdle time)
 WHEN eviction loop runs
 THEN idle connection is closed and removed from pool

GIVEN pool at maxConns limit (3)
 WHEN a 4th Get(serverID) is called
 THEN caller blocks until a connection is released
  AND does NOT open a 4th connection

GIVEN server with INVALID credentials (wrong password)
 WHEN Get(serverID) calls dial
 THEN returns ErrSSHAuth error
  AND no connection is stored in pool
  AND Hub logs WARN with server_id (not password)
```

**Command Execution**

```
GIVEN server id=1 with valid SSH credentials (real or testcontainer)
 WHEN POST /api/v1/servers/1/exec {"command":"echo hello"}
 THEN response is 200
  AND stdout is "hello\n"
  AND stderr is empty
  AND exit_code is 0
  AND duration_ms > 0

GIVEN server id=1
 WHEN POST /api/v1/servers/1/exec {"command":"exit 42"}
 THEN response is 200
  AND exit_code is 42
  AND stderr may be empty (bash exits before stderr)

GIVEN server id=1
 WHEN POST /api/v1/servers/1/exec {"command":"sleep 60","timeout":2}
 THEN response is 504 (gateway timeout)
  AND the sleep process is killed on the target

GIVEN server id=1
 WHEN POST /api/v1/servers/1/exec {"command":""}
 THEN response is 422 — command must be non-empty

GIVEN server id=1
 WHEN POST /api/v1/servers/1/exec {"command":"$(cat /etc/passwd)"}
 THEN the command is executed as-is (no server-side shell validation)
  AND command is logged at INFO level (for audit trail)
  AND stdout/stderr are NOT logged

GIVEN server id=999 does not exist
 WHEN POST /api/v1/servers/999/exec {"command":"uptime"}
 THEN response is 404
```

**Interactive Terminal**

```
GIVEN server id=1 with valid SSH credentials
 WHEN WebSocket connects to /api/v1/servers/1/terminal?token=<jwt>
 THEN first message received is {"type":"connected","session_id":"..."}
  AND Hub has an active SSH PTY session

GIVEN an active terminal session
 WHEN client sends {"type":"input","data":"ls\n"}
 THEN client receives {"type":"output","data":"..."} with directory listing

GIVEN an active terminal session
 WHEN client sends {"type":"resize","cols":120,"rows":40}
 THEN SSH window-change request is sent to target
  AND terminal continues receiving output normally

GIVEN WebSocket connection without token
 WHEN attempting upgrade to /api/v1/servers/1/terminal
 THEN upgrade is rejected with 401

GIVEN an active terminal session
 WHEN client closes WebSocket (browser tab close)
 THEN Hub cleans up SSH session
  AND SSH client is returned to pool

GIVEN an active terminal session
 WHEN target server SSH connection drops
 THEN Hub sends {"type":"disconnected","reason":"ssh connection lost"}
  AND WebSocket is closed cleanly
```

**Frontend — xterm.js**

```
GIVEN user on /servers/:id/terminal page
 WHEN page loads
 THEN xterm.js terminal renders with dark theme (background #1a1b26)
  AND terminal connects to WebSocket

GIVEN terminal is connected
 WHEN user types "ls" and presses Enter
 THEN output appears in terminal

GIVEN terminal is connected
 WHEN user resizes browser window
 THEN terminal resize event is sent to Hub
  AND terminal cols/rows update accordingly

GIVEN terminal is connected
 WHEN server-side disconnects
 THEN terminal shows "[Disconnected]" message
  AND reconnect button is available
```

### Phase 3 Rollback
- Revert to Phase 2 commit. Files touched: `pkg/sshpool/`, `service/ssh.go`, `service/terminal.go`, `handler/exec.go`, `handler/terminal.go`, frontend `features/terminal/`, `features/servers/components/exec-panel.tsx`.

---

## Phase 4: Monitoring Pipeline

### Goal
Monitoring agent binary, metrics collection loop, TimescaleDB storage, and dashboard charts. After Phase 4, the operator can view CPU/Memory/Disk charts over time.

### Tasks

#### 4.1 Agent Binary (F6)
- [ ] Create `backend/cmd/agent/main.go` — CLI flag parsing, gopsutil collection
- [ ] Create `backend/cmd/agent/collector/cpu.go` — `gopsutil/v4/cpu.Percent()`
- [ ] Create `backend/cmd/agent/collector/memory.go` — `gopsutil/v4/mem.VirtualMemory()`
- [ ] Create `backend/cmd/agent/collector/disk.go` — `gopsutil/v4/disk.Partitions()` + `Usage()`
- [ ] JSON output to stdout
- [ ] Build: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o agent ./cmd/agent`

**Validation**:
```bash
cd backend && go build -o /tmp/agent ./cmd/agent
/tmp/agent --format json | jq
# → valid JSON with cpu, memory, disks fields
file /tmp/agent  # → statically linked
```

#### 4.2 Metrics Model & Repository
- [ ] Create `backend/internal/model/metric.go` — `Metric` struct
- [ ] Create `backend/internal/repository/metric.go` — `Insert()`, `Query(serverID, from, to, interval)`
- [ ] TimescaleDB hypertable auto-created by migration 000005 (already done in Phase 1)

**Validation**:
```bash
go test ./internal/repository/... -run Metric -v
```

#### 4.3 Monitor Service (F6)
- [ ] Create `backend/internal/service/monitor.go` — background goroutine, collection loop
- [ ] Agent binary deployment: `ensureAgent()` — SCP binary to target if missing/outdated
- [ ] `collectMetrics()` — SSH exec agent, parse JSON, store
- [ ] Server status tracking: consecutive failures → "offline"
- [ ] Register monitor service startup in `cmd/server/main.go`

#### 4.4 Metrics Handler & API (F6)
- [ ] Create `backend/internal/handler/metrics.go` — `GET /api/v1/metrics/{serverId}?from=&to=&interval=`
- [ ] Query parameter validation (time range, interval)
- [ ] Register route

**Validation**:
```bash
# After monitor has collected some data (wait 1-2 minutes)
curl "http://localhost:8080/api/v1/metrics/1?from=2026-07-03T00:00:00Z&to=2026-07-03T23:59:59Z" \
  -H "Authorization: Bearer $TOKEN"
# → 200 {"data":[{"time":"...","cpu_percent":23.5,...}]}
```

#### 4.5 Frontend — Monitoring Dashboard (F7)
- [ ] Install `@tremor/react`
- [ ] Create `src/types/metrics.ts` — `MetricPoint`, `TimeRange` types
- [ ] Create `src/features/monitoring/api.ts` — `getMetrics(serverId, from, to, interval)`
- [ ] Create `src/features/monitoring/hooks/use-metrics.ts` — TanStack Query with auto-refresh
- [ ] Create `src/features/monitoring/components/cpu-chart.tsx` — Tremor `AreaChart`
- [ ] Create `src/features/monitoring/components/memory-gauge.tsx` — Tremor `BarChart` or gauge
- [ ] Create `src/features/monitoring/components/disk-chart.tsx` — Tremor chart per mount
- [ ] Create `src/features/monitoring/components/time-range-selector.tsx` — 1h/6h/24h/7d buttons
- [ ] Create monitoring page route `/servers/:id/monitoring`

### Phase 4 Completion Criteria
- [ ] Agent binary builds as static binary (<15MB)
- [ ] `agent --format json` outputs valid JSON
- [ ] Monitor service collects and stores metrics every 60s
- [ ] `GET /metrics/{serverId}` returns time-series data
- [ ] Dashboard renders CPU, memory, disk charts
- [ ] Time range selector works (1h → 7d)
- [ ] Auto-refresh: dashboard updates every 60s
- [ ] Three consecutive SSH failures → server marked "offline"
- [ ] `go build ./cmd/agent` succeeds with CGO_ENABLED=0

### Phase 4 Test Scenarios

**Agent Binary**

```
GIVEN agent binary built for linux/amd64
 WHEN executed on a Linux target: /tmp/vpsmanager-agent --format json
 THEN stdout is valid JSON
  AND JSON has keys: timestamp, hostname, cpu, memory, disks
  AND cpu has keys: percent, cores, load_1, load_5, load_15
  AND memory has keys: used_bytes, total_bytes, percent
  AND disks is an array, each with: mount, device, used_bytes, total_bytes, percent

GIVEN agent binary
 WHEN executed with --format json --disk=false
 THEN JSON has cpu and memory, but no disks key

GIVEN agent binary
 WHEN executed with --format invalid
 THEN exits with non-zero code and error message to stderr

GIVEN agent binary
 WHEN executed with --help
 THEN exits 0 and prints usage
```

**Monitor Service**

```
GIVEN monitor service is running, server id=1 has valid SSH
 WHEN 60s ticker fires
 THEN Hub SSH-execs agent on server 1
  AND metrics row is INSERTed into TimescaleDB
  AND row has server_id=1, time ≈ now, cpu_percent > 0

GIVEN agent binary NOT on target server
 WHEN first collection cycle for that server
 THEN Hub SCPs agent binary to /tmp/vpsmanager-agent
  AND chmod +x on target
  AND agent execution succeeds
  AND subsequent cycles skip upload (binary already present)

GIVEN agent binary IS on target server, but Hub's bundled version is newer
 WHEN next collection cycle
 THEN Hub re-uploads agent (version mismatch detected via SHA256)
  AND executes new agent binary

GIVEN server id=1 SSH connection fails (target down)
 WHEN first collection cycle
 THEN Hub logs WARN, skips this server
  AND other servers' collection continues unaffected
 WHEN third consecutive failure for same server
 THEN server status is updated to "offline"

GIVEN agent outputs invalid JSON (corrupted binary, disk full, etc.)
 WHEN Hub parses agent stdout
 THEN Hub logs ERROR, skips this cycle
  AND no metrics row is inserted
  AND server status is NOT changed on first failure (takes 3 consecutive)
```

**Metrics Query**

```
GIVEN metrics data exists for server 1 over 24 hours
 WHEN GET /api/v1/metrics/1?from=<24h ago>&to=<now>&interval=5m
 THEN response is 200 with array of MetricPoint
  AND each point has time, cpu_percent, memory_percent, disk_percent
  AND points are ordered by time ascending
  AND number of points ≈ 288 (1440 minutes / 5)

GIVEN no metrics data for server 1
 WHEN GET /api/v1/metrics/1?from=<yesterday>&to=<now>&interval=1m
 THEN response is 200 with empty array (not 404)

GIVEN any state
 WHEN GET /api/v1/metrics/1 (missing from/to params)
 THEN response is 422 with validation error

GIVEN any state
 WHEN GET /api/v1/metrics/1?from=invalid&to=<now>
 THEN response is 422 with validation error for "from" format
```

**Dashboard — Frontend**

```
GIVEN user on /servers/:id/monitoring page
  AND metrics data exists
 WHEN page loads
 THEN CPU area chart renders with data points
  AND memory chart renders
  AND disk chart renders (or "No disk data" if no disk metrics)
  AND time range defaults to "1h"

GIVEN monitoring page with "1h" selected
 WHEN clicking "24h" time range button
 THEN charts re-fetch with new time range
  AND data points are aggregated to 15-minute buckets

GIVEN monitoring page loaded
 WHEN auto-refresh interval (60s) fires
 THEN charts update with latest data point
  AND no page reload occurs (TanStack Query refetch)

GIVEN server has no metrics yet (just added, first collection hasn't run)
 WHEN monitoring page loads
 THEN charts show "No data yet" state
  AND a spinner or countdown indicates next collection
```

### Phase 4 Rollback
- Revert to Phase 2 or Phase 3 commit. Files touched: `cmd/agent/`, `model/metric.go`, `repository/metric.go`, `service/monitor.go`, `handler/metrics.go`, frontend `features/monitoring/`, `types/metrics.ts`.

---

## Phase 5: Integration & Deployment

### Goal
Docker Compose setup, production build, end-to-end verification. After Phase 5, the entire stack starts with `docker compose up`.

### Tasks

#### 5.1 Hub Dockerfile
- [ ] Create `backend/Dockerfile` — multi-stage: build hub + agent, copy to alpine
- [ ] Agent binary bundled as `/agent/vpsmanager-agent`
- [ ] Health check endpoint added to router if missing

#### 5.2 Docker Compose
- [ ] Create `docker-compose.yml` — `db`, `migrate`, `hub` services
- [ ] Service dependency ordering: `db` (healthy) → `migrate` (completed) → `hub` (started)
- [ ] Volume mounts: `pgdata`, `agent_binary`
- [ ] `.env.example` with placeholder values

#### 5.3 Frontend Production Build
- [ ] Configure Vite proxy for dev: `http://localhost:8080` (or serve from Hub)
- [ ] `npm run build` produces `frontend/dist/`
- [ ] Decision: serve static files from Hub (`embed` directive) vs separate nginx container
  - **Decision**: MVP serves from Hub via `//go:embed frontend/dist` — single container, simpler.
  - Fallback: if Hub is behind nginx, serve static files from nginx.

#### 5.4 Static File Serving in Hub
- [ ] Add `//go:embed` directive for `frontend/dist/*` (path relative to `cmd/server/`)
- [ ] Serve `index.html` for all non-API routes (SPA fallback)
- [ ] Register in `router.go`: `r.NotFound(serveSPA)`

#### 5.5 End-to-End Smoke Test
- [ ] `docker compose up -d`
- [ ] `docker compose ps` — all services healthy
- [ ] Login via browser at `http://localhost:8080`
- [ ] Add a test server, set credential
- [ ] Run `uptime` command
- [ ] Open terminal, type `ls`
- [ ] Wait 2 minutes, view monitoring dashboard
- [ ] `docker compose down`

#### 5.6 Polish
- [ ] Loading states for all pages (skeleton or spinner)
- [ ] Error states with retry button (not white screen)
- [ ] Empty states ("No servers yet" with CTA)
- [ ] Toast notifications for create/update/delete success/failure
- [ ] Confirm dialog before delete operations
- [ ] Responsive layout (sidebar collapse on small screens)

### Phase 5 Completion Criteria
- [ ] `docker compose up` starts all services
- [ ] `docker compose ps` shows all healthy
- [ ] Browser login, server CRUD, exec, terminal, monitoring all work
- [ ] `docker compose down` cleanly stops
- [ ] All 7 acceptance criteria from PRD verified:
  - [ ] AC1: Login with username/password
  - [ ] AC2: Add, edit, list, delete target servers
  - [ ] AC3: SSH credential encrypted, never exposed in UI
  - [ ] AC4: Execute shell command on any server
  - [ ] AC5: Interactive terminal session
  - [ ] AC6: CPU/Memory/Disk charts over 1h/6h/24h/7d
  - [ ] AC7: Start stack with `docker compose up`

### Phase 5 Test Scenarios

**Docker Compose — Lifecycle**

```
GIVEN .env file with DB_PASSWORD, VPSMANAGER_MASTER_KEY, JWT_SECRET set
 WHEN docker compose up -d
 THEN all 3 services start (db, migrate, hub)
  AND docker compose ps shows all "healthy" or "running"
  AND curl http://localhost:8080/healthz returns 200

GIVEN stack is running
 WHEN docker compose down
 THEN all containers stop
  AND volumes pgdata and agent_binary persist

GIVEN stack was stopped via docker compose down
 WHEN docker compose up -d (second time)
 THEN db data is preserved (users, servers, credentials survive restart)
  AND migrate service is a no-op (already applied)

GIVEN stack is running
 WHEN docker compose down -v
 THEN all volumes are deleted
  AND docker compose up -d starts fresh (first-run bootstrap)
```

**Deployment — Smoke Test**

```
GIVEN fresh docker compose up
 WHEN browser navigates to http://localhost:8080
 THEN redirected to /login
 WHEN logging in for the first time with any username/password
 THEN account is created and login succeeds
  AND redirected to dashboard

GIVEN logged in
 WHEN adding a real target server with valid SSH credentials
  AND executing "uptime"
 THEN command output appears on screen within 5 seconds
  AND exit code is 0

GIVEN command execution works
 WHEN navigating to /servers/:id/terminal
 THEN xterm.js loads and connects
  AND typing "ls" shows directory listing

GIVEN server is added with monitoring configured
 WHEN waiting 2 minutes
  AND navigating to /servers/:id/monitoring
 THEN charts show CPU, memory, disk data over last 1h

GIVEN everything works in browser
 WHEN checking Hub logs: docker compose logs hub
 THEN no ERROR-level logs (only INFO and WARN)
  AND no credential plaintext in any log line
```

**Polish — UI States**

```
GIVEN any page with data loading (slow network)
 WHEN component mounts
 THEN skeleton or spinner is displayed (not blank page)
  AND loading state resolves to actual content

GIVEN any API request fails (network error, 500)
 WHEN component receives error
 THEN error message is displayed with a "Retry" button
  AND page does NOT crash to white screen

GIVEN a list page with zero items (no servers/credentials/etc.)
 WHEN page renders
 THEN empty state is shown with descriptive text and CTA button (e.g. "Add your first server")

GIVEN any destructive action (delete server, delete credential)
 WHEN user clicks delete button
 THEN confirm dialog appears
  WHEN clicking Cancel
  THEN nothing happens
  WHEN clicking Confirm
  THEN item is deleted, toast shows success

GIVEN a successful create/update/delete action
 WHEN action completes
 THEN toast notification appears (not an alert dialog)
  AND toast auto-dismisses after 3-5 seconds
```

### Phase 5 Rollback
- `docker compose down -v` (wipes data)
- Revert to any previous phase's commit

---

## Cross-Phase Quality Gates

These apply after EVERY phase:

```bash
# Backend
cd backend
go vet ./...
golangci-lint run ./...
go test ./... -count=1

# Frontend
cd frontend
biome check ./src
tsc --noEmit
vitest run
```

### Commit Strategy

Each phase produces one or more logical commits:

```
feat: phase 1 - project scaffolding, database, auth
feat: phase 2 - server CRUD, credential encryption
feat: phase 3 - SSH command execution, interactive terminal
feat: phase 4 - monitoring agent, metrics collection, dashboard
feat: phase 5 - docker compose deployment, static serving, polish
```

### Testing Strategy

| Layer | Phase 1 | Phase 2 | Phase 3 | Phase 4 | Phase 5 |
|-------|:---:|:---:|:---:|:---:|:---:|
| `pkg/crypto/` | — | ✅ unit | — | — | — |
| `pkg/sshpool/` | — | — | ✅ integration | — | — |
| `pkg/token/` | ✅ unit | — | — | — | — |
| `repository/` | ✅ unit | ✅ unit | — | ✅ unit | — |
| `service/` | ✅ unit | ✅ unit | ✅ integration | ✅ unit | — |
| `handler/` | — | — | ✅ integration | — | — |
| Frontend hooks | ✅ unit | ✅ unit | ✅ unit | ✅ unit | — |
| Frontend E2E | — | — | — | — | ✅ Playwright |

---

## Risk Register

| Risk | Likelihood | Impact | Mitigation |
|------|:---:|:---:|------------|
| SSH host key verification skipped — MITM possible | Low | Medium | Documented. Add known_hosts in post-MVP. |
| Agent binary incompatible with old Linux kernels | Low | High | Build with `CGO_ENABLED=0`, target `linux/amd64`. Test on Ubuntu 20.04+. |
| TimescaleDB extension not loaded | Medium | High | Migration 000005 checks `CREATE EXTENSION IF NOT EXISTS`. Hub startup checks. |
| WebSocket connection drops on proxy timeout | Medium | Medium | Terminal hook implements auto-reconnect. SSH session survives brief WS drops. |
| JWT in localStorage stolen via XSS | Low | Medium | Acceptable for internal tool. Post-MVP: httpOnly cookies. |
| Credential master key lost | Low | Critical | Document in README: back up `VPSMANAGER_MASTER_KEY`. No recovery possible. |
| Concurrent SSH sessions exhaust server limits | Low | Medium | Pool limits concurrent connections per server (default 3). Configurable. |
