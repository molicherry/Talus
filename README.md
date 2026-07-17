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
- **API Keys** — Create and manage API keys for programmatic access.
- **Service Proxy** — Register external services (Grafana, Portainer, etc.) with encrypted credentials. Proxy API requests through Talus with credential injection and placeholder substitution.
- **i18n** — English and 中文 interface with light/dark/system theme.
- **Docker Compose** — One command to start: `docker compose up`.

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

### 2. Configure

```bash
cp .env.example .env
```

Edit `.env` and set:

```env
DB_PASSWORD=<your-postgres-password>
VPSMANAGER_MASTER_KEY=<openssl rand -hex 32>
JWT_SECRET=<openssl rand -hex 32>
```

### 3. Start

```bash
docker compose up -d
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

## Production Deployment

Pre-built images are published to [GHCR](https://github.com/molicherry/Talus/pkgs/container/talus) on every version tag (`v*`).

```bash
# 1. Login to GitHub Container Registry
docker login ghcr.io -u <your-github-username> -p <personal-access-token>

# 2. Set secrets
export DB_PASSWORD=<your-password>
export VPSMANAGER_MASTER_KEY=$(openssl rand -hex 32)
export JWT_SECRET=$(openssl rand -hex 32)

# 3. Deploy
docker compose up -d
```

**Upgrade to a new version:**

```bash
docker compose pull
docker compose up -d
```

### Local Build

If you prefer to build from source instead of using pre-built GHCR images, add a `build:` block to `docker-compose.yml` under the `hub` service:

```yaml
hub:
    build:
      context: .
      dockerfile: backend/Dockerfile
    # image: ghcr.io/molicherry/talus:latest  # comment out the image line
```

Then run:

```bash
docker compose build
docker compose up -d
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

An [OpenCode skill](skills/talus/SKILL.md) is included in the repository. AI assistants that load this skill can manage servers, execute commands, query metrics, relay requests to registered services, and create scoped API keys through Talus's REST API — no manual endpoint lookup needed.

```bash
# In any AI session with the Talus repository open:
"List all servers via Talus API"
"Check CPU metrics on server web-01"
"Relay a request to the Grafana service"
"Create a read-only API key for monitoring"
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

- SSH private keys and passwords are encrypted with **AES-256-GCM** before storage, using an Argon2id-derived key from `VPSMANAGER_MASTER_KEY`.
- Credentials are **never returned** in API responses.
- Target servers require **no additional open ports** — monitoring data flows over the existing SSH channel.
- The monitoring agent is **ephemeral**: deployed on-demand, collects metrics, and exits. No persistent binary or daemon remains.
- SSH host keys are verified using **TOFU (Trust On First Use)**: the key presented on first connection is recorded and verified on all subsequent connections, preventing MITM attacks.

## License

[MIT](LICENSE)
