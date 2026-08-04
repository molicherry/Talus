#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Talus Service Skills — Codex hook (UserPromptSubmit)

Injects the Talus service directory (name + description + usage_guide excerpt)
into the CURRENT prompt only when the user message mentions services.

Design (zero accumulation):
- Conditional trigger: reads the user prompt from stdin (UserPromptSubmit JSON)
  and injects only when it matches service keywords or names a registered
  service. Unrelated turns print nothing and make no Talus API call.
- Hook additionalContext affects only the current turn — it is never written
  to conversation history, so context does not grow turn over turn.
- Silent skip: no TALUS_API_KEY, unreachable Talus, empty service list, or
  any error -> prints nothing, conversation untouched.

Prerequisites (Codex 0.129+):
  - User-level config must enable hooks: [features].hooks = true
    in ~/.codex/config.toml (legacy name: codex_hooks = true).
  - After installing, approve the hook once via the /hooks TUI.

Install:
  1. Copy this file to ~/.codex/hooks/inject-service-skills.py (chmod +x)
  2. Register the hook in the matching hooks.json
     (user-level ~/.codex/hooks.json or project .codex/hooks.json):

     {
       "hooks": {
         "UserPromptSubmit": [
           { "hooks": [{ "type": "command", "command": "python3 -X utf8 ~/.codex/hooks/inject-service-skills.py", "timeout": 15 }] }
         ]
       }
     }

Config: TALUS_URL (default http://localhost:8080) + TALUS_API_KEY env vars.
"""

import json
import os
import sys
import time
import urllib.error
import urllib.request

TALUS_URL = os.environ.get("TALUS_URL", "http://localhost:8080").rstrip("/")
TALUS_API_KEY = os.environ.get("TALUS_API_KEY", "")

TTL_MS = 60_000
_cache = {"ts": 0, "text": "", "names": []}

SERVICE_KEYWORDS = [
    "service", "services", "relay", "proxy", "deploy",
    "部署", "服务", "代理", "应用", "业务", "面板",
    "dokploy", "portainer", "grafana",
]


def _has_service_keyword(text):
    lower = text.lower()
    return any(kw in lower for kw in SERVICE_KEYWORDS)


def _fetch_service_directory():
    global _cache
    now_ms = time.time() * 1000
    if now_ms - _cache["ts"] < TTL_MS:
        return _cache
    if not TALUS_API_KEY:
        return {"text": "", "names": []}
    try:
        req = urllib.request.Request(
            f"{TALUS_URL}/api/v1/services",
            headers={"X-API-Key": TALUS_API_KEY},
        )
        with urllib.request.urlopen(req, timeout=5) as resp:
            payload = json.loads(resp.read().decode("utf-8"))
        data = payload if isinstance(payload, list) else payload.get("data")
        if not isinstance(data, list) or len(data) == 0:
            return {"text": "", "names": []}
        names = []
        lines = []
        for s in data:
            name = str(s.get("name", ""))
            names.append(name)
            desc = s.get("description") or ""
            excerpt = s.get("usage_guide_excerpt") or "无"
            lines.append(f"- {name} — {desc} | 指南: {excerpt}")
        text = (
            "<service-skills-directory>\n"
            "可用服务（调用某个服务前，先 GET /services/{id} 读取 usage_guide）：\n"
            + "\n".join(lines)
            + "\n</service-skills-directory>"
        )
        _cache = {"ts": now_ms, "text": text, "names": names}
        return _cache
    except (urllib.error.URLError, urllib.error.HTTPError, json.JSONDecodeError, TimeoutError, OSError):
        return {"text": "", "names": []}


def _read_user_prompt():
    """UserPromptSubmit hooks receive a JSON payload on stdin with a `prompt` field."""
    try:
        raw = sys.stdin.read()
        if not raw:
            return ""
        data = json.loads(raw)
        prompt = data.get("prompt") or data.get("user_prompt") or ""
        return str(prompt)
    except Exception:
        return ""


def _emit(text):
    output = {
        "hookSpecificOutput": {
            "hookEventName": "UserPromptSubmit",
            "additionalContext": text,
        }
    }
    print(json.dumps(output, ensure_ascii=False), flush=True)


def main():
    try:
        prompt = _read_user_prompt()
        if not prompt:
            return 0
        if not _has_service_keyword(prompt):
            # No keyword — maybe the user named a service directly; that needs
            # the directory, so fetch once and check names.
            dir_data = _fetch_service_directory()
            if dir_data["text"] and any(
                n and n.lower() in prompt.lower() for n in dir_data["names"]
            ):
                _emit(dir_data["text"])
            return 0
        dir_data = _fetch_service_directory()
        if dir_data["text"]:
            _emit(dir_data["text"])
    except Exception:
        # Never break the conversation.
        pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
