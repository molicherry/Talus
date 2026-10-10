#!/usr/bin/env bash
# Static checker for REQ-01: the platform adapters are gone and the skill /
# API / Relay documentation is preserved.
#
#   bash tests/static/adapters-removed.sh            # repo assertion + self-test
#   bash tests/static/adapters-removed.sh --repo      # repo assertion only
#   bash tests/static/adapters-removed.sh --self-test # fixture self-test only
#
# Exit codes: 0 = all assertions passed, 1 = assertion failure, 2 = tool/scan error.
#
# Content exemptions come from tests/coverage.json `scan_exemptions` (the single
# source). They affect the content scan only; they never bypass the directory /
# artifact assertions.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="${TALUS_SCAN_ROOT:-$(cd "$HERE/../.." && pwd)}"
COVERAGE="${TALUS_COVERAGE_JSON:-$ROOT/tests/coverage.json}"

MODE="both"
for a in "$@"; do
  case "$a" in
    --repo) MODE="repo" ;;
    --self-test) MODE="self" ;;
    *) echo "unknown arg: $a" >&2; exit 2 ;;
  esac
done

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Banned content. Deliberately specific: generic words (Codex, OpenCode, skill,
# plugin, hook) are NOT banned.
PATTERNS=(
  'ai-integration'
  'inject-service-skills'
  'service-skills/index.ts'
  '<service-skills-directory>'
  '安装服务目录注入插件'
)

need_rg() {
  command -v rg >/dev/null 2>&1 || { echo "rg (ripgrep) is required" >&2; exit 2; }
}

read_exemptions() {
  # Prints "<kind>\t<path>" lines; exits 2 if the manifest is unreadable.
  node -e '
    const fs=require("fs");
    const p=process.argv[1];
    let d;
    try { d=JSON.parse(fs.readFileSync(p,"utf8")); }
    catch(e){ console.error("cannot read coverage.json: "+e.message); process.exit(2); }
    if(!Array.isArray(d.scan_exemptions)){ console.error("scan_exemptions missing"); process.exit(2); }
    for(const e of d.scan_exemptions) console.log(e.kind+"\t"+e.path);
  ' "$COVERAGE" || return 2
}

# --- content scan (repo or fixture root) -------------------------------------
scan_content() {
  local root="$1"
  local globs=(--glob '!.git/**' --glob '!**/node_modules/**' --glob '!frontend/dist/**' --glob '!**/.compiled-*')
  local kind path
  while IFS=$'\t' read -r kind path; do
    [ -n "$path" ] || continue
    if [ "$kind" = "negative_fixture" ]; then
      globs+=(--glob "!**/${path%/}/**")
    else
      globs+=(--glob "!**/$path")
    fi
  done < "$TMP/case-exemptions.tsv"

  set +e
  rg --no-heading --line-number --color never "${globs[@]}" \
    -e "${PATTERNS[0]}" -e "${PATTERNS[1]}" -e "${PATTERNS[2]}" -e "${PATTERNS[3]}" -e "${PATTERNS[4]}" \
    "$root" > "$TMP/rg.out" 2> "$TMP/rg.err"
  local rc=$?
  set -e
  if [ "$rc" -eq 2 ]; then
    echo "✗ scan error:" >&2
    cat "$TMP/rg.err" >&2
    return 2
  fi
  if [ "$rc" -eq 0 ]; then
    echo "✗ residual adapter references found:" >&2
    cat "$TMP/rg.out" >&2
    return 1
  fi
  return 0
}

check_artifacts() {
  local root="$1" rc=0
  if [ -e "$root/ai-integration" ] || [ -L "$root/ai-integration" ]; then
    echo "✗ ai-integration/ still exists (or is an empty dir / dangling symlink)" >&2
    rc=1
  fi
  return $rc
}

check_preserved() {
  local root="$1" rc=0
  local skill="$root/skills/talus/SKILL.md"
  [ -s "$skill" ] || { echo "✗ skills/talus/SKILL.md missing or empty" >&2; rc=1; }
  for f in "$root/README.md" "$root/docs/README.zh-CN.md"; do
    [ -s "$f" ] || { echo "✗ $f missing or empty" >&2; rc=1; continue; }
    grep -q 'TALUS_URL' "$f" || { echo "✗ $f lost TALUS_URL guidance" >&2; rc=1; }
    grep -q 'TALUS_API_KEY' "$f" || { echo "✗ $f lost TALUS_API_KEY guidance" >&2; rc=1; }
  done
  [ -s "$skill" ] && for needle in 'X-API-Key' 'services:relay' 'usage_guide'; do
    grep -q "$needle" "$skill" || { echo "✗ SKILL.md lost $needle" >&2; rc=1; }
  done
  return $rc
}

run_assertions() {
  local root="$1" full="$2" rc=0
  scan_content "$root" || rc=$?
  [ "$rc" -eq 0 ] || return "$rc"
  check_artifacts "$root" || rc=1
  if [ "$full" = "full" ]; then
    check_preserved "$root" || rc=1
  fi
  return "$rc"
}

# --- self-test (TC-01-03) -----------------------------------------------------
make_clean_fixture() {
  local d="$1"
  mkdir -p "$d/skills/talus" "$d/docs" "$d/tests"
  printf '# Skill\nX-API-Key services:relay usage_guide\n' > "$d/skills/talus/SKILL.md"
  printf '# Talus\nInstall the skill. TALUS_URL TALUS_API_KEY\n' > "$d/README.md"
  printf '# Talus 中文\n安装 skill。TALUS_URL TALUS_API_KEY\n' > "$d/docs/README.zh-CN.md"
  cp "$COVERAGE" "$d/tests/coverage.json"
}

self_test() {
  local rc=0
  # 1. clean tree passes
  make_clean_fixture "$TMP/clean"
  TALUS_SCAN_ROOT="$TMP/clean" TALUS_COVERAGE_JSON="$TMP/clean/tests/coverage.json" \
    bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1 && echo "✓ clean tree passes" || { echo "✗ clean tree should pass" >&2; rc=1; }

  # 2. residual content fails
  make_clean_fixture "$TMP/residue"; printf 'see ai-integration/install.sh\n' > "$TMP/residue/notes.md"
  TALUS_SCAN_ROOT="$TMP/residue" TALUS_COVERAGE_JSON="$TMP/residue/tests/coverage.json" \
    bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1 && { echo "✗ residual content should fail" >&2; rc=1; } || echo "✓ residual content rejected"

  # 3. leftover directory fails (even empty)
  make_clean_fixture "$TMP/dir"; mkdir -p "$TMP/dir/ai-integration"
  TALUS_SCAN_ROOT="$TMP/dir" TALUS_COVERAGE_JSON="$TMP/dir/tests/coverage.json" \
    bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1 && { echo "✗ empty ai-integration/ should fail" >&2; rc=1; } || echo "✓ empty directory rejected"

  # 4. dangling symlink fails
  make_clean_fixture "$TMP/link"; ln -s /nonexistent "$TMP/link/ai-integration"
  TALUS_SCAN_ROOT="$TMP/link" TALUS_COVERAGE_JSON="$TMP/link/tests/coverage.json" \
    bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1 && { echo "✗ dangling symlink should fail" >&2; rc=1; } || echo "✓ dangling symlink rejected"

  # 5. missing skill fails
  make_clean_fixture "$TMP/noskill"; rm "$TMP/noskill/skills/talus/SKILL.md"
  TALUS_SCAN_ROOT="$TMP/noskill" TALUS_COVERAGE_JSON="$TMP/noskill/tests/coverage.json" \
    bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1 && { echo "✗ missing skill should fail" >&2; rc=1; } || echo "✓ missing skill rejected"

  # 6. exempted path is allowed
  make_clean_fixture "$TMP/case-exempt"; mkdir -p "$TMP/case-exempt/docs"
  printf 'historical ai-integration note\n' > "$TMP/case-exempt/docs/ARCHITECTURE.zh-CN.md"
  TALUS_SCAN_ROOT="$TMP/case-exempt" TALUS_COVERAGE_JSON="$TMP/case-exempt/tests/coverage.json" \
    bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1 && echo "✓ exempted historical doc allowed" || { echo "✗ exempted path should pass" >&2; rc=1; }

  # 7. missing rg / unreadable manifest → tool error (2)
  TALUS_SCAN_ROOT="$TMP/clean" TALUS_COVERAGE_JSON="$TMP/does-not-exist.json" \
    bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1
  [ $? -eq 2 ] && echo "✓ unreadable manifest is a tool error" || { echo "✗ unreadable manifest must exit 2" >&2; rc=1; }

  return $rc
}

# --- main ---------------------------------------------------------------------
need_rg
if ! read_exemptions > "$TMP/case-exemptions.tsv"; then
  echo "✗ cannot load scan_exemptions from $COVERAGE" >&2
  exit 2
fi

status=0
case "$MODE" in
  repo)
    run_assertions "$ROOT" full || status=$?
    ;;
  self)
    self_test || status=1
    ;;
  both)
    run_assertions "$ROOT" full || status=$?
    if [ "$status" -eq 0 ]; then
      self_test || status=1
    fi
    ;;
esac

if [ "$status" -eq 0 ]; then
  echo "✓ adapters removed; skill and API/Relay docs preserved"
fi
exit "$status"
