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
    --module nodenext --moduleResolution nodenext --target es2022 \
    --skipLibCheck --esModuleInterop 2>"$tmp/tsc.log"; then
    echo "✗ tsc failed for $src"
    cat "$tmp/tsc.log"
    rm -rf "$tmp"
    exit 1
  fi
  outfile="$tmp/$(basename "${src%.ts}").js"
  cp "$outfile" "tests/.compiled-$out.mjs"
  rm -rf "$tmp"
}

compile src/lib/query.ts query
compile src/features/monitoring/lib/series.ts series
compile src/features/auth/lib/api-key-error.ts api-key-error
compile src/lib/unauthorized.ts unauthorized

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
