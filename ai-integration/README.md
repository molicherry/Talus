# Talus AI Integration — per-service usage guides

Talus lets you attach a **usage guide** (markdown) to every registered service —
the per-service "skill" written in **Talus Web UI → Services → Add/Edit Service →
Usage Guide**. Any AI that can reach your Talus API can read it on demand via the
[talus skill](../skills/talus/SKILL.md) ("How do I use a specific service?").

This directory ships the **standard service-directory injection component**: a small
plugin/hook per AI platform that injects the service directory (name + description
+ guide excerpt) **only when the user mentions services** — so the AI sees what is
available exactly when it matters, without paying tokens on unrelated turns.

**Zero context accumulation**: injection affects only the current LLM call (pi
`context` messages / OpenCode `experimental.chat.messages.transform` / hook
`additionalContext`) and is never written to conversation history. Context does
not grow turn over turn. The full `usage_guide` is still fetched on demand —
only the directory is injected.

## Supported platforms

| Platform | File | Mechanism | Prerequisites |
| --- | --- | --- | --- |
| OpenCode | `opencode/inject-service-skills.js` | plugin (`experimental.chat.messages.transform`) | copy to plugins dir |
| pi | `pi/service-skills/index.ts` | extension (`context` → messages) | copy to extensions dir |
| Claude Code | `claude/inject-service-skills.py` | hook (`UserPromptSubmit`) | register in settings.json |
| Codex | `codex/inject-service-skills.py` | hook (`UserPromptSubmit`) | 0.129+ · `[features].hooks=true` · `/hooks` approval |

Cursor and other platforms without a prompt-injection hook are fully covered by
the skill's discovery rules (list services → read guide → relay) — the capability
is complete either way, this plugin is just an ergonomic boost.

## Behavior

- **Conditional trigger**: injects only when the latest user message matches
  service keywords (`service`, `服务`, `deploy`, `dokploy`, `portainer`, ...) or
  names a registered service. Unrelated turns: no injection (and, within the 60s
  cache window, no Talus API call either).
- Injected block: a bilingual header — *可用服务 / Available services（调用某个
  服务前，先 GET /services/{id} 读取 usage_guide / read its usage_guide before
  calling）* — followed by one line per service:
  `- <name> — <description> | 指南: <excerpt or 无>`.
- **Zero accumulation**: injection is per-current-call only, never persisted to
  conversation history.
- **Silent skip**: no `TALUS_API_KEY`, unreachable Talus, empty service list, or
  any error → nothing is injected, the conversation is untouched.
- 60s in-process cache avoids hammering the API.
- Backward compatible: only stable fields (`name`, `description`,
  `usage_guide_excerpt`) are read; older Talus instances simply show no excerpt.

## Install (one-liner)

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/molicherry/Talus/main/ai-integration/install.sh)
```

The script detects installed platforms under `$HOME` and installs the matching
plugin (OpenCode / pi / Claude Code / Codex). Manual per-platform steps below.

Prerequisites: a running Talus instance and an API key (Talus Web UI → API Keys).
Set `TALUS_URL` and `TALUS_API_KEY` in your shell environment (see
[README](../README.md#ai-integration)).

### OpenCode

```bash
mkdir -p ~/.config/opencode/plugins
cp opencode/inject-service-skills.js ~/.config/opencode/plugins/
```

### pi

```bash
mkdir -p ~/.pi/agent/extensions/
cp -r pi/service-skills ~/.pi/agent/extensions/
```

### Claude Code

```bash
mkdir -p ~/.claude/hooks
cp claude/inject-service-skills.py ~/.claude/hooks/
chmod +x ~/.claude/hooks/inject-service-skills.py
```

Add to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [{ "type": "command", "command": "python3 ~/.claude/hooks/inject-service-skills.py", "timeout": 15 }] }
    ]
  }
}
```

### Codex (0.129+)

```bash
mkdir -p ~/.codex/hooks
cp codex/inject-service-skills.py ~/.codex/hooks/
chmod +x ~/.codex/hooks/inject-service-skills.py
```

Enable hooks in your **user-level** `~/.codex/config.toml` (project-level
config cannot set feature flags):

```toml
[features]
hooks = true
```

Register the hook in the matching `hooks.json` (user-level
`~/.codex/hooks.json` or project `.codex/hooks.json`):

```json
{
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [{ "type": "command", "command": "python3 -X utf8 ~/.codex/hooks/inject-service-skills.py", "timeout": 15 }] }
    ]
  }
}
```

Then approve the hook once via the `/hooks` TUI in a Codex session.

## Verify

In a new AI session, ask *"what services are available via Talus?"* — if the
directory block appears in the reply/context, the plugin is wired up. Stop your
Talus instance (or unset `TALUS_API_KEY`) and confirm conversations proceed
normally with no errors.
