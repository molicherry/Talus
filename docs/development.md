# Development guide

English | [简体中文](development.zh-CN.md)

This guide takes a fresh checkout through local startup and explains how to choose checks for a change. Read [architecture](architecture.md) for module responsibilities. Deployment and parameter semantics remain in the [README](../README.md#configuration); the full test contract is in [tests/README.md](../tests/README.md).

## Prerequisites

Use Linux or WSL 2 with Bash for the commands below. Docker Compose provides the simplest complete-stack setup. Native Windows agent verification is a separate workflow described in [Windows monitoring](windows-monitoring.md#native-verification); WSL testing does not replace it.

| Tool | Requirement / source |
| --- | --- |
| Go | Follow [backend/go.mod](../backend/go.mod), currently Go 1.27.3; allow toolchain downloads if dependencies require them |
| Node.js and npm | Use Node 24, matching CI and the Docker build; install frontend dependencies with `npm ci` |
| Docker and Compose v2 | Database and complete-stack development; image acceptance also needs Docker |
| Database | TimescaleDB on PostgreSQL 16, matching [docker-compose.yml](../docker-compose.yml); plain PostgreSQL lacks the required extension |
| Shell tools | Bash, `sh`, `sha256sum`, Git, curl and OpenSSL; agent builds use `sha256sum` |
| Browser tests | The project's Playwright dependency and Chromium; install them when running browser checks |

## First startup with Compose

From a fresh checkout:

```bash
git clone https://github.com/molicherry/Talus.git
cd Talus
cp .env.example .env
openssl rand -hex 32
openssl rand -hex 32
openssl rand -hex 32
```

Edit `.env`: choose `DB_PASSWORD` and put the two independently generated values in `VPSMANAGER_MASTER_KEY` and `JWT_SECRET`. For a fresh database, put the third independently generated value in `BOOTSTRAP_TOKEN` before starting Compose. Existing accounts need no bootstrap token. Preserve the master and JWT values while reusing the database. `.env` is ignored by Git; do not commit it. Leave `AGENT_HUB_URL` empty until you want to configure monitoring.

```bash
docker compose up -d --build
docker compose logs -f hub
```

In another terminal, verify readiness and version:

```bash
curl --fail http://localhost:8080/healthz
curl --fail http://localhost:8080/api/v1/version
```

Open `http://localhost:8080`. For a fresh database, set the third independent random value as `BOOTSTRAP_TOKEN` in `.env` and enter it at `/setup` to create the administrator; use a password of at least eight characters. Remove the token and restart after initialization; existing accounts need no bootstrap token. Ordinary login only authenticates existing accounts. An empty dashboard is expected. Adding a real node also requires SSH credentials and explicit host-key confirmation; see the [setup instructions](../README.md#4-add-a-server).

Compose builds the source but does not watch it. After code changes, rebuild the Hub with `docker compose up -d --no-deps --build hub`. `docker compose stop` preserves the database; do not use `down -v` on a database you intend to retain.

## Run the backend and frontend separately

This path supports Go iteration and Vite hot reload. All examples start at the repository root. Stop the Compose Hub first if it occupies port 8080; the development database below is separate from the Compose volume.

### 1. Start a development database

```bash
docker run -d --name talus-dev-db \
  -p 127.0.0.1:54329:5432 \
  -e POSTGRES_USER=talus_dev \
  -e POSTGRES_PASSWORD=talus_dev_local \
  -e POSTGRES_DB=talus_dev \
  timescale/timescaledb:latest-pg16
docker exec talus-dev-db pg_isready -U talus_dev -d talus_dev
```

Wait until `pg_isready` reports accepting connections. The credentials above are only for this loopback-bound development database. Reuse the container with `docker start talus-dev-db`; do not repeat `docker run` with the same name. No named volume is configured, so a replacement container will not automatically reuse the data. Use Compose's named volume for a database you need to retain across container replacement.

### 2. Build all monitoring agents

```bash
sh backend/build/agents.sh dev bin/agents
(cd backend && go run ./cmd/agentverify -version-in-binary bin/agents)
```

The script resolves its output relative to `backend/`; these commands create `backend/bin/agents`, which Git ignores. The Hub validates the manifest and all Linux amd64/arm64 and Windows amd64 binaries at startup, even when monitoring is disabled. Rebuild them after agent changes. This setup uses `dev`; custom versioned builds must keep Hub, agent and manifest versions consistent.

### 3. Start the Hub

Use the `.env` you configured above. Its values must be valid Bash assignments; quote values containing shell metacharacters. Source only your own trusted local configuration. The Go process does not automatically read `.env`.

```bash
set -a
source .env
set +a
export DATABASE_URL='postgres://talus_dev:talus_dev_local@127.0.0.1:54329/talus_dev?sslmode=disable'
export AGENT_ARTIFACTS_DIR="$(pwd)/backend/bin/agents"
export PORT=8080
unset STATIC_DIR
(cd backend && go run ./cmd/server)
```

The explicit database URL selects the development container; `.env`'s `DB_PASSWORD` is used by Compose, not by this URL. Keep the same master key when restarting against an existing database. Restart `go run` after Go changes; no backend watcher is configured. Verify `/healthz` and `/api/v1/version` as above.

### 4. Start Vite in another terminal

```bash
cd frontend
npm ci
VITE_API_BASE_URL=http://localhost:8080 npm run dev -- --host 127.0.0.1
```

Open the URL Vite prints. `VITE_API_BASE_URL` is the Hub base URL without `/api/v1` or a trailing slash. Both REST requests and terminal WebSockets use it. Vite has no API proxy configured, so omitting this value directs requests to the frontend server. Restart Vite after changing environment variables.

For a shared remote development environment, use a Hub address reachable by the browser; browser `localhost` refers to the browser's machine. A monitored node separately needs a reachable `AGENT_HUB_URL`. Existing database overrides take precedence over environment settings; inspect System settings when a changed environment value appears ineffective.

### 5. Check the production frontend build

```bash
(cd frontend && npm run build)
```

For same-origin local serving, stop the Go process and restart it with `STATIC_DIR` pointing to the absolute `frontend/dist` path. Build without a production `VITE_API_BASE_URL` override for same-origin serving and mock-API browser tests. Vite environment values are compiled into the bundle, so changing the Hub environment cannot change an already-built frontend.

## Verification

Commands below run from the repository root unless the command changes directory. Check [package scripts](../frontend/package.json), [CI](../.github/workflows/ci.yml) and the [test inventory](../tests/coverage.json) for executable definitions.

| Change | Local checks |
| --- | --- |
| Go behavior | Format changed files with `gofmt -w`; run affected packages/tests using `go test` from `backend/`; run `go vet ./...` and `go build ./...` as appropriate |
| Database, authentication, migrations or concurrency | Supply an isolated `TEST_DATABASE_URL`; run affected tests and relevant M1/M2/M4 gates; use `go test -race` for concurrency changes |
| Frontend logic or API decoding | `(cd frontend && npm run lint && npm test && npm run build)` |
| User interactions, terminal, files or settings | Install Chromium with `(cd frontend && npx playwright install --with-deps chromium)`; run `(cd frontend && npm run test:ui)` or the relevant documented browser suite |
| Agent artifacts or monitoring | Build/verify the agents and run relevant M3/M4 checks; native Windows lifecycle changes also need M5 |
| Locale text | `node frontend/tests/i18n-locale.test.mjs`; review meaning and interpolation in both languages |
| Documentation | `git diff --check`, relative-link checks, `node tests/render-coverage.mjs --check`, `bash tests/static/adapters-removed.sh --repo` |
| Release automation | `node --test tests/release-policy.test.mjs` |

CI also runs golangci-lint with [backend/.golangci.yml](../backend/.golangci.yml); its tool version is pinned in the workflow. Local focused checks do not replace required acceptance checks. Report missing prerequisites and skipped tests explicitly. Some native Go tests skip without a database; a successful process exit alone does not satisfy the unified acceptance gates.

### Isolated database tests

Create a separate test database in the development container once:

```bash
docker exec talus-dev-db createdb -U talus_dev talus_test
export TEST_DATABASE_URL='postgres://talus_dev:talus_dev_local@127.0.0.1:54329/talus_test?sslmode=disable'
bash tests/run.sh --list
bash tests/run.sh --gate M1
```

Install frontend dependencies first. The test role needs `CREATEDB` and permission to install TimescaleDB; startup tests create their own temporary databases and other tests use temporary schemas. Never point this variable at production. M3 additionally needs image-building support, including arm64 execution/emulation; browser gates need Chromium. Read [tests/README.md](../tests/README.md) before selecting M1–M5 or suites. Execution records go to `tests/.artifacts/`; `tests/coverage.md` describes implemented coverage, not a particular passing run.

## Making a change

1. Start from current `main` on a topic branch and inspect the affected paths in the [architecture map](architecture.md). Preserve unrelated local work.
2. For an API change, follow routes and authorization through handler, service and repository, then update frontend decoding/types and any affected Talus skill contract. For a setting, inspect the whitelist, runtime snapshot, UI, deployment mapping and configuration documentation together.
3. Keep product text in both locale files. Use the shared API client and query/session handling; preserve structured error reasons and account-change cleanup.
4. Add or update meaningful behavior coverage for changed contracts and failure paths. Register acceptance changes in `tests/coverage.json`, then regenerate with `node tests/render-coverage.mjs`; never edit the generated Markdown directly.
5. Update affected English/Chinese documentation together and run the selected checks. Review the diff for secrets, generated output and unrelated changes.
6. Describe the problem, resulting behavior, compatibility implications and checks actually run in the PR. English or Chinese is sufficient. Optionally add user-facing notes and upgrade steps in the template's corresponding language block; CI collects supplied notes into the Release draft, where maintainers complete translations. Version tags, the block format and publication follow the separate [release guide](releasing.md#pull-request-notes).

## Documentation ownership

README owns product onboarding; this guide owns contributor setup; architecture owns responsibility, data flow and lasting safety contracts; test and release guides own their procedures. Link to these owners instead of copying their inventories. Commands, defaults and supported platforms must be checked against scripts and code. Completed implementation plans belong in PR/Git history; keep their enduring decisions in maintained guides. PRs hold release-note inputs; the GitHub Release draft is the place to consolidate and review them before publication.

English and Chinese pages should cover the same workflows, commands and limitations and link to each other. The locale test checks translation keys, not Markdown translation completeness or factual correctness. CI currently skips documentation-only changes through path filters; no automatic bilingual-prose or general Markdown-link gate exists. Perform those reviews locally and do not claim them as existing CI checks.

## Common startup failures

| Symptom | Check |
| --- | --- |
| Agent manifest/binary validation fails | Rebuild all three artifacts; verify the absolute directory and matching versions |
| TimescaleDB extension or migration fails | Use the TimescaleDB image and a suitable database role; inspect Hub logs |
| Browser API requests return Vite HTML/404 | Set the Hub base URL in Vite's environment and restart Vite |
| Existing credentials stop decrypting | Restore the original master key for that database; generating another key does not recover data |
| Settings ignore an environment change | Inspect saved database overrides and restart/recreate the appropriate process/container |
| Monitoring remains unconfigured or unreachable | Check node-reachable Hub URL, host-key confirmation and platform prerequisites; HTTP health alone does not verify SSH or agent reporting |
