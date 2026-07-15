# Quality Guidelines

> Code quality standards for backend development.

---

## Overview

- **Language**: Go 1.23+
- **Linter**: golangci-lint v2 with default + project-specific rules
- **Formatter**: `gofumpt` (stricter gofmt)
- **Testing**: `go test` with table-driven tests + testify
- **CI**: GitHub Actions — lint + test on every PR

---

## Linting Rules

Run before every commit:

```bash
golangci-lint run ./...
```

`.golangci.yml` enables:

```yaml
linters:
  enable:
    - errcheck       # check unchecked errors
    - gosimple       # simplify code
    - govet          # go vet
    - ineffassign    # detect ineffectual assignments
    - staticcheck    # static analysis
    - unused         # unused variables/functions
    - gofumpt        # stricter formatting
    - revive         # style linter
    - goconst        # repeated strings → constants
    - gocyclo        # cyclomatic complexity (max 15)
    - nilnil         # return (nil, nil) detection
    - exhaustruct    # enforce struct field initialization

linters-settings:
  gocyclo:
    min-complexity: 15
  revive:
    rules:
      - name: exported
        arguments: ["checkPrivateReceivers"]
      - name: context-as-argument
```

---

## Forbidden Patterns

### Never

- ❌ `panic()` outside of `main()` or `init()` — always return errors.
- ❌ `interface{}` / `any` — use generics or concrete types.
- ❌ `_ = err` to ignore errors — if intentional, comment why: `// err always nil here because...`.
- ❌ Global mutable state (`var db *gorm.DB` at package level) — use dependency injection.
- ❌ `time.Sleep()` in tests — use `time.Ticker` or mock clock.
- ❌ Cgo dependencies — keep binary static, portable.
- ❌ `goto` or labeled breaks that jump more than one level.
- ❌ Raw string concatenation for SQL — always use GORM parameterized queries or `database/sql` placeholders.
- ❌ Returning `(nil, nil)` from a function that returns `(*T, error)` — caller can't distinguish "empty" from "error".

### Always

- ✅ `context.Context` as first parameter in every function that does I/O.
- ✅ `defer` for resource cleanup (file handles, SSH sessions, DB transactions).
- ✅ `errors.Is()` / `errors.As()` for error type checks.
- ✅ Table-driven tests (see Testing Requirements below).
- ✅ Dependency injection via constructor functions (`NewServerService(repo, ...)` not `&ServerService{...}`).

---

## Testing Requirements

### Coverage Target

| Layer | Minimum Coverage | Notes |
|-------|:---:|-------|
| `internal/service/` | 80% | Core business logic must be well-tested |
| `internal/handler/` | 60% | Integration-test heavy, unit test the happy path |
| `internal/repository/` | 60% | Test with testcontainers or sqlite in-memory |
| `internal/pkg/` | 90% | Utility packages, small surface area |

### Table-Driven Tests (required)

```go
func TestExecCommand(t *testing.T) {
    tests := []struct {
        name    string
        cmd     string
        wantOut string
        wantErr bool
    }{
        {"simple echo", "echo hello", "hello\n", false},
        {"invalid command", "nonexistent_cmd", "", true},
        {"empty command", "", "", true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            out, err := service.ExecCommand(ctx, serverID, tt.cmd)
            if tt.wantErr {
                assert.Error(t, err)
            } else {
                assert.NoError(t, err)
                assert.Equal(t, tt.wantOut, out)
            }
        })
    }
}
```

### Testing Rules

- File naming: `*_test.go` alongside the source, never in a separate `test/` package.
- Use `testify/assert` for assertions, `testify/require` for preconditions.
- Mocks go in `internal/test/mocks/` and are generated — don't hand-write mocks. Use `mockery`.
- SSH service tests: use a real SSH server in a testcontainer. Do not mock `crypto/ssh`.
- Database tests: use testcontainers-go to spin up a real PostgreSQL. No sqlite fallback (GORM dialects differ).

---

## Code Review Checklist

Before marking a PR ready for review:

- [ ] `golangci-lint run ./...` passes with zero warnings
- [ ] `go test ./...` passes
- [ ] New functions/methods have table-driven tests
- [ ] No `panic()` outside main/init
- [ ] All errors wrapped with context (`fmt.Errorf("doing X: %w", err)`)
- [ ] SSH credentials are never logged
- [ ] Database queries use parameterized inputs (no string concat)
- [ ] New API routes have audit log entries
- [ ] Middleware is registered in the correct order in `router.go`
- [ ] Dockerfile builds without warnings

---

## Dependency Injection Pattern

All services and repositories follow constructor injection:

```go
// Correct
type ServerService struct {
    repo   repository.ServerRepo
    sshSvc *SSHService
    audit  *AuditService
}

func NewServerService(repo repository.ServerRepo, sshSvc *SSHService, audit *AuditService) *ServerService {
    return &ServerService{repo: repo, sshSvc: sshSvc, audit: audit}
}

// Wrong — field injection, hidden dependencies
type ServerService struct {
    Repo   repository.ServerRepo
    SSHSvc *SSHService
}
```

---

## Package Visibility

- `internal/` packages are importable only within the module.
- `cmd/` packages cannot be imported by any other package.
- No circular imports — golangci-lint catches this, but design your interfaces to avoid needing it.
- Prefer small, focused packages over large `util` / `common` catch-alls.
