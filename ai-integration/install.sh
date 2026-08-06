#!/usr/bin/env bash
# Talus AI integration installer
#
# Installs the service-directory injection plugin for every AI platform detected
# on this machine (OpenCode / pi / Claude Code / Codex). The talus skill itself
# is installed separately (see README AI Integration section).
#
# Usage:
#   bash <(curl -fsSL https://raw.githubusercontent.com/molicherry/Talus/main/ai-integration/install.sh)
#   # or locally:
#   bash ai-integration/install.sh

set -euo pipefail

BASE_URL="https://raw.githubusercontent.com/molicherry/Talus/main/ai-integration"
installed=0

echo "== Talus AI integration installer =="

# OpenCode
if [ -d "$HOME/.config/opencode" ]; then
  mkdir -p "$HOME/.config/opencode/plugins"
  curl -fsSL "$BASE_URL/opencode/inject-service-skills.js" \
    -o "$HOME/.config/opencode/plugins/inject-service-skills.js"
  echo "  [ok] OpenCode plugin → ~/.config/opencode/plugins/inject-service-skills.js"
  installed=1
fi

# pi
if [ -d "$HOME/.pi" ]; then
  mkdir -p "$HOME/.pi/agent/extensions/service-skills"
  curl -fsSL "$BASE_URL/pi/service-skills/index.ts" \
    -o "$HOME/.pi/agent/extensions/service-skills/index.ts"
  echo "  [ok] pi extension → ~/.pi/agent/extensions/service-skills/index.ts"
  installed=1
fi

# Claude Code
if [ -d "$HOME/.claude" ]; then
  mkdir -p "$HOME/.claude/hooks"
  curl -fsSL "$BASE_URL/claude/inject-service-skills.py" \
    -o "$HOME/.claude/hooks/inject-service-skills.py"
  chmod +x "$HOME/.claude/hooks/inject-service-skills.py"
  echo "  [ok] Claude Code hook → ~/.claude/hooks/inject-service-skills.py"
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$HOME/.claude/settings.json" <<'PY'
import json, os, shutil, sys

path = sys.argv[1]
hook = {"type": "command", "command": "python3 ~/.claude/hooks/inject-service-skills.py", "timeout": 15}
data = {}
existed = os.path.exists(path)
if existed:
    try:
        with open(path, "r") as f:
            data = json.load(f)
    except Exception as e:
        print(f"  [!] could not parse {path} ({e}); register the hook manually"); sys.exit(0)
    if not isinstance(data, dict):
        print(f"  [!] {path} is not a JSON object; register the hook manually"); sys.exit(0)
    shutil.copyfile(path, path + ".bak")
hooks = data.setdefault("hooks", {})
ups = hooks.setdefault("UserPromptSubmit", [])
already = any(h.get("command") == hook["command"] for e in ups for h in e.get("hooks", []))
if not already:
    ups.append({"hooks": [hook]})
    with open(path, "w") as f:
        json.dump(data, f, indent=2)
    print("  [ok] hook registered in " + path + (" (backup: " + path + ".bak)" if existed else ""))
else:
    print("  [ok] hook already registered in " + path)
PY
  else
    echo "  [!] python3 not found — register the hook manually in ~/.claude/settings.json"
  fi
  installed=1
fi

# Codex
if [ -d "$HOME/.codex" ]; then
  mkdir -p "$HOME/.codex/hooks"
  curl -fsSL "$BASE_URL/codex/inject-service-skills.py" \
    -o "$HOME/.codex/hooks/inject-service-skills.py"
  chmod +x "$HOME/.codex/hooks/inject-service-skills.py"
  echo "  [ok] Codex hook → ~/.codex/hooks/inject-service-skills.py"
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$HOME/.codex" <<'PY'
import json, os, re, shutil, sys

base = sys.argv[1]
# 1) ~/.codex/config.toml — enable [features].hooks
cfg = os.path.join(base, "config.toml")
content = ""
existed = os.path.exists(cfg)
if existed:
    with open(cfg) as f:
        content = f.read()
    shutil.copyfile(cfg, cfg + ".bak")
if re.search(r"^\s*hooks\s*=\s*true\s*$", content, re.M):
    print("  [ok] hooks already enabled in " + cfg)
elif re.search(r"^\s*hooks\s*=", content, re.M):
    print("  [!] " + cfg + " already sets hooks to something other than true; set [features].hooks = true manually")
elif "[features]" in content:
    content = content.replace("[features]", "[features]\nhooks = true", 1)
    with open(cfg, "w") as f:
        f.write(content)
    print("  [ok] enabled hooks in " + cfg + " (backup: " + cfg + ".bak)")
else:
    content += "\n[features]\nhooks = true\n"
    with open(cfg, "w") as f:
        f.write(content)
    print("  [ok] created " + cfg + " with [features].hooks = true" + (" (backup: " + cfg + ".bak)" if existed else ""))
# 2) ~/.codex/hooks.json — register UserPromptSubmit
hp = os.path.join(base, "hooks.json")
hook = {"type": "command", "command": "python3 -X utf8 ~/.codex/hooks/inject-service-skills.py", "timeout": 15}
data = {}
hexisted = os.path.exists(hp)
if hexisted:
    try:
        with open(hp) as f:
            data = json.load(f)
    except Exception as e:
        print(f"  [!] could not parse {hp} ({e}); register the hook manually")
        hexisted = False
    if not isinstance(data, dict):
        print(f"  [!] {hp} is not a JSON object; register the hook manually"); hexisted = False
    if hexisted:
        shutil.copyfile(hp, hp + ".bak")
if isinstance(data, dict):
    hooks = data.setdefault("hooks", {})
    ups = hooks.setdefault("UserPromptSubmit", [])
    already = any(h.get("command") == hook["command"] for e in ups for h in e.get("hooks", []))
    if not already:
        ups.append({"hooks": [hook]})
        with open(hp, "w") as f:
            json.dump(data, f, indent=2)
        print("  [ok] hook registered in " + hp + (" (backup: " + hp + ".bak)" if hexisted else ""))
    else:
        print("  [ok] hook already registered in " + hp)
print("  [i] approve the hook once via /hooks in a Codex session (required)")
PY
  else
    echo "  [!] python3 not found — enable hooks manually (see ai-integration/README.md)"
  fi
  installed=1
fi

if [ "$installed" -eq 0 ]; then
  echo "  No supported AI platform detected under \$HOME."
  echo "  Install manually — see https://github.com/molicherry/Talus/tree/main/ai-integration"
  exit 1
fi

echo
echo "== Done. Restart your AI sessions. =="
echo "Remember to export TALUS_URL and TALUS_API_KEY (see README)."
