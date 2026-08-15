#!/bin/bash
# Compile the query layer (TS) to a temp module and run all committed tests.
# Zero new dependencies: uses the project's own tsc + node.
set -euo pipefail
cd "$(dirname "$0")/.."   # frontend/

# 1. Compile src/lib/query.ts to a runnable .mjs (gitignored).
OUT="tests/.compiled-query.mjs"
rm -rf /tmp/pi-query-compile
env -u NODE_ENV npx tsc src/lib/query.ts \
  --ignoreConfig --outDir /tmp/pi-query-compile \
  --module nodenext --moduleResolution nodenext --target es2022 \
  --skipLibCheck --esModuleInterop >/dev/null 2>&1
cp /tmp/pi-query-compile/query.js "$OUT"

# 2. Run every test file under tests/ (skip this runner + the compiled artifact).
FAIL=0
for test in tests/*.test.mjs; do
  echo "▶ $test"
  if node "$test"; then
    echo "  ✓ PASS"
  else
    echo "  ✗ FAIL"
    FAIL=1
  fi
done

rm -f "$OUT"
exit $FAIL
