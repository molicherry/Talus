# MVP Core Platform — Technical Design

> Architecture, contracts, data flow, and tradeoffs for the VPS management platform.

---

## 1. Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                        Operator Browser                          │
│  ┌──────────────┐  ┌───────────┐  ┌───────────┐  ┌──────────┐  │
│  │  Login Page  │  │ Server CRUD│  │ Terminal  │  │Dashboard │  │
│  │  (auth/)     │  │ (servers/) │  │(terminal/)│  │(monitor/)│  │
│  └──────┬───────┘  └─────┬─────┘  └─────┬─────┘  └────┬─────┘  │
│         │                │              │              │        │
│         │   TanStack Query / xterm.js + WebSocket      │        │
│         └────────────────┬──────────────┴──────────────┘        │
│                          │  HTTP/1.1 + WSS                      │
└──────────────────────────┼──────────────────────────────────────┘
                           │
┌──────────────────────────┼──────────────────────────────────────┐
│                     Docker Network                               │
│  ┌───────────────────────┴─────────────────────────────────┐    │
│  │                    Hub Service (:8080)                    │    │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────┐ │    │
│  │  │   Auth   │  │  Server  │  │   SSH    │  │ Monitor │ │    │
│  │  │  Handler │  │  Handler │  │  Service │  │ Service │ │    │
│  │  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬────┘ │    │
│  │       │            │             │ ssh            │      │    │
│  │  ┌────┴────────────┴─────────────┴──────────┐     │      │    │
│  │  │           PostgreSQL 16 + TimescaleDB     │◄────┘      │    │
│  │  │  users │ servers │ credentials │ metrics  │            │    │
│  │  └───────────────────────────────────────────┘            │    │
│  └──────────────────────────────────────────────────────────┘    │
└──────────────────────────────────────────────────────────────────┘
                           │ SSH (port 22)
          ┌────────────────┼────────────────┐
          ▼                ▼                ▼
     ┌─────────┐     ┌─────────┐     ┌─────────┐
     │Server 1 │     │Server 2 │     │Server N │
     │(Debian) │     │(Ubuntu) │     │(Debian) │
     │         │     │         │     │         │
     │ agent   │     │ agent   │     │ agent   │
     │ (on-demand│   │(on-demand│   │(on-demand│
     │  exec)  │     │  exec)  │     │  exec)  │
     └─────────┘     └─────────┘     └─────────┘
```

**Key architectural decisions**:

- **Hub is the single control plane**: All SSH connections originate from Hub. Target servers never call back to Hub.
- **Agent is ephemeral**: Statically-linked Go binary, deployed via SSH exec on demand for monitoring. No daemon, no open ports, no persistent install on targets.
- **No direct browser→target**: All traffic routes through Hub. Browser speaks HTTP/WS to Hub; Hub speaks SSH to targets.

---

## 2. Data Flow

### 2.1 Command Execution Flow

```
[Browser]          [Hub Service]           [Target Server]
   │                    │                       │
   │ POST /exec        │                       │
   │ {cmd:"uptime"}    │                       │
   │──────────────────►│                       │
   │                    │ 1. Validate JWT       │
   │                    │ 2. Lookup server      │
   │                    │ 3. Decrypt credential │
   │                    │ 4. Get SSH conn       │
   │                    │    from pool          │
   │                    │                       │
   │                    │ SSH: exec "uptime"    │
   │                    │──────────────────────►│
   │                    │                       │ run
   │                    │    stdout + exit      │ uptime
   │                    │◄──────────────────────│
   │                    │                       │
   │                    │ 5. Return conn ➜ pool │
   │                    │ 6. Write audit log    │
   │                    │                       │
   │  200 {stdout,     │                       │
   │  stderr, exit}    │                       │
   │◄──────────────────│                       │
```

### 2.2 Terminal Flow

```
[Browser xterm.js]     [Hub Service]           [Target Server]
   │                       │                       │
   │ WSS /terminal/{id}   │                       │
   │──────────────────────►│                       │
   │                       │ 1. Auth (token param) │
   │                       │ 2. Dial SSH           │
   │                       │ 3. Request PTY        │
   │                       │──────────────────────►│
   │                       │    pty allocated      │
   │                       │◄──────────────────────│
   │   {type:"connected"} │                       │
   │◄──────────────────────│                       │
   │                       │                       │
   │   {type:"input",     │                       │
   │    data:"ls\n"}      │                       │
   │──────────────────────►│  SSH: stdin write     │
   │                       │──────────────────────►│
   │                       │    stdout             │
   │                       │◄──────────────────────│
   │   {type:"output",    │                       │
   │    data:"file1..."}  │                       │
   │◄──────────────────────│                       │
   │                       │                       │
   │   {type:"resize",    │                       │
   │    cols:120,rows:40} │                       │
   │──────────────────────►│  SSH: window-change   │
   │                       │──────────────────────►│
```

### 2.3 Monitoring Flow

```
[Hub Monitor Service]          [Target Server]          [TimescaleDB]
   │                               │                       │
   │ tick: every 60s              │                       │
   │                               │                       │
   │ SSH exec: agent binary       │                       │
   │──────────────────────────────►│                       │
   │     agent runs gopsutil      │                       │
   │     outputs JSON to stdout   │                       │
   │◄──────────────────────────────│                       │
   │                               │                       │
   │ Parse JSON                    │                       │
   │                               │                       │
   │ INSERT metrics ────────────────────────────────────►│
   │                               │                       │
   │ (periodic) rollup 1m→10m    │                       │
   │────────────────────────────────────────────────────►│
```

---

## 3. Component Design

### 3.1 Hub Service (`cmd/server`)

**Lifecycle**:
1. `main.go` loads config from environment variables
2. Opens database connection pool (pgx via GORM)
3. Runs pending migrations (via golang-migrate, one-shot)
4. Constructs dependency tree: repos → services → handlers
5. Registers middleware stack + routes on Chi router
6. Starts HTTP server (graceful shutdown on SIGTERM)

**Dependency injection graph**:

```
config.Config
    │
    ├──► *gorm.DB (PostgreSQL)
    │       │
    │       ├──► repository.UserRepo
    │       ├──► repository.ServerRepo
    │       ├──► repository.CredentialRepo
    │       ├──► repository.AuditRepo
    │       └──► repository.MetricRepo
    │
    ├──► *crypto.MasterKey (derived from VPSMANAGER_MASTER_KEY)
    │       │
    │       └──► service.CredentialService
    │
    ├──► *sshpool.Pool
    │       │
    │       ├──► service.SSHService
    │       └──► service.TerminalService
    │
    └──► *token.JWTService
            │
            └──► service.AuthService

Services:
    service.AuthService(userRepo, jwtSvc)
    service.ServerService(serverRepo, auditSvc)
    service.CredentialService(credRepo, crypto)
    service.SSHService(sshPool, serverRepo, credentialSvc, auditSvc)
    service.TerminalService(sshPool, serverRepo, credentialSvc)
    service.MonitorService(sshPool, metricRepo, serverRepo, credentialSvc)
    service.AuditService(auditRepo)

Handlers (thin, delegate to services):
    handler.AuthHandler(authSvc)
    handler.ServerHandler(serverSvc)
    handler.CredentialHandler(credSvc)
    handler.ExecHandler(sshSvc)
    handler.TerminalHandler(terminalSvc)
    handler.MetricsHandler(monitorSvc)
```

**Middleware stack** (order matters):

```
Chi Router
├── middleware.RequestID       (inject X-Request-ID)
├── middleware.Logger          (slog JSON request logging)
├── middleware.Recoverer       (panic → 500)
├── middleware.CORS            (permissive for Docker Compose dev)
├── middleware.RateLimit       (100 req/min per IP, token bucket)
├── middleware.Auth            (JWT validation, injects user into ctx)
│   ├── Public routes: POST /auth/login
│   └── Protected routes: everything else
└── handlers...
```

### 3.2 Monitoring Agent (`cmd/agent`)

**Design**: Single statically-linked Go binary (`CGO_ENABLED=0`). Zero dependencies at runtime. No configuration files — all config via CLI flags.

```
agent [flags]
  --cpu           collect CPU usage (default: true)
  --memory        collect memory usage (default: true)
  --disk          collect disk usage (default: true)
  --format json   output format (json, cbor)
  --pretty        human-readable JSON output
```

**Output JSON schema**:

```json
{
  "timestamp": "2026-07-03T14:30:00Z",
  "hostname": "web-01",
  "cpu": {
    "percent": 23.5,
    "cores": 4,
    "load_1": 0.45,
    "load_5": 0.62,
    "load_15": 0.58
  },
  "memory": {
    "used_bytes": 4294967296,
    "total_bytes": 17179869184,
    "percent": 25.0
  },
  "disks": [
    {
      "mount": "/",
      "device": "/dev/sda1",
      "used_bytes": 32212254720,
      "total_bytes": 107374182400,
      "percent": 30.0
    },
    {
      "mount": "/data",
      "device": "/dev/sdb1",
      "used_bytes": 536870912000,
      "total_bytes": 1073741824000,
      "percent": 50.0
    }
  ]
}
```

**Deployment**: Hub copies agent binary to target server via SSH when first needed:

```
# Hub deploys agent on first monitoring cycle for a server:
1. scp agent binary → /tmp/vpsmanager-agent (if not present or version mismatch)
2. chmod +x /tmp/vpsmanager-agent
3. ssh exec: /tmp/vpsmanager-agent --format json
4. Parse stdout JSON → INSERT metrics
```

Binary version check: Hub computes SHA256 of its bundled agent binary before scp. On subsequent cycles, it compares `sha256sum /tmp/vpsmanager-agent` before deciding to re-upload.

**Why ephemeral, not a daemon**:
- Zero install footprint on target servers
- No long-running process to manage, monitor, or restart
- No open ports (reuses existing SSH)
- Agent failure is self-healing (Hub re-execs on next cycle)
- Binary size <10MB static, easily scp'd

### 3.3 Frontend SPA

**Routing** (React Router v7):

| Route | Component | Auth Required |
|-------|-----------|:---:|
| `/login` | `features/auth/LoginPage` | No |
| `/` | `features/dashboard/DashboardPage` | Yes |
| `/servers` | `features/servers/ServerListPage` | Yes |
| `/servers/new` | `features/servers/ServerFormPage` | Yes |
| `/servers/:id` | `features/servers/ServerDetailPage` | Yes |
| `/servers/:id/terminal` | `features/terminal/TerminalPage` | Yes |
| `/servers/:id/monitoring` | `features/monitoring/MonitoringPage` | Yes |
| `/servers/:id/exec` | `features/servers/ExecPage` | Yes |
| `/credentials` | `features/credentials/CredentialListPage` | Yes |
| `*` | 404 Not Found | — |

**Auth guard**: `app/router.tsx` wraps protected routes in `<ProtectedRoute>` which checks `useAuth().isAuthenticated` and redirects to `/login` if false.

**Layout**: All authenticated routes share `<MainLayout>` (sidebar + header + content area).

---

## 4. API Contract

### 4.1 REST Endpoints

| Method | Path | Request Body | Response | Notes |
|--------|------|-------------|----------|-------|
| `POST` | `/api/v1/auth/login` | `{username, password}` | `{token}` | First user auto-created if none exist |
| `GET` | `/api/v1/servers` | — | `{data: Server[]}` | List all servers for current user |
| `POST` | `/api/v1/servers` | `{name, host, port?, description?}` | `{data: Server}` | Create server |
| `GET` | `/api/v1/servers/{id}` | — | `{data: Server}` | Get server detail + status |
| `PUT` | `/api/v1/servers/{id}` | `{name?, host?, port?, description?}` | `{data: Server}` | Update server |
| `DELETE` | `/api/v1/servers/{id}` | — | `{data: null}` | Soft delete (GORM DeletedAt) |
| `POST` | `/api/v1/servers/{id}/exec` | `{command, timeout?}` | `{stdout, stderr, exit_code, duration_ms}` | Execute command on server |
| `GET` | `/api/v1/servers/{id}/status` | — | `{status, last_seen}` | Quick connectivity check |
| `WS` | `/api/v1/servers/{id}/terminal` | — | Terminal messages | WebSocket upgrade, PTY session |
| `POST` | `/api/v1/credentials` | `{server_id, auth_type, username, password? \| private_key?}` | `{data: SSHCredential}` | Store credential (encrypted) |
| `GET` | `/api/v1/credentials` | — | `{data: SSHCredential[]}` | List credentials (masked) |
| `DELETE` | `/api/v1/credentials/{id}` | — | `{data: null}` | Delete credential |
| `GET` | `/api/v1/metrics/{serverId}` | `?from=&to=&interval=` | `{data: MetricPoint[]}` | Query monitoring time series |

### 4.2 WebSocket Protocol (Terminal)

**Connection**: `wss://host/api/v1/servers/{id}/terminal?token=<jwt>`

Auth via query parameter because WebSocket upgrade doesn't support custom headers in all browsers. Hub validates JWT on upgrade, rejects with 401 if invalid.

**Message format** (JSON, both directions):

```typescript
// Client → Hub
type ClientMessage =
  | { type: "input"; data: string }     // stdin bytes
  | { type: "resize"; cols: number; rows: number };

// Hub → Client
type ServerMessage =
  | { type: "connected"; session_id: string }
  | { type: "output"; data: string }    // stdout bytes
  | { type: "error"; message: string }
  | { type: "disconnected"; reason?: string };
```

**Lifecycle**:
1. Client opens WebSocket → Hub auths JWT → Hub dials SSH → Hub requests PTY → Hub sends `{type:"connected"}`
2. Client sends `{type:"input"}` → Hub writes to SSH stdin
3. Hub reads SSH stdout → sends `{type:"output"}` to client
4. Client sends `{type:"resize"}` → Hub sends SSH `window-change` request
5. Either side closes → Hub cleans up SSH session, sends `{type:"disconnected"}` if Hub initiates close

**Concurrency**: Hub uses two goroutines per terminal session:
- `readFromSSH`: reads from SSH stdout → writes to WebSocket
- `readFromWS`: reads from WebSocket → writes to SSH stdin

Both select on a shared `ctx.Done()` channel for coordinated shutdown.

---

## 5. SSH Connection Pool Design

### 5.1 Pool Structure

```go
// internal/pkg/sshpool/pool.go
type Pool struct {
    mu       sync.Mutex
    conns    map[uint]*connEntry   // key: server ID
    maxIdle  time.Duration         // idle timeout before eviction (default: 5min)
    maxConns int                   // max connections per server (default: 3)
}

type connEntry struct {
    client    *ssh.Client
    lastUsed  time.Time
    semaphore chan struct{}        // limits concurrent sessions
}
```

### 5.2 Connection Lifecycle

```
Get(serverID) ──► Check pool
                    ├── Have idle conn? ──► return conn (update lastUsed)
                    └── No conn?
                         ├── At maxConns limit? ──► wait for semaphore
                         └── Under limit? ──► dial SSH → store → return

Release(serverID, conn) ──► mark idle (update lastUsed)
                              Release semaphore slot

EvictLoop (background goroutine, every 30s):
    For each conn where time.Since(lastUsed) > maxIdle:
        Close SSH connection, remove from pool
```

### 5.3 SSH Connection Configuration

Derived from `SSHCredential` at dial time:

```go
func dialSSH(host string, port int, cred *model.SSHCredential, decryptedAuth interface{}) (*ssh.Client, error) {
    config := &ssh.ClientConfig{
        User:            cred.Username,
        Auth:            []ssh.AuthMethod{decryptedAuth}, // ssh.Password() or ssh.PublicKeys()
        HostKeyCallback: ssh.InsecureIgnoreHostKey(),     // MVP: skip host key verification
        Timeout:         10 * time.Second,
    }
    return ssh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), config)
}
```

**Host key verification (MVP decision)**: Skipped in MVP. Reason: single-operator managing known servers. Known host verification adds complexity (key rotation, first-use trust) without meaningful security gain for single-user tool. Documented as a future enhancement.

### 5.4 Session Reuse

For command execution (one-shot), each `exec` is a new `ssh.Session` from the same `ssh.Client`:

```go
func (s *SSHService) Exec(ctx context.Context, serverID uint, command string, timeout time.Duration) (*ExecResult, error) {
    client, err := s.pool.Get(serverID)
    defer s.pool.Release(serverID, client)

    session, err := client.NewSession()
    defer session.Close()

    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    // Goroutine to kill session on timeout
    go func() {
        <-ctx.Done()
        if ctx.Err() == context.DeadlineExceeded {
            session.Signal(ssh.SIGKILL)
        }
    }()

    var stdout, stderr bytes.Buffer
    session.Stdout = &stdout
    session.Stderr = &stderr

    err = session.Run(command)
    exitCode := 0
    if exitErr, ok := err.(*ssh.ExitError); ok {
        exitCode = exitErr.ExitStatus()
        err = nil // not a transport error
    }

    return &ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, err
}
```

---

## 6. Encryption Architecture

### 6.1 Master Key Derivation

```
VPSMANAGER_MASTER_KEY (env, 32+ bytes, hex-encoded)
         │
         ▼
   Argon2id (salt: fixed project salt, memory: 64MB, time: 1, threads: 4)
         │
         ▼
   32-byte AES-256 key
```

Derivation happens once at startup in `crypto.NewMasterKey(hexKey string)`. The derived key lives in memory only and is passed to `CredentialService` via constructor injection.

### 6.2 Credential Encryption

```go
// internal/pkg/crypto/aes.go
func Encrypt(plaintext []byte, key []byte) ([]byte, error) {
    // 1. Generate random 12-byte nonce
    // 2. AES-256-GCM seal: nonce || ciphertext || tag
    // 3. Return base64-encoded result for DB storage
}

func Decrypt(ciphertext []byte, key []byte) ([]byte, error) {
    // 1. Base64 decode
    // 2. Extract nonce (first 12 bytes)
    // 3. AES-256-GCM open
    // 4. Return plaintext (never logged, never stored)
}
```

**Encryption flow on credential save**:
1. Handler receives `POST /credentials` with `{password: "secret123"}`
2. Handler → `CredentialService.Save(cred)`
3. Service calls `crypto.Encrypt([]byte("secret123"), masterKey)`
4. Stores `encrypted_password` = base64(nonce+ciphertext+tag) in DB
5. Returns credential with `encrypted_password` field never populated (JSON `-`)

**Decryption flow on SSH connect**:
1. `SSHService` calls `CredentialService.GetDecrypted(serverID)`
2. Service reads encrypted field from DB
3. Service calls `crypto.Decrypt(encrypted, masterKey)`
4. Returns plaintext password or private key in memory
5. `SSHService` uses it to construct `ssh.ClientConfig`
6. Plaintext is garbage-collected after dial; never persisted, never logged

### 6.3 Key Rotation

Not in MVP. If `VPSMANAGER_MASTER_KEY` changes, all existing encrypted credentials become unreadable. Rotation would require a re-encryption migration that reads with old key, writes with new key — deferred.

---

## 7. Database Schema

(Full SQL in `backend/migrations/` — see `backend/database-guidelines.md` for complete DDL.)

**Migration files**:

```
migrations/
├── 000001_create_users.up.sql        / .down.sql
├── 000002_create_servers.up.sql      / .down.sql
├── 000003_create_credentials.up.sql  / .down.sql
├── 000004_create_audit_logs.up.sql   / .down.sql
└── 000005_create_metrics.up.sql      / .down.sql
```

**Migration 000005 (TimescaleDB)** includes:
```sql
CREATE EXTENSION IF NOT EXISTS timescaledb;
SELECT create_hypertable('metrics', 'time', chunk_time_interval => INTERVAL '1 day');
CREATE INDEX idx_metrics_server_time ON metrics(server_id, time DESC);
```

**Rollup aggregation** (TimescaleDB continuous aggregates — deferred to Phase 4, may be done via materialized views or cron in MVP):

```sql
-- 1m → 10m rollup (MVP: query-time aggregation, no materialized views)
SELECT
    time_bucket('10 minutes', time) AS bucket,
    server_id,
    AVG(cpu_percent) AS cpu_percent,
    AVG(memory_percent) AS memory_percent,
    AVG(disk_percent) AS disk_percent
FROM metrics
WHERE server_id = $1 AND time BETWEEN $2 AND $3
GROUP BY bucket, server_id
ORDER BY bucket;
```

For MVP: query-time `time_bucket()` aggregation. Continuous aggregates deferred — complexity not justified for ≤15 servers with 60s intervals.

---

## 8. Monitoring Pipeline

### 8.1 Collection Schedule

```
Hub Monitor Service (background goroutine, started in main.go):
    ticker := time.NewTicker(cfg.MonitorInterval) // default: 60s

    for range ticker.C:
        servers, _ := serverRepo.FindAll(ctx)
        for _, srv := range servers:
            go collectMetrics(ctx, srv) // concurrent per server
```

**Concurrency control**: Each server gets its own goroutine per cycle. With ≤15 servers, no worker pool needed. If a server is unreachable (SSH timeout), log WARN and skip this cycle. Three consecutive failures → mark server status as "offline".

### 8.2 Agent Deployment & Execution

```go
func (s *MonitorService) collectMetrics(ctx context.Context, server model.Server) {
    // 1. Get SSH client from pool
    client, err := s.pool.Get(server.ID)
    if err != nil { /* WARN, skip */ }
    defer s.pool.Release(server.ID, client)

    // 2. Ensure agent binary exists on target (version check)
    if err := s.ensureAgent(client); err != nil { /* WARN, skip */ }

    // 3. Exec agent
    execResult, err := s.execAgent(ctx, client)
    if err != nil { /* WARN, skip */ }

    // 4. Parse JSON
    var metrics AgentOutput
    json.Unmarshal([]byte(execResult.Stdout), &metrics)

    // 5. Store
    s.metricRepo.Insert(ctx, server.ID, metrics)
}
```

**Error handling per server**: A single server's failure does not block collection for other servers. Each goroutine is independent.

### 8.3 Dashboard Query

Frontend `GET /api/v1/metrics/{serverId}?from=...&to=...&interval=...` calls `MetricRepo.Query` which does:

```sql
SELECT time_bucket($1::interval, time) AS bucket,
       AVG(cpu_percent) AS cpu_percent,
       AVG(memory_percent) AS memory_percent,
       AVG(disk_percent) AS disk_percent
FROM metrics
WHERE server_id = $2 AND time BETWEEN $3 AND $4
GROUP BY bucket
ORDER BY bucket;
```

`interval` parameter maps time range: 1h → 1m, 6h → 5m, 24h → 15m, 7d → 1h.

---

## 9. Terminal (PTY) Design

### 9.1 Hub-side Implementation

```go
// internal/service/terminal.go
func (s *TerminalService) StartSession(ctx context.Context, serverID uint, ws *websocket.Conn) error {
    // 1. Get SSH client from pool
    client, err := s.pool.Get(serverID)
    defer s.pool.Release(serverID, client) // release conn, but keep alive via session

    // 2. Open SSH session with PTY
    session, err := client.NewSession()
    defer session.Close()

    modes := ssh.TerminalModes{
        ssh.ECHO:          1,
        ssh.TTY_OP_ISPEED: 14400,
        ssh.TTY_OP_OSPEED: 14400,
    }
    session.RequestPty("xterm-256color", 80, 24, modes)

    // 3. Wire stdin/stdout
    stdinPipe, _ := session.StdinPipe()
    stdoutPipe, _ := session.StdoutPipe()

    // 4. Start shell
    session.Shell()

    // 5. Bidirectional relay
    ctx, cancel := context.WithCancel(ctx)
    defer cancel()

    go readFromSSH(ctx, stdoutPipe, ws)  // SSH stdout → WebSocket
    go readFromWS(ctx, ws, stdinPipe)    // WebSocket → SSH stdin

    <-ctx.Done()
    return nil
}
```

### 9.2 Frontend (xterm.js)

```typescript
// features/terminal/hooks/use-terminal.ts
export function useTerminal(serverId: number) {
  const termRef = useRef<Terminal | null>(null);
  const wsRef = useRef<WebSocket | null>(null);

  const connect = useCallback((terminalElement: HTMLElement) => {
    const term = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: "'JetBrains Mono', monospace",
      theme: { background: "#1a1b26" },
    });
    term.open(terminalElement);
    termRef.current = term;

    const token = getAuthToken();
    const ws = new WebSocket(`ws://localhost:8080/api/v1/servers/${serverId}/terminal?token=${token}`);

    ws.onmessage = (event) => {
      const msg = JSON.parse(event.data);
      if (msg.type === "output") term.write(msg.data);
      if (msg.type === "disconnected") term.write("\r\n[Disconnected]\r\n");
    };

    term.onData((data) => {
      ws.send(JSON.stringify({ type: "input", data }));
    });

    term.onResize(({ cols, rows }) => {
      ws.send(JSON.stringify({ type: "resize", cols, rows }));
    });

    wsRef.current = ws;
  }, [serverId]);

  const disconnect = useCallback(() => {
    wsRef.current?.close();
    termRef.current?.dispose();
  }, []);

  useEffect(() => () => disconnect(), [disconnect]);

  return { connect, disconnect };
}
```

---

## 10. Deployment Architecture

### 10.1 Docker Compose

```yaml
# docker-compose.yml
version: "3.9"
services:
  db:
    image: timescale/timescaledb:2.17-pg16
    environment:
      POSTGRES_USER: vpsmanager
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: vpsmanager
    volumes:
      - pgdata:/var/lib/postgresql/data
    ports:
      - "127.0.0.1:5432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U vpsmanager"]
      interval: 5s
      timeout: 5s
      retries: 5

  migrate:
    build:
      context: ./backend
      dockerfile: Dockerfile.migrate
    environment:
      DATABASE_URL: postgres://vpsmanager:${DB_PASSWORD}@db:5432/vpsmanager?sslmode=disable
    depends_on:
      db:
        condition: service_healthy

  hub:
    build:
      context: ./backend
      dockerfile: Dockerfile
    ports:
      - "8080:8080"
    environment:
      DATABASE_URL: postgres://vpsmanager:${DB_PASSWORD}@db:5432/vpsmanager?sslmode=disable
      VPSMANAGER_MASTER_KEY: ${VPSMANAGER_MASTER_KEY}
      JWT_SECRET: ${JWT_SECRET}
      PORT: "8080"
      LOG_FORMAT: json
      LOG_LEVEL: info
    depends_on:
      migrate:
        condition: service_completed_successfully
    volumes:
      - agent_binary:/agent  # agent binary for scp to targets

volumes:
  pgdata:
  agent_binary:
```

### 10.2 Hub Dockerfile

```dockerfile
# backend/Dockerfile (multi-stage)
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /hub ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /agent ./cmd/agent

FROM alpine:3.20
RUN apk add --no-cache openssh-client ca-certificates
COPY --from=builder /hub /usr/local/bin/hub
COPY --from=builder /agent /agent/vpsmanager-agent
COPY migrations/ /migrations/
EXPOSE 8080
ENTRYPOINT ["hub"]
```

### 10.3 Environment Variables

| Variable | Purpose | Default | Required |
|----------|---------|---------|:---:|
| `DATABASE_URL` | PostgreSQL connection string | — | Yes |
| `VPSMANAGER_MASTER_KEY` | 32+ byte hex key for credential encryption | — | Yes |
| `JWT_SECRET` | HMAC signing key for JWT | — | Yes |
| `PORT` | HTTP listen port | `8080` | No |
| `LOG_FORMAT` | `json` or `text` | `json` | No |
| `LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` | No |
| `MONITOR_INTERVAL` | Metrics collection interval (seconds) | `60` | No |
| `SSH_TIMEOUT` | SSH connection timeout (seconds) | `10` | No |
| `EXEC_TIMEOUT` | Default command execution timeout (seconds) | `30` | No |
| `SSH_MAX_IDLE` | SSH pool idle eviction (seconds) | `300` | No |
| `RATE_LIMIT` | Requests per minute per IP | `100` | No |

---

## 11. Tradeoffs & Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Host key verification | Skipped in MVP | Single operator, known servers. Add in future via `known_hosts` file. |
| JWT storage | localStorage | Simpler than httpOnly cookies for WebSocket auth (token in query param). Acceptable for single-user internal tool. |
| WebSocket auth | Query parameter `?token=` | WebSocket API doesn't support custom headers. Token is short-lived (configurable expiry). |
| No WebSocket auth via cookie | Not used | Cross-origin cookie complexity in Docker Compose dev. Query param is simpler. |
| Agent: ephemeral exec vs daemon | Ephemeral exec | Zero install, no ports, self-healing. Acceptable for ≤15 servers at 60s interval. |
| Continuous aggregates (TimescaleDB) | Deferred | Query-time `time_bucket()` sufficient for ≤15 servers. Materialized views add migration complexity. |
| Audit logs in MVP | Deferred | Single user, low value. Add when API key / multi-user feature ships. |
| No password reset | Out of scope | Single user. Seed script or first-run bootstrap handles account creation. |
| Soft deletes (GORM DeletedAt) | Yes | Recoverable. Servers/credentials can be undeleted. |
| API versioning (`/api/v1/`) | Yes | Path-based. Cheap insurance for future breaking changes. |
| gofumpt over gofmt | Yes | Stricter, opinionated. Biome equivalent for Go. |
| testcontainers for DB/SSH tests | Yes | Real PostgreSQL and SSH server in tests. No mocks for infrastructure. |

---

## 12. Security Model

### Threat Model (MVP)

**Trust boundary**: Hub ↔ Target Servers (SSH), Browser ↔ Hub (HTTPS/WSS).

**What we protect against**:
- Unauthorized access to Hub API (JWT auth)
- Credential exposure at rest (AES-256-GCM)
- Credential leakage in logs (redaction rules)
- Credential leakage in API responses (masked, never returned)

**What we don't protect against (MVP)**:
- Compromised Hub host (all in-memory keys accessible)
- MITM on SSH (no host key verification)
- XSS in browser (localStorage token theft — accepted for internal tool)
- Brute force login (rate limiting on auth endpoint, no account lockout)

### Future hardening (post-MVP):
- Host key verification with known_hosts
- httpOnly secure cookies for JWT
- API key scoping (server-level permissions)
- Audit log for all sensitive operations
- TLS for Hub (reverse proxy or built-in)
- Rate limiting per user, not just per IP

---

## 13. Open Questions / Future Decisions

1. **Agent binary distribution**: Bundle in Hub Docker image and SCP to targets (current plan), or have targets pull from a HTTP endpoint? SCP is simpler for MVP since SSH is already configured.
2. **Metrics retention**: How long to keep raw (1m) data? Default: 30 days raw, 1 year aggregated.
3. **Frontend static files serving**: Vite dev server for development. For production, Hub serves `frontend/dist/` as static files (single binary deployment) or separate nginx container. Decision deferred.
4. **Terminal recording/replay**: Out of scope for MVP. Future feature for audit/playback.
5. **Web terminal vs native terminal**: xterm.js in browser is the MVP. Future: desktop app with native terminal emulation.
