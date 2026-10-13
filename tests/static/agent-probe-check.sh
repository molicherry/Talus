#!/usr/bin/env bash
# Verifies the final image's agent protocol check by extracting it from the
# Dockerfiles and running it against crafted manifests. A substring check accepted
# `"protocol_version": 10` and any `agent_version` at all; this asserts the real
# acceptance rule, including the negative cases (N07).
#
#   bash tests/static/agent-probe-check.sh
#
# Exit codes: 0 = every case behaves as required, 1 = a case misbehaved, 2 = setup
# error (the check could not be extracted).
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="${TALUS_SCAN_ROOT:-$(cd "$HERE/../.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Extract the awk program that the final stage runs, so the test cannot drift from
# the Dockerfile: the block between `awk -v want=` and the closing quote line.
extract_check() { # <dockerfile> <out>
  # Take the awk program verbatim: everything after the invocation line, up to the
  # line that closes the quoted program and names the probe file. Dockerfile line
  # continuations are dropped so the result is a runnable program.
  awk '
    /awk -v want=/ { inside = 1; next }
    inside {
      line = $0
      sub(/^[ \t]*/, "", line)
      if (line ~ /^'"'"'[ ]/) { exit }
      sub(/\\$/, "", line)
      print line
    }
  ' "$1" > "$2"
  [ -s "$2" ] || return 1
}

run_case() { # <name> <manifest> <expected-version> <want-ok|want-fail>
  local name="$1" manifest="$2" want="$3" expect="$4" rc
  printf '%s' "$manifest" > "$TMP/probe.json"
  awk -v want="$want" -f "$TMP/check.awk" "$TMP/probe.json" >/dev/null 2>&1
  rc=$?
  case "$expect" in
    ok) [ "$rc" -eq 0 ] || { echo "✗ $name: rejected a valid manifest (exit $rc)" >&2; return 1; } ;;
    fail) [ "$rc" -ne 0 ] || { echo "✗ $name: accepted an invalid manifest" >&2; return 1; } ;;
    *) echo "✗ $name: bad expectation $expect" >&2; return 2 ;;
  esac
  echo "✓ $name"
}

status=0
for dockerfile in Dockerfile backend/Dockerfile; do
  if ! extract_check "$ROOT/$dockerfile" "$TMP/check.awk"; then
    echo "✗ cannot extract the probe check from $dockerfile" >&2
    exit 2
  fi
  echo "▶ $dockerfile"
  run_case "$dockerfile exact manifest" \
    '{"protocol_version": 1, "agent_version": "v1.2.3"}' 'v1.2.3' ok || status=1
  run_case "$dockerfile pretty-printed manifest" \
    "$(printf '{\n  "protocol_version": 1,\n  "agent_version": "v1.2.3"\n}\n')" 'v1.2.3' ok || status=1
  run_case "$dockerfile protocol 10 is not protocol 1" \
    '{"protocol_version": 10, "agent_version": "v1.2.3"}' 'v1.2.3' fail || status=1
  run_case "$dockerfile protocol as a string is rejected" \
    '{"protocol_version": "1", "agent_version": "v1.2.3"}' 'v1.2.3' fail || status=1
  run_case "$dockerfile empty version is rejected" \
    '{"protocol_version": 1, "agent_version": ""}' 'v1.2.3' fail || status=1
  run_case "$dockerfile wrong version is rejected" \
    '{"protocol_version": 1, "agent_version": "v9.9.9"}' 'v1.2.3' fail || status=1
  run_case "$dockerfile missing version is rejected" \
    '{"protocol_version": 1}' 'v1.2.3' fail || status=1
  run_case "$dockerfile missing protocol is rejected" \
    '{"agent_version": "v1.2.3"}' 'v1.2.3' fail || status=1
  run_case "$dockerfile version prefix is not a match" \
    '{"protocol_version": 1, "agent_version": "v1.2.30"}' 'v1.2.3' fail || status=1
done

if [ "$status" -eq 0 ]; then
  echo "agent probe check: ALL PASS"
else
  echo "agent probe check: FAILED" >&2
fi
exit "$status"
