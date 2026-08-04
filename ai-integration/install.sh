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
  echo "  [!] Register it in ~/.claude/settings.json:"
  echo '      { "hooks": { "UserPromptSubmit": [ { "hooks": [ { "type": "command", "command": "python3 ~/.claude/hooks/inject-service-skills.py", "timeout": 15 } ] } ] } }'
  installed=1
fi

# Codex
if [ -d "$HOME/.codex" ]; then
  mkdir -p "$HOME/.codex/hooks"
  curl -fsSL "$BASE_URL/codex/inject-service-skills.py" \
    -o "$HOME/.codex/hooks/inject-service-skills.py"
  chmod +x "$HOME/.codex/hooks/inject-service-skills.py"
  echo "  [ok] Codex hook → ~/.codex/hooks/inject-service-skills.py"
  echo "  [!] Codex 0.129+: enable [features].hooks=true in ~/.codex/config.toml,"
  echo "      register the hook in hooks.json, and approve it via /hooks."
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
