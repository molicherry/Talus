# Logging Guidelines

> How logging is done in this project.

---

## Overview

- **Library**: `log/slog` (Go 1.21+ standard library structured logging)
- **Format**: JSON to stdout (production), text to stderr (development)
- **Handler**: `slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{...})`
- **Middleware**: HTTP request logging handled by `middleware/logging.go`

---

## Log Levels

| Level | When to Use | Example |
|-------|-------------|---------|
| `DEBUG` | Development-only diagnostics. Strip in production. | "ssh pool: acquired connection 3/10" |
| `INFO` | Normal operational events. Low volume. | "user alice executed command on web-01", "agent registered" |
| `WARN` | Recoverable issues. Degraded but working. | "ssh reconnecting to server-3 (attempt 2/3)", "rate limit hit for user X" |
| `ERROR` | Operation failed. Needs attention. | "ssh authentication failed for server-5", "db migration rollback failed" |

### Level Decision Tree

```
Is the operation successful?
├── YES → INFO (if noteworthy) or DEBUG (if routine)
└── NO
    ├── Will retry and likely succeed? → WARN
    └── Permanent failure? → ERROR
```

---

## Structured Logging Format

Every log entry includes a base set of attributes, injected by the logging middleware:

```json
{
    "time": "2026-07-03T14:30:00Z",
    "level": "INFO",
    "msg": "command executed",
    "request_id": "req_abc123",
    "user_id": 42,
    "server_id": 7,
    "command": "systemctl restart nginx",
    "duration_ms": 342
}
```

### Required Attributes per Event Type

| Event | Required Attributes |
|-------|-------------------|
| HTTP request | `method`, `path`, `status`, `duration_ms`, `request_id`, `user_id` (if auth'd) |
| SSH exec | `server_id`, `command`, `duration_ms`, `exit_code` |
| Terminal session | `server_id`, `session_id`, `event` (open/close/resize) |
| Auth | `user_id`, `event` (login/apikey_create/login_failed), `ip_address` |
| Audit | `user_id`, `server_id` (optional), `action`, `detail` (JSON) |

---

## What to Log

- **Auth events**: login success, login failure, API key creation/revocation
- **SSH operations**: command execution, terminal open/close, file transfer start/end
- **Errors**: all ERROR-level events, with full error chain and context
- **Security-relevant**: rate limit hits, unauthorized access attempts, credential access
- **Lifecycle**: server startup, graceful shutdown, agent connect/disconnect

---

## What NOT to Log

- **SSH private keys** or key material — even encrypted
- **Passwords** or password hashes
- **JWT tokens** or API keys in full (log first 4 chars only: `sk_a1b2...`)
- **Command output** — `stdout`/`stderr` may contain sensitive data. Log only exit code and duration.
- **Full request bodies** — log payload size, never the content.

### Sensitive Field Redaction

```go
// Safe: log command without arguments if they might contain secrets
slog.Info("command executed", "server_id", sid, "command", sanitizeCommand(cmd))

func sanitizeCommand(cmd string) string {
    // Truncate to 200 chars. Strip known secret patterns.
    if len(cmd) > 200 {
        return cmd[:200] + "..."
    }
    return cmd
}
```

---

## Initialization

```go
// cmd/server/main.go
func initLogger(cfg config.LogConfig) *slog.Logger {
    opts := &slog.HandlerOptions{
        Level: cfg.Level, // slog.LevelInfo or slog.LevelDebug
    }
    if cfg.Format == "json" {
        return slog.New(slog.NewJSONHandler(os.Stdout, opts))
    }
    return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
```

Set as default so all packages use `slog.InfoContext(ctx, ...)` without passing logger around:

```go
slog.SetDefault(logger)
```

---

## Common Mistakes

- ❌ `log.Println("something happened")` — use `slog` for everything.
- ❌ `slog.Error("failed", "password", pw)` — never log credentials.
- ❌ `slog.Info("success", "stdout", output)` — stdout may contain secrets.
- ❌ Not passing `context.Context` — use `slog.InfoContext(ctx, ...)` so request_id propagates.
- ❌ Logging in service layer — logging is the middleware's job. Service returns errors only.
