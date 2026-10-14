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
# source). Matching is on the exact repository-relative path (only a
# negative_fixture may declare a directory prefix). Exemptions affect the content
# scan only; they never bypass the directory / artifact assertions.
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

EXCLUDES=(--glob '!.git/**' --glob '!**/node_modules/**' --glob '!frontend/dist/**' --glob '!**/.compiled-*')

# The shipped tree is scanned with git (ls-files/grep), so ripgrep is not
# required at all there — it is only the fallback for a non-git fixture tree, and
# even that degrades to grep so the check runs on a stock runner.
# have_rg reports whether the ripgrep branch is used. TALUS_SCAN_TOOL pins the tool
# ("ripgrep" or "grep") so the self-test can exercise the ripgrep-free branch on a
# machine where ripgrep is installed; unset means "use ripgrep when available".
have_rg() {
  case "${TALUS_SCAN_TOOL:-}" in
    grep) return 1 ;;
    ripgrep|rg) return 0 ;;
  esac
  command -v rg >/dev/null 2>&1
}

list_files_fallback() { # <root> <out>
  # Same scope as the ripgrep path: only frontend/dist and node_modules are
  # pruned, and any other directory called dist stays in the scan (N06).
  ( cd "$1" && find . -path ./.git -prune -o \
      -path '*/node_modules' -prune -o -path ./frontend/dist -prune -o \
      -name '.compiled-*' -prune -o -type f -print ) | sed 's|^\./||' > "$2"
}

grep_content_fallback() { # <root> <out> <err>  -> 0 matches, 1 no matches, 2 tool error
  # The exclusion scope must match the ripgrep path exactly: only frontend/dist is
  # generated output. Excluding every directory named dist would quietly widen the
  # exemption and let residue under e.g. notes/dist pass (N06).
  #
  # The scan tool's exit status is captured immediately: the filtering steps below
  # used to become the function's status, so a grep error (2) reached the caller as
  # "clean" and an unfinished scan was reported as passing (P2-03).
  local rc=0 frc=0
  ( cd "$1" && grep -rInE --exclude-dir=.git --exclude-dir=node_modules \
      --exclude='.compiled-*' \
      -e "${PATTERNS[0]}" -e "${PATTERNS[1]}" -e "${PATTERNS[2]}" -e "${PATTERNS[3]}" -e "${PATTERNS[4]}" . ) \
    > "$2" 2> "$3" || rc=$?
  if [ "$rc" -ge 2 ]; then
    return 2
  fi
  if [ "$rc" -eq 1 ]; then
    : > "$2"
    return 1
  fi
  # grep cannot express a path-qualified directory exclusion, so drop the one
  # generated tree from the match list instead - and treat a filtering failure as a
  # tool error rather than as "no matches" (never mask it with an empty file).
  grep -v '^\./frontend/dist/' "$2" > "$2.filtered" 2>> "$3" || frc=$?
  if [ "$frc" -ge 2 ]; then
    rm -f "$2.filtered"
    return 2
  fi
  if ! mv "$2.filtered" "$2"; then
    rm -f "$2.filtered"
    return 2
  fi
  if [ "$frc" -eq 1 ]; then
    return 1
  fi
  return 0
}

# Prints "<kind>\t<path>"; exits 2 if the manifest is unreadable or malformed.
load_exemptions() {
  node -e '
    const fs=require("fs");
    const p=process.argv[1];
    let d;
    try { d=JSON.parse(fs.readFileSync(p,"utf8")); }
    catch(e){ console.error("cannot read coverage.json: "+e.message); process.exit(2); }
    const kinds=["planning","history","checker","negative_fixture"];
    if(!Array.isArray(d.scan_exemptions)){ console.error("scan_exemptions missing"); process.exit(2); }
    for(const e of d.scan_exemptions){
      if(typeof e.path!=="string"||e.path===""){ console.error("scan_exemption.path required"); process.exit(2); }
      if(!kinds.includes(e.kind)){ console.error("bad kind for "+e.path); process.exit(2); }
      if(e.path.includes("*")){ console.error("wildcards are not allowed: "+e.path); process.exit(2); }
      if(e.kind!=="negative_fixture"){
        // A non-fixture exemption must be a real file path, never a directory.
        if(e.path.endsWith("/")){ console.error("directory exemption not allowed: "+e.path); process.exit(2); }
        if(!e.path.includes(".")){ console.error("not a file path: "+e.path); process.exit(2); }
      }
      console.log(e.kind+"\t"+e.path);
    }
  ' "$COVERAGE" || return 2
}

is_exempt() { # <repo-relative-path>
  local rel="$1" kind path
  while IFS=$'\t' read -r kind path; do
    [ -n "$path" ] || continue
    if [ "$kind" = "negative_fixture" ]; then
      local dir="${path%/}"
      case "$rel" in "$dir"|"$dir"/*) return 0 ;; esac
    elif [ "$rel" = "$path" ]; then
      return 0
    fi
  done < "$TMP/exemptions.tsv"
  return 1
}

# --- content scan (repo or fixture root) -------------------------------------
scan_content() {
  local root="$1" rc=0
  # Scan the shipped content: everything git tracks, including tracked files
  # that match .gitignore and hidden files. A path that is not a repository
  # root (a fixture) is walked in full.
  local is_git=0
  if [ "$(git -C "$root" rev-parse --show-toplevel 2>/dev/null)" = "$root" ]; then is_git=1; fi

  set +e
  if [ "$is_git" = 1 ]; then
    ( cd "$root" && git ls-files ) > "$TMP/files" 2> "$TMP/err"
  elif have_rg; then
    ( cd "$root" && rg --files --hidden --no-ignore "${EXCLUDES[@]}" . ) > "$TMP/files" 2> "$TMP/err"
  else
    list_files_fallback "$root" "$TMP/files" 2> "$TMP/err"
  fi
  rc=$?
  set -e
  if [ "$rc" -ge 2 ]; then
    echo "✗ file scan error:" >&2
    cat "$TMP/err" >&2
    return 2
  fi
  sed -i 's|^\./||' "$TMP/files" 2>/dev/null || true

  set +e
  if [ "$is_git" = 1 ]; then
    ( cd "$root" && git grep -n -I -e "${PATTERNS[0]}" -e "${PATTERNS[1]}" -e "${PATTERNS[2]}" -e "${PATTERNS[3]}" -e "${PATTERNS[4]}" -- . ) \
      > "$TMP/matches" 2> "$TMP/err"
  elif have_rg; then
    ( cd "$root" && rg --no-heading --line-number --color never --hidden --no-ignore "${EXCLUDES[@]}" \
        -e "${PATTERNS[0]}" -e "${PATTERNS[1]}" -e "${PATTERNS[2]}" -e "${PATTERNS[3]}" -e "${PATTERNS[4]}" . ) \
      > "$TMP/matches" 2> "$TMP/err"
  else
    grep_content_fallback "$root" "$TMP/matches" "$TMP/err"
  fi
  rc=$?
  set -e
  if [ "$rc" -ge 2 ]; then
    echo "✗ content scan error:" >&2
    cat "$TMP/err" >&2
    return 2
  fi
  if [ "$rc" -eq 1 ]; then
    return 0
  fi

  : > "$TMP/content-residue.txt"
  while IFS= read -r line; do
    local rel="${line%%:*}"
    rel="${rel#./}"
    if ! is_exempt "$rel"; then
      printf '%s\n' "$line" >> "$TMP/content-residue.txt"
    fi
  done < "$TMP/matches"
  if [ -s "$TMP/content-residue.txt" ]; then
    echo "✗ residual adapter references found:" >&2
    cat "$TMP/content-residue.txt" >&2
    return 1
  fi
  return 0
}

check_artifacts() {
  local root="$1" rc=0 rel
  if [ -e "$root/ai-integration" ] || [ -L "$root/ai-integration" ]; then
    echo "✗ ai-integration/ still exists (or is an empty dir / dangling symlink)" >&2
    rc=1
  fi
  while IFS= read -r rel; do
    case "$rel" in
      inject-service-skills.js|inject-service-skills.py|service-skills/index.ts|\
*/inject-service-skills.js|*/inject-service-skills.py|*/service-skills/index.ts)
        echo "✗ leftover adapter artifact: $rel" >&2
        rc=1 ;;
    esac
  done < "$TMP/files"
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
  if [ -s "$skill" ]; then
    for needle in 'X-API-Key' 'services:relay' 'usage_guide'; do
      grep -q "$needle" "$skill" || { echo "✗ SKILL.md lost $needle" >&2; rc=1; }
    done
  fi
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

expect_fail() { # <name> <root>
  local name="$1" root="$2"
  if TALUS_SCAN_ROOT="$root" TALUS_COVERAGE_JSON="$root/tests/coverage.json" \
      bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1; then
    echo "✗ $name should fail" >&2
    return 1
  fi
  echo "✓ $name rejected"
  return 0
}

expect_pass() { # <name> <root>
  local name="$1" root="$2"
  if TALUS_SCAN_ROOT="$root" TALUS_COVERAGE_JSON="$root/tests/coverage.json" \
      bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1; then
    echo "✓ $name allowed"
    return 0
  fi
  echo "✗ $name should pass" >&2
  return 1
}

expect_tool_error() { # <name> <root> <tool> [path-prefix]  -> requires exit 2, never 1
  local name="$1" root="$2" tool="${3:-grep}" prefix="${4:-}" got=0
  if [ -n "$prefix" ]; then
    PATH="$prefix:$PATH" TALUS_SCAN_TOOL="$tool" TALUS_SCAN_ROOT="$root" \
      TALUS_COVERAGE_JSON="$root/tests/coverage.json" \
      bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1 || got=$?
  else
    TALUS_SCAN_TOOL="$tool" TALUS_SCAN_ROOT="$root" TALUS_COVERAGE_JSON="$root/tests/coverage.json" \
      bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1 || got=$?
  fi
  if [ "$got" -ne 2 ]; then
    echo "✗ $name must fail closed with exit 2, got $got" >&2
    return 1
  fi
  echo "✓ $name reported as a tool error"
  return 0
}

make_failing_grep() { # <dir>
  local dir="$1" real
  real="$(command -v grep)"
  mkdir -p "$dir"
  cat > "$dir/grep" <<SHIM
#!/usr/bin/env bash
for arg in "\$@"; do
  if [ "\$arg" = "-rInE" ]; then
    echo "simulated scan failure" >&2
    exit 2
  fi
done
exec "$real" "\$@"
SHIM
  chmod +x "$dir/grep"
}

self_test() {
  local rc=0

  # The ripgrep-free branch is real code with its own failure modes: exercise it
  # explicitly, with a working and with a failing scan tool (P2-03, N06).
  make_clean_fixture "$TMP/fallback"; mkdir -p "$TMP/fallback/notes/dist"
  printf 'ai-integration\n' > "$TMP/fallback/notes/dist/residue.md"
  if TALUS_SCAN_TOOL=grep TALUS_SCAN_ROOT="$TMP/fallback" \
      TALUS_COVERAGE_JSON="$TMP/fallback/tests/coverage.json" \
      bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1; then
    echo "✗ ripgrep-free scan allowed residue under notes/dist" >&2
    rc=1
  else
    echo "✓ ripgrep-free scan rejects notes/dist residue"
  fi

  make_clean_fixture "$TMP/clean" && expect_pass "clean tree" "$TMP/clean" || rc=1

  make_clean_fixture "$TMP/case-residue"; printf 'see ai-integration/install.sh\n' > "$TMP/case-residue/notes.md"
  expect_fail "residual content" "$TMP/case-residue" || rc=1

  make_clean_fixture "$TMP/hidden"; mkdir -p "$TMP/hidden/.config"; printf 'ai-integration\n' > "$TMP/hidden/.config/legacy.conf"
  expect_fail "hidden-file residue" "$TMP/hidden" || rc=1

  # A tracked file that also matches .gitignore still ships, so it must be
  # scanned (git ls-files, not the ignore rules).
  make_clean_fixture "$TMP/tracked"
  (
    cd "$TMP/tracked" || exit 1
    git init -q
    printf 'ignored/\n' > .gitignore
    mkdir -p ignored
    printf 'ai-integration\n' > ignored/residue.md
    git add -f ignored/residue.md .gitignore skills/talus/SKILL.md README.md docs/README.zh-CN.md tests/coverage.json >/dev/null 2>&1
  )
  expect_fail "tracked-but-gitignored residue" "$TMP/tracked" || rc=1

  make_clean_fixture "$TMP/dir"; mkdir -p "$TMP/dir/ai-integration"
  expect_fail "empty adapter directory" "$TMP/dir" || rc=1

  make_clean_fixture "$TMP/link"; ln -s /nonexistent "$TMP/link/ai-integration"
  expect_fail "dangling adapter symlink" "$TMP/link" || rc=1

  make_clean_fixture "$TMP/artifact"; mkdir -p "$TMP/artifact/pi/service-skills"
  : > "$TMP/artifact/pi/service-skills/index.ts"
  expect_fail "leftover adapter artifact" "$TMP/artifact" || rc=1

  make_clean_fixture "$TMP/noskill"; rm "$TMP/noskill/skills/talus/SKILL.md"
  expect_fail "missing skill" "$TMP/noskill" || rc=1

  make_clean_fixture "$TMP/exempt"
  printf 'historical ai-integration note\n' > "$TMP/exempt/docs/ARCHITECTURE.zh-CN.md"
  expect_pass "exact-path exemption" "$TMP/exempt" || rc=1

  make_clean_fixture "$TMP/narrow"; mkdir -p "$TMP/narrow/other/docs"
  printf 'ai-integration\n' > "$TMP/narrow/other/docs/ARCHITECTURE.zh-CN.md"
  expect_fail "unregistered look-alike path" "$TMP/narrow" || rc=1

  # A negative_fixture exemption is directory-boundary scoped: a look-alike
  # sibling (adapters-removal-evil) must NOT inherit the exemption.
  make_clean_fixture "$TMP/neg"
  node -e '
    const fs=require("fs");const p=process.argv[1];
    const d=JSON.parse(fs.readFileSync(p,"utf8"));
    d.scan_exemptions.push({path:"tests/fixtures/adapters-removal",kind:"negative_fixture",reason:"Negative fixtures intentionally contain banned adapter artifacts."});
    fs.writeFileSync(p,JSON.stringify(d,null,2));
  ' "$TMP/neg/tests/coverage.json"
  mkdir -p "$TMP/neg/tests/fixtures/adapters-removal" "$TMP/neg/tests/fixtures/adapters-removal-evil"
  printf 'ai-integration\n' > "$TMP/neg/tests/fixtures/adapters-removal/inside.md"
  expect_pass "negative fixture inside" "$TMP/neg" || rc=1
  printf 'ai-integration\n' > "$TMP/neg/tests/fixtures/adapters-removal-evil/outside.md"
  expect_fail "negative fixture boundary" "$TMP/neg" || rc=1

  TALUS_SCAN_ROOT="$TMP/clean" TALUS_COVERAGE_JSON="$TMP/does-not-exist.json" \
    bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1
  if [ $? -eq 2 ]; then echo "✓ unreadable manifest is a tool error"; else echo "✗ unreadable manifest must exit 2" >&2; rc=1; fi

  # A tool failure must not be mistaken for a clean scan. The fixture is not a git
  # repository, so the scan really goes through grep and the shim is reached.
  make_clean_fixture "$TMP/tool-error"
  make_failing_grep "$TMP/fakebin"
  expect_tool_error "failing grep" "$TMP/tool-error" grep "$TMP/fakebin" || rc=1
  # Positive control with the same tool selection and the real grep.
  if TALUS_SCAN_TOOL=grep TALUS_SCAN_ROOT="$TMP/clean" \
      TALUS_COVERAGE_JSON="$TMP/clean/tests/coverage.json" \
      bash "$HERE/adapters-removed.sh" --repo >/dev/null 2>&1; then
    echo "✓ ripgrep-free scan allows a clean tree"
  else
    echo "✗ ripgrep-free scan rejected a clean tree" >&2
    rc=1
  fi

  return $rc
}

# --- main ---------------------------------------------------------------------
# The scan uses git where it can and falls back to ripgrep or grep, so there is no
# hard tool requirement to assert here; a tool that misbehaves during the scan is
# reported as exit 2 by the scan itself (N08).
if ! load_exemptions > "$TMP/exemptions.tsv"; then
  echo "✗ cannot load scan_exemptions from $COVERAGE" >&2
  exit 2
fi

status=0
case "$MODE" in
  repo) run_assertions "$ROOT" full || status=$? ;;
  self) self_test || status=1 ;;
  both)
    run_assertions "$ROOT" full || status=$?
    if [ "$status" -eq 0 ]; then self_test || status=1; fi
    ;;
esac

if [ "$status" -eq 0 ]; then
  echo "✓ adapters removed; skill and API/Relay docs preserved"
fi
exit "$status"
