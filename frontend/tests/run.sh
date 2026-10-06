#!/bin/bash
# Compile the modules under test (TS) to runnable .mjs, then run all committed
# tests. Zero new dependencies: uses the project's own tsc + node.
set -euo pipefail
cd "$(dirname "$0")/.."   # frontend/

# 1. Compile every source that has tests. Adding a test file alone does NOT
#    wire up its module — add a `compile <src> <name>` line here too, otherwise
#    the test silently fails to import and the suite never exercises it.
compile() { # <src> <outName>
  local src="$1" out="$2" tmp outfile
  tmp="$(mktemp -d)"
  if ! env -u NODE_ENV npx tsc "$src" \
    --ignoreConfig --outDir "$tmp" \
    --module esnext --moduleResolution bundler --target es2022 \
    --skipLibCheck --esModuleInterop 2>"$tmp/tsc.log"; then
    echo "✗ tsc failed for $src"
    cat "$tmp/tsc.log"
    rm -rf "$tmp"
    exit 1
  fi
  outfile="$(find "$tmp" -name "$(basename "${src%.ts}").js" -print -quit)"
  cp "$outfile" "tests/.compiled-$out.mjs"
  rm -rf "$tmp"
}

compile src/lib/auth.ts auth
compile src/lib/query.ts query
# query.ts imports the auth store; tests use the separately compiled module.
sed 's|"./auth"|"./.compiled-auth.mjs"|g' tests/.compiled-query.mjs > tests/.compiled-query-auth.mjs
mv tests/.compiled-query-auth.mjs tests/.compiled-query.mjs
compile src/features/monitoring/lib/series.ts series
compile src/features/auth/lib/api-key-error.ts api-key-error
compile src/lib/unauthorized.ts unauthorized
compile src/features/usage-logs/lib/session.ts usage-session
compile src/features/services/lib/credential-rows.ts credential-rows

# 2. Run every test file under tests/ (skip this runner + compiled artifacts).
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

rm -f tests/.compiled-*.mjs
exit $FAIL
