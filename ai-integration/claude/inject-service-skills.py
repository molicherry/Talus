#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Talus Service Skills — Claude Code hook (UserPromptSubmit)

Injects the Talus service directory (name + description + usage_guide excerpt)
into every user prompt, so the AI always sees what services are available and
is reminded to fetch the per-service usage guide before relaying.

Silent-skip behavior: no TALUS_API_KEY, unreachable Talus, empty service list,
or any error -> prints nothing (empty stdout), conversation untouched.

Install:
  1. Copy this file to ~/.claude/hooks/inject-service-skills.py (chmod +x)
  2. Register the hook in ~/.claude/settings.json:

     {
       "hooks": {
         "UserPromptSubmit": [
           { "hooks": [{ "type": "command", "command": "python3 ~/.claude/hooks/inject-service-skills.py", "timeout": 15 }] }
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
_cache = {"ts": 0, "text": ""}


def _fetch_service_directory():
    now_ms = time.time() * 1000
    if now_ms - _cache["ts"] < TTL_MS:
        return _cache["text"]
    if not TALUS_API_KEY:
        return ""
    try:
        req = urllib.request.Request(
            f"{TALUS_URL}/api/v1/services",
            headers={"X-API-Key": TALUS_API_KEY},
        )
        with urllib.request.urlopen(req, timeout=5) as resp:
            payload = json.loads(resp.read().decode("utf-8"))
        data = payload if isinstance(payload, list) else payload.get("data")
        if not isinstance(data, list) or len(data) == 0:
            return ""
        lines = []
        for s in data:
            name = s.get("name", "")
            desc = s.get("description") or ""
            excerpt = s.get("usage_guide_excerpt") or "无"
            lines.append(f"- {name} — {desc} | 指南: {excerpt}")
        text = (
            "<service-skills-directory>\n"
            "可用服务（调用某个服务前，先 GET /services/{id} 读取 usage_guide）：\n"
            + "\n".join(lines)
            + "\n</service-skills-directory>"
        )
        _cache["ts"] = now_ms
        _cache["text"] = text
        return text
    except (urllib.error.URLError, urllib.error.HTTPError, json.JSONDecodeError, TimeoutError, OSError):
        return ""


def main():
    try:
        block = _fetch_service_directory()
        if not block:
            return 0
        output = {
            "hookSpecificOutput": {
                "hookEventName": "UserPromptSubmit",
                "additionalContext": block,
            }
        }
        print(json.dumps(output, ensure_ascii=False), flush=True)
    except Exception:
        # Never break the conversation.
        pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
