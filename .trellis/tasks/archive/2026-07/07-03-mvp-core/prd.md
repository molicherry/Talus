# MVP Core Platform

## Goal

Build the minimal viable VPS remote management platform: a single-user Go backend + React frontend that manages a handful of servers via SSH, executes commands, provides an interactive terminal, and collects monitoring metrics.

## Target User

Single operator managing 5–15 Linux servers. No multi-user, no role-based access.

---

## MVP Scope

### In Scope

| # | Feature | Priority |
|---|---------|:--------:|
| F1 | JWT-based user login | P0 |
| F2 | Server CRUD (name, host, port, description) | P0 |
| F3 | SSH credential management (password + private key, AES-256-GCM encrypted at rest) | P0 |
| F4 | Command execution — single-shot SSH exec, returns stdout/stderr + exit code | P0 |
| F5 | Interactive terminal — WebSocket PTY proxied through Hub SSH | P0 |
| F6 | Monitoring agent — per-server CPU, memory, disk metrics collected via Hub SSH pull | P0 |
| F7 | Monitoring dashboard — Tremor charts showing CPU/Memory/Disk over time | P0 |
| F8 | Docker Compose deployment (Hub + PostgreSQL/TimescaleDB) | P0 |

### Deferred (Out of Scope for MVP)

| Feature | Reason |
|---------|--------|
| Audit logs | Single user, low value |
| API Key + AI Skill (OpenCode integration) | No AI client in MVP |
| File upload/download | Existing tools (scp/sftp) sufficient |
| Multi-user / role-based access | Single operator |
| Alerting / notifications | Out of scope |

---

## Constraints

- **Scale**: ≤ 15 servers, 1 user
- **Platform**: Linux servers only (Debian/Ubuntu target)
- **Deployment**: Docker Compose, single-node
- **Monitoring interval**: 60s default, configurable
- **Agent binary**: Single statically-linked Go binary, deployed to target servers via SSH, no persistent installation required
- **Agent transport**: Reuse Hub SSH channel; Hub pulls metrics by exec'ing agent over SSH, agent writes gopsutil data to stdout, Hub parses and stores
- **No new ports on target servers**: Agent uses existing SSH channel only
- **Database**: PostgreSQL 16+ with TimescaleDB extension for metrics hypertables

---

## Functional Requirements

### F1: JWT Login

- User registers/creates first admin account on first run (or seed script)
- Login with username + password → JWT token
- Token passed as Bearer header for all API calls
- No password reset, no email verification in MVP

### F2: Server CRUD

- Add server: name, host/IP, SSH port (default 22), optional description
- Edit / delete server
- List servers with status indicator (online/offline/unknown)
- Server ownership scoped to the single user (no multi-tenancy)

### F3: SSH Credential Management

- Per server: one credential (password OR private key)
- `auth_type`: `password` or `private_key`
- Credentials encrypted with AES-256-GCM before DB write
- Master key derived from `VPSMANAGER_MASTER_KEY` environment variable (Argon2id)
- Credentials never returned in API responses (except masked fingerprint)
- UI: form with password field or key file upload, key fingerprint display

### F4: Command Execution

- POST `/api/v1/servers/{id}/exec` with `{ "command": "..." }`
- Response: `{ "stdout": "...", "stderr": "...", "exit_code": 0 }`
- Timeout: configurable (default 30s)
- Concurrent execution allowed on different servers
- Command history stored client-side only (no server persistence in MVP)
- UI: text input + output panel, with execution history per session

### F5: Interactive Terminal

- WebSocket endpoint: `/api/v1/servers/{id}/terminal`
- PTY allocated on target server, resize events forwarded
- Support: input, output, terminal resize
- Single terminal session per server (MVP)
- UI: xterm.js-based terminal emulator embedded in page

### F6: Monitoring Agent

- Hub SSH-exec's agent binary on target server every N seconds (default 60s)
- Agent binary: statically compiled Go using `gopsutil/v4`
- Agent reads CPU percent, memory (used/total), disk (used/total per mount point)
- Agent outputs JSON/CBOR to stdout, Hub parses and stores
- TimescaleDB hypertable for metrics with 1-day chunk interval
- Hierarchical aggregation: 1m raw → 10m → 1h → 1d rollups

### F7: Monitoring Dashboard

- Per-server monitoring page at `/servers/:id/monitoring`
- Charts: CPU usage (line/area), Memory usage (gauge + line), Disk usage (gauge)
- Time range selector: 1h, 6h, 24h, 7d
- Auto-refresh every 60s
- Tremor (Recharts wrapper) for charts

### F8: Docker Compose Deployment

- `docker-compose.yml` with:
  - `hub`: Go binary, exposes port for HTTP/WS
  - `db`: PostgreSQL 16 + TimescaleDB extension
  - `migrate`: one-shot container running golang-migrate
- Environment variables: `VPSMANAGER_MASTER_KEY`, `DATABASE_URL`, `JWT_SECRET`
- Data persistence via Docker volumes

---

## Non-Functional Requirements

- **Security**: SSH private keys never logged; credentials encrypted at rest; JWT with configurable expiry
- **Reliability**: SSH connection pool with reconnection; graceful degradation when agent unavailable
- **Performance**: Dashboard charts render within 2s for 7-day range; terminal latency acceptable for interactive use
- **Operability**: Single `docker compose up` to start; logs to stdout (JSON structured)

---

## Acceptance Criteria

### MVP is complete when a single operator can:

- [ ] AC1: Log in with username/password
- [ ] AC2: Add, edit, list, and delete target servers
- [ ] AC3: Store SSH password or private key per server (encrypted, never exposed in UI)
- [ ] AC4: Execute a shell command on any server and see output
- [ ] AC5: Open an interactive terminal session to any server
- [ ] AC6: View CPU, memory, and disk usage charts for any server over 1h/6h/24h/7d ranges
- [ ] AC7: Start the entire stack with `docker compose up`
