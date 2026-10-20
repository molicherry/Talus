# Talus

[中文文档](docs/README.zh-CN.md)

> The bronze guardian of your server fleet. Monitor, command, and connect — all from one place.

**Talus** is a self-hosted, single-user platform for managing Linux servers. SSH/SFTP installs a resident monitoring agent automatically; nodes report over outbound WSS/HTTPS without opening additional inbound ports.

## Features

- **Server Inventory** — Register your VPS and bare-metal hosts by name, IP, and SSH port.
- **SSH Connection Routes** — Choose direct, a single SSH jump host, or SOCKS5 per server. Commands, browser terminals, file operations and agent installation use the same route; resident monitoring uses a separate outbound connection.
- **Credential Vault** — Store SSH passwords and private keys encrypted with AES-256-GCM at rest.
- **Remote Shell** — Execute commands on any server and see stdout, stderr, and exit codes.
- **Interactive Terminal** — Open a full PTY session in your browser (xterm.js, resize-aware, WebSocket-backed).
- **Live Monitoring** — CPU, memory, disk, load, swap, network, and disk I/O charts with 15m / 1h / 6h / 24h / 7d / 30d time ranges, average/peak views, and accessible data tables. A resident agent collects every five seconds by default, buffers samples across outages, and reports over WSS with HTTPS fallback. Connection, collection and storage states are displayed separately.
- **API Keys** — Create scoped API keys with per-server access control. Two-dimensional permissions (what actions × which servers). Keys are encrypted at rest and can be copied on demand. Rate-limited reveal with audit logging.
- **SFTP Files** — Browse files alongside SSH, upload/download with resume, transfer folders, and edit UTF-8 text with version conflict checks. Scoped file APIs share SSH credentials, verified host keys and proxy routes.
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

- **Management and monitoring**: The Hub uses SSH for management and agent installation. Resident agents call back to `AGENT_HUB_URL` over WSS/HTTPS for monitoring.
- **Resident agent**: The verified binary runs as a systemd system/user service, with a private persistent outbox (24h / 64 MiB). Samples are removed only after transactional, deduplicated storage acknowledges them. A running systemd manager and `flock` are required; user services require lingering for the SSH account. Talus does not run sudo.
- **Credentials**: Encrypted at rest with a master key you control. Secret values are omitted from list responses; authenticated administrators can reveal them through dedicated, audited endpoints.

## Quick Start

### Prerequisites

- Docker & Docker Compose
- Linux servers with SSH access (Debian/Ubuntu recommended); the SSH `sftp` subsystem must be enabled for file transfers and initial monitoring-agent deployment or updates.

### 1. Clone

```bash
git clone https://github.com/molicherry/Talus.git
cd Talus
```

### 2. Configure

Copy `.env.example` to `.env` and set real secrets — the stack refuses to start
without them:

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

The dashboard is at **<http://localhost:8080>**.

On first login, enter any username and a password of at least 8 characters — the first login automatically creates the admin account.

### 4. Add a Server

1. Go to **Servers** → **Add Server**
2. Fill in name, host (IP), SSH port, and description
3. Go to **Credentials** → **Add Credential** and attach a password or private key
4. Your server is now live — execute commands, open a terminal, or view metrics.

For servers behind an IP allowlist, choose a **Connection mode** in the server
form. **SSH jump host** selects an existing direct server with SSH credentials;
its SSH daemon must permit TCP forwarding to the target. **SOCKS5** accepts a
proxy host and port, with optional username/password authentication. Keep the
target's real host, SSH port, and SSH credentials in the server fields. The
target must allow the jump host or proxy's actual outbound IP.

Save the server, then use **Test connection** on its edit page to check a fresh
SSH handshake. The complete test, including queue waits and configuration reads,
uses the smaller of `SSH_TIMEOUT` and 10 seconds. Target names are resolved by
the jump host or SOCKS5 proxy.
Proxy failures never fall back to a direct connection. Passwords are encrypted
and are not returned by ordinary server reads; leave an unchanged proxy's
password blank to retain it. Changing its address or username requires a new
password. Connection changes apply to new operations; reconnect open terminals
to use the new route.

Only a signed-in administrator can change routes or test saved connections.
API keys can execute permitted operations on their allowed targets through the
administrator's configured route. Stop older Hub versions before enabling
proxy routes: older binaries do not understand them and would connect directly.

### 5. Proxy a Service

1. Go to **Services** → **Add Service**
2. Fill in name, display name, base URL (e.g. `http://localhost:3000`), and credentials (key-value pairs)
3. Optionally assign the service to a server for management and authorization scope — the relay calls the service base URL directly from the Hub; there is **no** SSH tunnel
4. Use the relay API to proxy requests through Talus — credentials are injected automatically, `{{key}}` placeholders are substituted. An omitted `mode` is a normal 30s request; pass `"mode": "bounded_stream"` for an explicit up-to-300s stream (30s header wait, 30s no-progress idle limit; the mode is not forwarded upstream)

### 6. View Usage Logs

Administrators can open **Usage Logs** for filtered operation history and details. Logs cover resource changes, sensitive reveals, SSH exec, service relay and terminal sessions, without recording secrets, commands or stream content. API keys cannot query this history.

### 7. Manage Files over SFTP

Talus file management uses the configured SSH account and SSH port. It reuses encrypted credentials, explicit host-key confirmation, direct/SOCKS5/jump routing, connection revisions and pool quotas. The target must enable its SSH `sftp` subsystem. Internal file uploads and monitoring-agent deployment use the same supervised SFTP transport; the previous shell-based upload is replaced, with no automatic shell fallback. Agent deployment retains its pinned SSH connection through upload, SHA256 verification and atomic activation. Uploaded artifacts stay private (`0600`) until activation sets the executable permissions. No additional FTP server or target agent is required. These operations have the filesystem permissions of that SSH account; the API does not add a directory jail or `sudo` access.

The terminal, file panel and text editor follow the global light/dark/system theme. Theme changes preserve the SSH session, terminal output and editor draft.

Open **Files** in the SSH toolbar. Desktop uses a split pane; phones switch between the terminal and files without reconnecting the terminal. Browse by path, upload or download files, create directories, rename/move entries, and delete files or empty directories. Upload replacement requires confirmation. New files use mode `0600`; replacement preserves the existing permission bits. The owner of a replacement file is the SSH account creating the temporary file.

Uploads use 1 MiB chunks and a SHA256 checksum of the complete file. Cancel pauses a resumable upload. Re-select the same local file to continue; the backend supplies the actual remote offset. **Discard partial upload** removes its temporary file without deleting the destination. The current tab remembers the upload across page reloads; logout, session expiry or an account change clears its recovery information. Signed recovery tokens expire after 24 hours and bind the uploader identity, server, SSH route, destination, size and checksum. They remain usable after a Hub restart when `JWT_SECRET`, credentials and connection configuration remain unchanged. An expired token can still discard its own temporary file when the caller and SSH route match. Abandoned files whose route changed can be removed through normal file management by an account with delete permission.

Downloads use verified ranges. While the file panel stays open, a paused download can resume from the last complete 16 MiB chunk. Closing/reloading the panel releases those browser-held chunks; a new download then starts from the beginning. `If-Range` prevents mixing versions, and the browser verifies the completed download against its SHA256 ETag.

**Upload folder** preserves relative subdirectories and refuses to overwrite existing files automatically. Earlier successful files remain if a later file fails or the transfer is cancelled. Browser folder selection supplies files only, so empty local directories are not uploaded. **Download folder as ZIP** includes nested and empty remote directories, skips symbolic links/special files, and checks limits before starting the response.

**Edit text** supports UTF-8 text up to 1 MiB, preserves line endings and permission bits, and sends a content-hash version on save. A stale version returns `409 sftp_conflict`; the UI keeps the draft. Reloading or closing a dirty editor requires an explicit discard. Files are written through a same-directory temporary file. Because SFTP has no conditional rename, external writers can still race the final version check and rename; the version check is optimistic concurrency control rather than a filesystem-wide lock.

Transfer limits and lifecycle:

- 64 MiB per file and for the combined file contents in a directory ZIP/folder upload; ZIP metadata adds at most 2 MiB. Text editing is limited to 1 MiB.
- Directory responses show at most 2,000 entries or 2 MiB of metadata; directory protocol input is bounded to 8 MiB. ZIP manifests allow 2,000 entries and 32 levels of subdirectories. Oversized directories return a limit error or an explicit truncated listing.
- One active SFTP file API operation per server, sharing the existing SSH quota; other terminal/monitor leases keep their own transports. A reservation is released only after its SSH channel and workers settle.
- Metadata operations have a 25-second budget. Transfers have a 10-minute total budget and HTTP reads/writes use a 30-second idle deadline. Reverse proxies must also permit these transfer sizes/timeouts.
- Ordinary uploads clean temporary files on handled failures/cancellation where the remote remains reachable. Network loss can leave a temporary file. Resumable uploads keep temporary files for recovery until completion or explicit discard; token expiry does not remove remote temporary files. The original destination is not touched before commit.
- Overwrite requires `posix-rename@openssh.com`. Talus never deletes the original as an overwrite fallback. New-file publication prefers the atomic hard-link extension where available, otherwise uses the SFTP v3 no-clobber rename contract.
- Symbolic links are displayed and can be renamed/deleted, but file download/edit/overwrite do not intentionally follow them. Directory archives do not follow links. Filesystem isolation, when required, should be enforced by the target SSH account/chroot.

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
      DB_MAX_OPEN_CONNECTIONS: ${DB_MAX_OPEN_CONNECTIONS:-32}
      USAGE_LOG_WRITE_CONCURRENCY: ${USAGE_LOG_WRITE_CONCURRENCY:-4}
      USAGE_LOG_ACTIVE_LIMIT: ${USAGE_LOG_ACTIVE_LIMIT:-4096}
      USAGE_LOG_RETENTION_DAYS: ${USAGE_LOG_RETENTION_DAYS:-30}
      USAGE_LOG_SENSITIVE_RETENTION_DAYS: ${USAGE_LOG_SENSITIVE_RETENTION_DAYS:-90}
      USAGE_LOG_LEGACY_WRITERS_DRAINED: ${USAGE_LOG_LEGACY_WRITERS_DRAINED:-false}
      AGENT_HUB_URL: ${AGENT_HUB_URL:-}
      AGENT_ALLOW_HTTP: ${AGENT_ALLOW_HTTP:-false}
      MONITOR_INTERVAL: ${MONITOR_INTERVAL:-5}
      SSH_TIMEOUT: ${SSH_TIMEOUT:-10}
      EXEC_TIMEOUT: ${EXEC_TIMEOUT:-30}
      EXEC_TIMEOUT_MAX: ${EXEC_TIMEOUT_MAX:-300}
      EXEC_OUTPUT_LIMIT: ${EXEC_OUTPUT_LIMIT:-8388608}
      EXEC_OUTPUT_STREAM_LIMIT: ${EXEC_OUTPUT_STREAM_LIMIT:-4194304}
      METRICS_RETENTION_ENABLED: ${METRICS_RETENTION_ENABLED:-false}
      METRICS_RETENTION_DRY_RUN: ${METRICS_RETENTION_DRY_RUN:-true}
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
docker compose -f docker-compose.prod.yml stop hub
docker compose -f docker-compose.prod.yml exec -T db pg_dump -U vpsmanager -d vpsmanager -Fc > talus-before-upgrade.dump
docker compose -f docker-compose.prod.yml pull hub
docker compose -f docker-compose.prod.yml up -d --no-deps hub
```

When upgrading from a version without JWT revocation, stop every old Hub instance
and its WebSocket sessions before starting this version. Do not run those old
and new versions together. Subsequent upgrades and rollbacks must retain token
version checks and active session revocation.

### Reverse-proxy timeouts

Talus disables its global HTTP write timeout and applies per-route budgets, so a
300-second Exec or a bounded Relay stream is not cut off mid-response. Any proxy
in front of Talus (nginx, Traefik, Caddy, Cloudflare) must match that budget —
including room for result return and cleanup, so a 300-second route needs more
than 300 seconds at the proxy:

| Route | Minimum proxy timeout |
| --- | --- |
| `POST /api/v1/servers/*/exec`, `POST /api/v1/services/*/relay` | ≥ 330s |
| SFTP metadata / transfers | ≥ 30s / ≥ 630s |
| Other ordinary HTTP API routes | ≥ 15s |

SFTP transfers have a 10-minute total budget and a 30-second HTTP read/write idle
deadline. Allow file bodies of at least 64 MiB and disable response buffering for
SFTP file and ZIP downloads.

Buffered responses must be disabled for `/api/v1/services/*/relay` so streamed
bodies are forwarded incrementally. Enable WebSocket upgrades for terminal
routes and configure their idle timeout for the intended interactive session
length; the ordinary HTTP timeout above does not define a terminal session limit.

The browser negotiates the `talus.terminal.v2` WebSocket subprotocol: terminal
output uses binary frames containing the original SSH bytes, while authentication,
input, resize, connection status and errors remain JSON. Decode binary output as
a UTF-8 stream across frame boundaries. Clients that omit the subprotocol retain
the JSON output format; incomplete UTF-8 characters are carried between reads.

Ready terminal sessions send WebSocket Ping and SSH `keepalive@openssh.com`
requests every 25 seconds. Browsers answer Ping automatically. An SSH request
without a reply within 10 seconds, or a missing WebSocket Pong for 35 seconds,
ends the session and discards its connection. An SSH rejection reply still counts
as a live peer. Probes stop and join during disconnect, cancellation or revocation.

## Tech Stack

| Layer | Technology |
| ------- | ----------- |
| Backend | Go, [chi](https://github.com/go-chi/chi), [GORM](https://gorm.io), [gorilla/websocket](https://github.com/gorilla/websocket), [golang-jwt](https://github.com/golang-jwt/jwt) |
| Frontend | React 19, TypeScript, [Vite](https://vite.dev), [Tailwind CSS](https://tailwindcss.com), [xterm.js](https://xtermjs.org) |
| Database | PostgreSQL 16 + [TimescaleDB](https://www.timescale.com) |
| Monitoring Agent | Go + [gopsutil](https://github.com/shirou/gopsutil) |
| Deployment | Docker Compose |

The workflow is informed by [EdgeSSH](https://github.com/aozorae/EdgeSSH): a file panel alongside the terminal, binary transfers, progress/cancellation and temporary-file publication. Talus implements the protocol through `github.com/pkg/sftp` on its Go backend.

## AI Integration

Talus ships with a portable [agent skill](skills/talus/SKILL.md) so any AI
coding assistant (OpenCode, Claude Code, Cursor, …) can operate Talus through
its REST API: list servers, run commands, read metrics, relay requests to
registered services, manage files over SFTP, and add/update servers.

### Prerequisites

- A running Talus instance (see Deployment above)
- An API key — create one in **Talus Web UI → API Keys** (default scopes cover
  read/exec/metrics; `servers:write`, `services:relay`, and file scopes are opt-in)

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

### Services: prefer Talus relay for registered services

Registered services (Dokploy, Portainer, or any internal app) are proxied by
Talus — their `base_url` is an internal address an AI cannot touch directly,
and their credentials are injected by Talus at relay time. When operating a
service on a server, the AI should first check whether it is registered in
Talus (`GET /api/v1/services?server_id=<id>`); if it is, use it through relay.
If it is not registered, the AI may operate it directly on the server (and
consider registering it). The workflow for a registered service is:

```
GET /api/v1/services          → directory (name, description, guide excerpt)
GET /api/v1/services/{id}     → full usage_guide (per-service "skill")
POST /api/v1/services/{id}/relay → follow the guide to build the request
```

Every registered service can carry its own **usage guide** (`usage_guide`, markdown) —
written in the **Talus Web UI → Services → Add/Edit Service → Usage Guide** field. It
tells the AI how to use that service (endpoints, auth headers, request examples)
without hard-coding any service specifics into the shared skill.

### File API

Use a user JWT (`Authorization: Bearer …`) or an API key (`X-API-Key: …`). All routes enforce the API key's `server_ids`. The new scopes are opt-in; existing keys do not acquire file permissions automatically. A key with Exec or terminal access still has the underlying SSH account's command capabilities.

| Scope | Operations |
| --- | --- |
| `files:read` | List, stat, download, read text, directory ZIP |
| `files:write` | Upload, resume/status/discard own upload, mkdir, rename, save text |
| `files:delete` | Delete a file, link or empty directory |

API keys cannot approve an unverified or changed host key. An administrator must confirm it in the Web UI first. SFTP unavailability is reported explicitly; there is no shell-command fallback.

All paths below are relative to `/api/v1/servers/{id}/sftp`. Send remote paths in a URL-encoded query parameter or a JSON body. Normal responses use `{"data": ...}` and errors use `{"error":{"code":...,"reason":...,"message":...}}`. File and ZIP downloads are binary responses. Transfer bodies are binary, not base64 or JSON.

| Method and path | Request | Response |
| --- | --- | --- |
| `GET /entries?path=…` | Default path `.` | `path`, `parent`, `entries`, `truncated` |
| `GET /stat?path=…` | Remote path | `name`, `path`, `kind`, `size`, `mode`, `modified_at` |
| `GET /download?path=…` | Optional `Range`, `If-Range` headers | Binary, `ETag`, `Accept-Ranges`, `Content-Length`; `206` plus `Content-Range` when partial |
| `POST /upload?path=…&overwrite=false` | Raw bytes; Content-Length or `size` query | Uploaded size; one-shot upload, cancellation cleans partial data where possible |
| `POST /mkdir` | `{"path":"…","parents":false}` | Success; `parents:true` creates missing parents |
| `POST /rename` | `{"from":"…","to":"…"}` | Success; destination must not exist |
| `DELETE /entry?path=…` | Remote path | Deletes file/link or empty directory; no recursive delete |
| `GET /archive?path=…` | Remote directory | Streamed ZIP attachment |
| `GET /text?path=…` | Remote text file | `path`, `content`, `version` |
| `PUT /text` | `{"path":"…","content":"…","version":"sha256:…"}` | Saved text and new version; stale version is `409` |
| `POST /uploads/start` | `{"path":"…","size":123,"sha256":"64 hex digits","overwrite":false}` | Signed `token`, `path`, `size`, `offset`, `sha256`, `expires_at` |
| `POST /uploads/status` | `{"token":"…"}` | Actual offset and metadata; `completed:true` recovers a lost final acknowledgement |
| `PUT /uploads/chunk?offset=…&size=…` | Raw bytes, at most 1 MiB; `X-Upload-Token` header | Confirmed remote offset; stale offset is `409` |
| `POST /uploads/complete` | `{"token":"…"}` | Verifies total size/checksum and publishes; safe to retry after a lost acknowledgement |
| `DELETE /uploads` | `X-Upload-Token` header | Removes this uploader's temporary file |

Each chunk/status/complete request is authenticated again. Resume requires the same JWT user or API-key identity, not merely the same role. Changing the route/credentials makes an old upload token unusable for that new route. If a chunk request fails, query `/uploads/status` before retrying because some bytes may already have arrived.

Directory entries have `kind` equal to `file`, `directory`, `symlink` or `other`. File size and modified time are metadata, not a guarantee against concurrent external writes. Use the content version for editing and `If-Range` for resumed downloads. Downloads may abort after headers when a remote read fails; clients must check Content-Length/checksum or ZIP integrity rather than treating a partial body as success.

Usage logs record the operation, caller, server, outcome, route and bounded byte counts. They do not record file contents, recovery tokens or remote path strings.

#### File API examples

```bash
# Browse with a file-read API key.
curl --get "$TALUS_URL/api/v1/servers/1/sftp/entries" \
  -H "X-API-Key: $TALUS_API_KEY" --data-urlencode 'path=/home/deploy'

# One-shot binary upload. An existing destination is preserved by default.
curl -X POST "$TALUS_URL/api/v1/servers/1/sftp/upload?path=%2Fhome%2Fdeploy%2Fdata.bin" \
  -H "X-API-Key: $TALUS_API_KEY" -H 'Content-Type: application/octet-stream' \
  --data-binary @data.bin

# Download or resume with a file-read key. Retain/use the ETag with If-Range
# when the remote file might change between requests.
curl "$TALUS_URL/api/v1/servers/1/sftp/download?path=%2Fhome%2Fdeploy%2Fdata.bin" \
  -H "X-API-Key: $TALUS_API_KEY" -o data.bin
```

### Example prompts

```bash
"List all servers via Talus API"
"Check CPU metrics on server web-01"
"Run 'docker ps' on prod-db"
"Relay a request to the Grafana service"
```

## Configuration

Configure these values in the Hub process or container environment. When using
Compose, add any variable not already listed to the Hub's `environment` section;
setting it in `.env` alone does not pass it into the container.

| Variable | Default | Description |
| ---------- | --------- | ------------- |
| `DB_PASSWORD` | *(required)* | PostgreSQL password |
| `VPSMANAGER_MASTER_KEY` | *(required)* | Cryptographically random hex master key (32+ bytes); preserve across upgrades |
| `JWT_SECRET` | *(required)* | JWT signing secret |
| `PORT` | `8080` | HTTP server port |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `DB_MAX_OPEN_CONNECTIONS` | `32` | Maximum open database connections, with a minimum of `12` |
| `USAGE_LOG_WRITE_CONCURRENCY` | `4` | Usage log write concurrency limit |
| `USAGE_LOG_ACTIVE_LIMIT` | `4096` | Maximum number of active operations tracked in memory |
| `USAGE_LOG_RETENTION_DAYS` | `30` | Ordinary usage log retention in days |
| `USAGE_LOG_SENSITIVE_RETENTION_DAYS` | `90` | Sensitive-operation and deletion log retention in days |
| `USAGE_LOG_LEGACY_WRITERS_DRAINED` | `false` | Set to `true` only after all legacy instances and their transactions have stopped to enable historical audit backfill; remove after completion is confirmed |
| `MONITOR_INTERVAL` | `5` | Resident metrics collection interval (1–60 seconds) |
| `AGENT_HUB_URL` | unset | Hub HTTPS address reachable from nodes; required for automatic resident monitoring |
| `AGENT_ALLOW_HTTP` | `false` | Explicit opt-in to HTTP for trusted test/private networks |
| `MONITOR_TOLERATED_ROUNDS` | `1` | Legacy SSH monitoring setting; resident contact uses a 45-second timeout |
| `MONITOR_ALLOWED_LATENCY_SECONDS` | `0` | Legacy SSH monitoring setting; resident data freshness uses three sampling intervals (minimum 15 seconds) |
| `AGENT_ARTIFACTS_DIR` | `/usr/local/bin` | Directory containing `agents-manifest.json` and both Linux amd64/arm64 agent binaries; validated at startup |
| `METRICS_RETENTION_ENABLED` | `false` | Enable cleanup of eligible raw and rollup metrics older than 35 days; deletion also requires dry-run mode to be disabled |
| `METRICS_RETENTION_DRY_RUN` | `true` | Report the cleanup plan without deleting; deletion requires `METRICS_RETENTION_ENABLED=true` and this value `false` |
| `SSH_TIMEOUT` | `10` | SSH connection timeout (seconds) |
| `EXEC_TIMEOUT` | `30` | Default command budget (seconds), including SSH setup and execution; requests may override it up to the configured cap (300s by default) |
| `EXEC_TIMEOUT_MAX` | `300` | Maximum command budget (seconds); must be at least `EXEC_TIMEOUT` and at most `300` |
| `EXEC_OUTPUT_LIMIT` | `8388608` | Retained stdout + stderr limit (bytes); positive and at most 8 MiB |
| `EXEC_OUTPUT_STREAM_LIMIT` | `4194304` | Retained limit for each output stream (bytes); positive, at most 4 MiB, and no greater than `EXEC_OUTPUT_LIMIT` |

Resident contact expires after 45 seconds; data becomes stale after three sampling intervals (minimum 15 seconds). Collection errors and storage failures are shown separately.

### Monitoring agent upgrades

Resident monitoring does not schedule per-sample SSH commands. Installation/repair uses a separate three-minute budget, at most four concurrent deployments, and one job per server. Failed deployments retry after at least one minute. Missing `AGENT_HUB_URL` is shown as setup required. Existing metric history is retained.

Artifacts and configuration are verified before activation. Compatible resident agents keep reporting while an update is staged. The new service must contact the Hub after restart; failed activation restores the previous executable, configuration and service unit where available.

Deleting a server uninstalls its resident agent and removes the recorded installation layout, even after `AGENT_HUB_URL` is cleared or the SSH credential changes to another account. The installer's account is recorded at activation time; deletion stops that account's unit (switching from a root login to the owning account when needed) and removes its state directory. A reinstall under a different account retires the previous layout after the new service is healthy. Root can stop the previous owner's unit; a downgraded login that cannot reach it keeps the files and logs a warning instead.

### Docker upgrades and encrypted data

Keep the existing database volume and the original `.env`, especially `VPSMANAGER_MASTER_KEY`. Stop the old Hub before replacing it; upgrades assume a single Hub and a brief maintenance window. A database advisory lock serializes migration by new Hubs, but cannot stop an older binary from writing.

For an image-based Compose deployment, select the new image tag in your Compose configuration and run:

```bash
docker compose stop hub
docker compose exec -T db pg_dump -U vpsmanager -d vpsmanager -Fc > talus-before-upgrade.dump
docker compose pull hub
docker compose up -d --no-deps hub
docker compose logs -f hub
```

Verify the backup can be restored with the original master key. Do not remove the database volume. Source-build deployments use `docker compose up -d --no-deps --build hub` instead of pulling an image.

On startup the Hub adds the internal encryption-version columns, then automatically upgrades SSH credentials, service credentials, SOCKS5 passwords and recoverable API keys. It processes one record at a time, verifies the new ciphertext, and atomically commits its salt, ciphertext and version. Soft-deleted records are included. Historical API keys without recoverable raw values retain their existing authentication hash and do not need reissuing. HTTP, monitoring and SSH pools start only after migration completes. Docker health checks may remain in `starting` while migration runs; allow enough startup time for large databases.

If migration fails, startup stops and logs the table, record ID and failure stage without secret values. Keep the original master key and data, fix the cause, and restart: already committed records are verified and skipped, while remaining records are migrated. The same version checks also validate existing v2 ciphertext on subsequent starts, including after backup restoration.

Once any record has been upgraded, the original Argon2id-only image cannot read all credentials, even if startup later fails. To roll back, restore the pre-upgrade database backup with its original master key; changes made after that backup will be lost. Merely changing the image tag or master key is not a rollback. Future reverse migration would require a separate compatible tool.

## Security

- SSH private keys and passwords, SOCKS5 passwords, service credentials, and recoverable API key raw values use **AES-256-GCM** at rest. New records use purpose-separated **HKDF-SHA256** keys derived from `VPSMANAGER_MASTER_KEY`, which must be generated by a cryptographically secure random source (`openssl rand -hex 32`). Login passwords continue to use bcrypt. Legacy Argon2id-encrypted records are automatically upgraded before the Hub starts serving requests.
- Credential and API key values are **never returned** in list API responses (`json:"-"`). Dedicated JWT-only reveal endpoints decrypt on-demand with **audit logging** (slog structured JSON) and **rate limiting** (5 req/min per user).
- Target servers require **no additional inbound ports**; resident monitoring needs outbound access to the configured Hub HTTPS endpoint.
- Each resident agent uses a node-scoped credential encrypted at rest, independent from administrator/API keys. Installation uses the verified SSH route; normal sampling does not require SSH connectivity.
- Changing a password invalidates existing JWTs and actively closes that user's terminal sessions. API keys have a separate lifecycle.
- **Explicit SSH host-key confirmation**: on first connection, Talus records the presented SHA256 fingerprint and stops before SSH authentication. An administrator must verify it against a trusted source and confirm it before connecting. Jump and target hosts are confirmed separately. Existing trusted keys remain valid; changed keys block authentication until explicitly approved. Confirmation checks the displayed fingerprint and connection revision to reject stale requests.

## Tests

Use [the unified test entry point](tests/README.md) for the `fast`, `integration`,
`ui`, and `e2e` suites and the M1–M4 gates. The generated
[test inventory](tests/coverage.md) lists scenarios, implementations, and commands.

M1's deployment check starts the current Hub with an owned temporary database
and a loopback HTTP upstream. It verifies bootstrap/restart, JWT revocation,
service discovery, usage guides, and Relay behavior without a personal server,
public endpoint, or historical deployment record. Set `TEST_DATABASE_URL` to an
isolated TimescaleDB test instance whose role can create temporary databases
and install the TimescaleDB extension.
A missing database or a skipped required test blocks the gate.

```bash
bash tests/run.sh --gate M1
```

## Releasing

Maintainers: follow the [release and tag workflow](docs/releasing.md) for version
selection, draft review, immutable images and `latest` recovery.

## License

[MIT](LICENSE)
