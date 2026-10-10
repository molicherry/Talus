#!/usr/bin/env bash
# Unified test entry point. Reads tests/coverage.json (the single machine
# source), validates it, then dispatches the selected runs.
#
#   bash tests/run.sh --suite fast|integration|ui|e2e|all
#   bash tests/run.sh --gate M1            # pre-registered phase gate
#   bash tests/run.sh --list
#
# Fail-closed: a missing/malformed coverage.json, or a required case in the
# selected set that is still `planned`, aborts before any runner starts.
set -euo pipefail
cd "$(dirname "$0")/.."
exec node tests/run-suites.mjs "$@"
