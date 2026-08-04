# Talus

[中文文档](docs/README.zh-CN.md)

> The bronze guardian of your server fleet. Monitor, command, and connect — all from one place.

**Talus** is a self-hosted, single-user platform for managing a handful of Linux servers. No agents to install, no extra ports to open — just SSH and a web dashboard.

## Features

- **Server Inventory** — Register your VPS and bare-metal hosts by name, IP, and SSH port.
- **Credential Vault** — Store SSH passwords and private keys encrypted with AES-256-GCM at rest.
- **Remote Shell** — Execute commands on any server and see stdout, stderr, and exit codes.
- **Interactive Terminal** — Open a full PTY session in your browser (xterm.js, resize-aware, WebSocket-backed).
- **Live Monitoring** — CPU, memory, disk, load, swap, network, and disk I/O charts with 1h / 6h / 24h / 7d time ranges. Ephemeral agent deployed on-demand over SSH.
- **API Keys** — Create scoped API keys with per-server access control. Two-dimensional permissions (what actions × which servers). Keys are encrypted at rest and can be copied on demand. Rate-limited reveal with audit logging.
- **Service Proxy** — Register external services (Grafana, Portainer, etc.) with encrypted credentials. Proxy API requests through Talus with credential injection and placeholder substitution. Edit forms load existing credentials with show/hide toggle and copy support.

## Architecture

```
 Browser                  Talus Hub                    Target Servers
┌──────────┐    HTTP/WS    ┌──────────────┐    SSH     ┌──────────┐
│  React   │ ◄──────────► │  Go backend  │ ◄────────► │  Linux   │
│  SPA     │              │  (chi, GORM) │            │  servers │
└──────────┘              │              │            └──────────┘
                          │  PostgreSQL  │
                          │  + Timescale │
                          └──────────────┘
```

- **Hub-and-spoke**: The Hub connects out to your servers via SSH. Servers never call back.
- **Ephemeral agent**: Monitoring data is collected by a statically-linked Go binary that the Hub deploys and runs over SSH — no daemon, no open ports, no leftovers.
- **Credentials**: Encrypted at rest with a master key you control. Never returned in API responses.

## Quick Start

### Prerequisites

- Docker & Docker Compose
- Linux servers with SSH access (Debian/Ubuntu recommended)

### 1. Clone

```bash
git clone https://github.com/molicherry/Talus.git
cd Talus
```

### 2. Configure (optional)

All secrets have development defaults so the stack starts out of the box. For
anything beyond local testing, copy `.env.example` to `.env` and set real
secrets:

```env
DB_PASSWORD=<your-postgres-password>
VPSMANAGER_MASTER_KEY=<openssl rand -hex 32>
JWT_SECRET=<openssl rand -hex 32>
```

### 3. Start (build from source)

The repository `docker-compose.yml` builds Talus from source:

```bash
docker compose up -d --build
```

The dashboard is at **http://localhost:8080**.

On first login, enter any username and password — the first login automatically creates the admin account.

### 4. Add a Server

1. Go to **Servers** → **Add Server**
2. Fill in name, host (IP), SSH port, and description
3. Go to **Credentials** → **Add Credential** and attach a password or private key
4. Your server is now live — execute commands, open a terminal, or view metrics.

### 5. Proxy a Service

1. Go to **Services** → **Add Service**
2. Fill in name, display name, base URL (e.g. `http://localhost:3000`), and credentials (key-value pairs)
3. Optionally assign the service to a server for SSH-tunneled access
4. Use the relay API to proxy requests through Talus — credentials are injected automatically, `{{key}}` placeholders are substituted

## Production Deployment (GHCR images)

Pre-built images are published to [GHCR](https://github.com/molicherry/Talus/pkgs/container/talus)
on every version tag (`v*`) by CI. To deploy without compiling, save the
compose file below as `docker-compose.prod.yml`:

```yaml
# docker-compose.prod.yml — deploy Talus from pre-built GHCR images
services:
  db:
    image: timescale/timescaledb:latest-pg16
    environment:
      POSTGRES_USER: vpsmanager
      POSTGRES_PASSWORD: ${DB_PASSWORD:?set DB_PASSWORD in .env}
      POSTGRES_DB: vpsmanager
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U vpsmanager"]
      interval: 5s
      timeout: 5s
      retries: 5
    restart: unless-stopped

  hub:
    image: ghcr.io/molicherry/talus:${TALUS_VERSION:-latest}
    ports:
      - "${PORT:-8080}:8080"
    environment:
      DATABASE_URL: postgres://vpsmanager:${DB_PASSWORD:?set DB_PASSWORD in .env}@db:5432/vpsmanager?sslmode=disable
      VPSMANAGER_MASTER_KEY: ${VPSMANAGER_MASTER_KEY:?set VPSMANAGER_MASTER_KEY in .env}
      JWT_SECRET: ${JWT_SECRET:?set JWT_SECRET in .env}
      PORT: 8080
      LOG_LEVEL: ${LOG_LEVEL:-info}
      MONITOR_INTERVAL: ${MONITOR_INTERVAL:-60}
      SSH_TIMEOUT: ${SSH_TIMEOUT:-10}
      EXEC_TIMEOUT: ${EXEC_TIMEOUT:-30}
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

volumes:
  pgdata:
```

Deploy:

```bash
cp .env.example .env   # then fill in DB_PASSWORD / VPSMANAGER_MASTER_KEY / JWT_SECRET
docker compose -f docker-compose.prod.yml up -d
```

**Upgrade to a new version:**

```bash
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Backend | Go, [chi](https://github.com/go-chi/chi), [GORM](https://gorm.io), [gorilla/websocket](https://github.com/gorilla/websocket), [golang-jwt](https://github.com/golang-jwt/jwt) |
| Frontend | React 19, TypeScript, [Vite](https://vite.dev), [Tailwind CSS](https://tailwindcss.com), [Tremor](https://tremor.so), [xterm.js](https://xtermjs.org) |
| Database | PostgreSQL 16 + [TimescaleDB](https://www.timescale.com) |
| Monitoring Agent | Go + [gopsutil](https://github.com/shirou/gopsutil) |
| Deployment | Docker Compose |

## AI Integration

Talus ships with a portable [agent skill](skills/talus/SKILL.md) so any AI
coding assistant (OpenCode, Claude Code, Cursor, …) can operate Talus through
its REST API: list servers, run commands, read metrics, relay requests to
registered services, and add/update servers.

### Prerequisites

- A running Talus instance (see Deployment above)
- An API key — create one in **Talus Web UI → API Keys** (default scopes cover
  read/exec/metrics; `servers:write` and `services:relay` are opt-in)

### Install

Copy the skill into your AI tool's skills directory (the skill is portable
and does not need to live inside this repository):

```bash
# OpenCode
mkdir -p ~/.config/opencode/skills && cp -r skills/talus ~/.config/opencode/skills/
# Claude Code
mkdir -p ~/.claude/skills && cp -r skills/talus ~/.claude/skills/
```

Point the assistant at your instance:

```bash
export TALUS_URL=https://your-talus.example.com   # defaults to http://localhost:8080
export TALUS_API_KEY=<your-api-key>
```

### Verify

In a new AI session, ask *"List all servers via Talus"* — a list of servers
(or an empty array) means the skill is wired up.

### Per-service usage guides (AI)

Every registered service can carry its own **usage guide** (`usage_guide`, markdown) —
written in the **Talus Web UI → Services → Add/Edit Service → Usage Guide** field. It is
the per-service "skill" that tells an AI how to use that service (endpoints, auth
headers, request examples) without hard-coding any service specifics into the shared
skill. The AI reads it on demand before relaying:

```
GET /api/v1/services          → directory (name, description, guide excerpt)
GET /api/v1/services/{id}     → full usage_guide
POST /api/v1/services/{id}/relay → follow the guide to build the request
```

Optional: an [ai-integration](ai-integration/) plugin (OpenCode / pi / Claude Code /
Codex) can inject the service directory into every turn, so the AI always sees what is
available without asking. Cursor and other platforms rely on the skill's discovery
rules instead — the capability is complete either way.

### Example prompts

```bash
"List all servers via Talus API"
"Check CPU metrics on server web-01"
"Run 'docker ps' on prod-db"
"Relay a request to the Grafana service"
```

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_PASSWORD` | *(required)* | PostgreSQL password |
| `VPSMANAGER_MASTER_KEY` | *(required)* | 64-char hex key for SSH credential encryption |
| `JWT_SECRET` | *(required)* | JWT signing secret |
| `PORT` | `8080` | HTTP server port |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `MONITOR_INTERVAL` | `60` | Metrics collection interval (seconds) |
| `SSH_TIMEOUT` | `10` | SSH connection timeout (seconds) |
| `EXEC_TIMEOUT` | `30` | Command execution timeout (seconds) |

## Security

- SSH private keys and passwords, service credentials, and API key raw values are encrypted with **AES-256-GCM** at rest, using an Argon2id-derived key from `VPSMANAGER_MASTER_KEY`.
- Credential and API key values are **never returned** in list API responses (`json:"-"`). Dedicated JWT-only reveal endpoints decrypt on-demand with **audit logging** (slog structured JSON) and **rate limiting** (5 req/min per user).
- Target servers require **no additional open ports** — monitoring data flows over the existing SSH channel.
- The monitoring agent is **ephemeral**: deployed on-demand, collects metrics, and exits. No persistent binary or daemon remains.
- SSH host keys are verified using **TOFU (Trust On First Use)**: the key presented on first connection is recorded and verified on all subsequent connections, preventing MITM attacks.

## License

[MIT](LICENSE)
