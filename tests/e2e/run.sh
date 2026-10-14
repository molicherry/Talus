#!/usr/bin/env bash
# M4 end-to-end: the built SPA served by a real Hub, speaking real HTTP/WS to a real
# SSH PTY peer (TC-09-02's browser flow on top of the protocol-level terminal-e2e
# suite).
#
# Fail-closed, following the project's convention: a missing prerequisite exits 2, a
# failed assertion exits 1. Nothing is mocked: the Hub migrates its own scratch
# database, serves the production bundle, and drives cmd/e2efixture over SSH.
#
#   TEST_DATABASE_URL=postgres://user:pass@host:port/postgres bash tests/e2e/run.sh
#
# Optional: E2E_HUB_PORT to pin the Hub's port.
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT="$PWD"

fail() { # setup/tooling problem: never confused with an assertion failure
  echo "✗ e2e setup: $*" >&2
  exit 2
}

TEST_DATABASE_URL="${TEST_DATABASE_URL:-}"
[ -n "$TEST_DATABASE_URL" ] || fail "TEST_DATABASE_URL is required (a reachable PostgreSQL 16 + TimescaleDB)"
command -v go >/dev/null 2>&1 || fail "go is required"
command -v node >/dev/null 2>&1 || fail "node is required"
command -v curl >/dev/null 2>&1 || fail "curl is required"
[ -d frontend/node_modules/playwright ] || fail "playwright is required (cd frontend && npm ci)"

WORK="$(mktemp -d)"
DB="talus_e2e_$(date +%s)_$$"
HUB_PORT="${E2E_HUB_PORT:-$((20000 + RANDOM % 20000))}"
VERSION="e2e-$(git rev-parse --short HEAD 2>/dev/null || echo local)"
PIDS=()

cleanup() {
  status=$?
  # Remove the playwright resolution symlink the spec relies on.
  rm -f tests/e2e/node_modules
  for pid in ${PIDS[@]:-}; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  # The Hub must be gone before its database is dropped.
  for pid in ${PIDS[@]:-}; do
    [ -n "$pid" ] && wait "$pid" 2>/dev/null || true
  done
  if [ -x "$WORK/e2efixture" ]; then
    "$WORK/e2efixture" -mode db-drop -database "$TEST_DATABASE_URL" -name "$DB" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK"
  exit "$status"
}
trap cleanup EXIT

echo "▶ building the Hub, the agent bundle, the fixture and the SPA"
(cd backend && go build -o "$WORK/hub" ./cmd/server && go build -o "$WORK/e2efixture" ./cmd/e2efixture) || fail "cannot build the backend"
(cd backend && sh build/agents.sh "$VERSION" "$WORK/agents" >/dev/null) || fail "cannot build the agent bundle"
(cd frontend && npm run build >/dev/null) || fail "cannot build the SPA"
[ -f frontend/dist/index.html ] || fail "the SPA build produced no index.html"

echo "▶ preparing the scratch database $DB"
DB_URL="$("$WORK/e2efixture" -mode db-create -database "$TEST_DATABASE_URL" -name "$DB" | sed -n 's/^DATABASE_URL=//p')"
[ -n "$DB_URL" ] || fail "cannot create the scratch database"

echo "▶ starting the controlled SSH peer"
"$WORK/e2efixture" -mode genkey -name "$WORK/client_key" >/dev/null
"$WORK/e2efixture" -mode serve -addr 127.0.0.1:0 -log "$WORK/peer.jsonl" \
  -host-key "$WORK/peer_host_key" -authorized-key "$WORK/client_key.pub" >"$WORK/peer.out" 2>&1 &
PIDS+=($!)
for _ in $(seq 1 100); do
  grep -q '^ADDR=' "$WORK/peer.out" 2>/dev/null && break
  sleep 0.1
done
PEER_ADDR="$(sed -n 's/^ADDR=//p' "$WORK/peer.out" | head -1)"
[ -n "$PEER_ADDR" ] || fail "the SSH peer did not start: $(cat "$WORK/peer.out" 2>/dev/null)"

echo "▶ starting the Hub on 127.0.0.1:$HUB_PORT"
DATABASE_URL="$DB_URL" \
  JWT_SECRET="e2e-jwt-secret-0123456789abcdef" \
  VPSMANAGER_MASTER_KEY="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" \
  STATIC_DIR="$ROOT/frontend/dist" \
  AGENT_ARTIFACTS_DIR="$WORK/agents" \
  PORT="$HUB_PORT" \
  "$WORK/hub" >"$WORK/hub.log" 2>&1 &
PIDS+=($!)
ready=""
for _ in $(seq 1 300); do
  if curl -fsS "http://127.0.0.1:$HUB_PORT/version" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.1
done
[ -n "$ready" ] || fail "the Hub did not become ready: $(tail -5 "$WORK/hub.log" 2>/dev/null)"

echo "▶ running the browser flow"
# The spec lives outside the frontend package, so it resolves playwright through the
# frontend's installed tree.
ln -sfn ../../frontend/node_modules tests/e2e/node_modules
HUB_URL="http://127.0.0.1:$HUB_PORT" \
  PEER_LOG="$WORK/peer.jsonl" \
  PEER_PORT="${PEER_ADDR##*:}" \
  CLIENT_KEY="$WORK/client_key" \
  node tests/e2e/terminal.spec.mjs || exit 1

echo "✓ terminal e2e passed (Hub on :$HUB_PORT, SSH peer $PEER_ADDR, database $DB)"
