# Error Handling

> How errors are handled in this project.

---

## Overview

Go errors are values. This project uses:

- **Sentinel errors** (`var ErrNotFound = ...`) for predictable cases
- **Custom error types** with HTTP status codes baked in
- **Error wrapping** via `fmt.Errorf("%w", err)` for traceability
- **Handler layer** catches everything and writes the standard JSON envelope

No panics in request handlers. No bare `return err` from handler — always wrap into JSON response.

---

## Error Types

Defined in `internal/server/errors.go`:

```go
package server

import "net/http"

// AppError is the base error type with HTTP status code.
type AppError struct {
    Code       int    `json:"code"`        // HTTP status code
    Message    string `json:"message"`     // human-readable, safe for clients
    Err        error  `json:"-"`           // wrapped internal error (never serialized)
    RequestID  string `json:"request_id,omitempty"`
}

func (e *AppError) Error() string {
    if e.Err != nil {
        return fmt.Sprintf("[%d] %s: %v", e.Code, e.Message, e.Err)
    }
    return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error { return e.Err }

// Sentinel errors for common cases.
var (
    ErrUnauthorized   = &AppError{Code: http.StatusUnauthorized, Message: "unauthorized"}
    ErrForbidden      = &AppError{Code: http.StatusForbidden, Message: "forbidden"}
    ErrNotFound       = &AppError{Code: http.StatusNotFound, Message: "resource not found"}
    ErrConflict       = &AppError{Code: http.StatusConflict, Message: "resource already exists"}
    ErrValidation     = &AppError{Code: http.StatusUnprocessableEntity, Message: "validation failed"}
    ErrInternal       = &AppError{Code: http.StatusInternalServerError, Message: "internal server error"}
    ErrSSHConnection  = &AppError{Code: http.StatusBadGateway, Message: "ssh connection failed"}
    ErrSSHTimeout     = &AppError{Code: http.StatusGatewayTimeout, Message: "ssh command timed out"}
)
```

### Usage Pattern

```go
// service layer: return sentinel or wrap with context
func (s *serverService) Get(id uint) (*model.Server, error) {
    server, err := s.repo.Find(ctx, id)
    if errors.Is(err, gorm.ErrRecordNotFound) {
        return nil, fmt.Errorf("server %d: %w", id, ErrNotFound)
    }
    if err != nil {
        return nil, fmt.Errorf("get server %d: %w", id, ErrInternal)
    }
    return server, nil
}

// handler layer: convert to JSON response
func (h *ServerHandler) Get(w http.ResponseWriter, r *http.Request) {
    id := chi.URLParam(r, "id")
    server, err := h.service.Get(parseUint(id))
    if err != nil {
        server.WriteError(w, r, err)  // auto-resolves AppError, sets status code
        return
    }
    server.WriteJSON(w, http.StatusOK, server)
}
```

---

## Error Propagation Chain

```
repository error          →  service wraps with AppError     →  handler delegates to WriteError()
(gorm.ErrRecordNotFound)     (fmt.Errorf("...: %w", ErrNotFound))   (JSON {error:{...}} + correct status)
```

### Chain Rules

| Layer | Responsibility |
|-------|---------------|
| **repository** | Return raw errors (gorm errors, sql errors). Don't wrap. |
| **service** | Wrap into `AppError` sentinel. Add context in Error() string. Don't log. |
| **handler** | Call `server.WriteError(w, r, err)`. Don't inspect error type. Don't log separately (middleware does it). |

---

## API Error Response Format

### Success
```json
{
    "data": { ... },
    "meta": { "total": 42, "page": 1 }
}
```

### Error
```json
{
    "error": {
        "code": 422,
        "message": "validation failed",
        "details": [
            {"field": "username", "message": "must be 3-64 characters"},
            {"field": "role", "message": "must be admin or operator"}
        ],
        "request_id": "req_abc123"
    }
}
```

### WriteError Implementation

```go
// internal/server/response.go
type ErrorResponse struct {
    Error ErrorBody `json:"error"`
}

type ErrorBody struct {
    Code      int              `json:"code"`
    Message   string           `json:"message"`
    Details   []ErrorDetail    `json:"details,omitempty"`
    RequestID string           `json:"request_id,omitempty"`
}

type ErrorDetail struct {
    Field   string `json:"field,omitempty"`
    Message string `json:"message"`
}

func WriteError(w http.ResponseWriter, r *http.Request, err error) {
    var appErr *AppError
    if errors.As(err, &appErr) {
        resp := ErrorResponse{
            Error: ErrorBody{
                Code:      appErr.Code,
                Message:   appErr.Message,
                RequestID: middleware.GetRequestID(r.Context()),
            },
        }
        // Check if it's a validation error with details.
        if ve, ok := err.(*ValidationError); ok {
            resp.Error.Details = ve.Details
        }
        WriteJSON(w, appErr.Code, resp)
        return
    }
    // Unknown error → 500, log the real error internally.
    slog.Error("unhandled error", "error", err, "request_id", middleware.GetRequestID(r.Context()))
    WriteJSON(w, http.StatusInternalServerError, ErrorResponse{
        Error: ErrorBody{Code: 500, Message: "internal server error"},
    })
}
```

---

## Validation Errors

Extended `AppError` with field-level details:

```go
type ValidationError struct {
    AppError
    Details []ErrorDetail
}

func NewValidationError(details []ErrorDetail) *ValidationError {
    return &ValidationError{
        AppError: AppError{Code: http.StatusUnprocessableEntity, Message: "validation failed"},
        Details:  details,
    }
}

// handler usage
func validateExec(req ExecRequest) error {
    var details []ErrorDetail
    if req.ServerID == 0 {
        details = append(details, ErrorDetail{Field: "server_id", Message: "required"})
    }
    if req.Command == "" {
        details = append(details, ErrorDetail{Field: "command", Message: "required"})
    }
    if len(details) > 0 {
        return NewValidationError(details)
    }
    return nil
}
```

---

## SSH-Specific Errors

SSH operations need their own error types because the frontend/Skill needs to distinguish "server unreachable" from "command failed":

```go
// internal/service/ssh_errors.go
var (
    ErrSSHConnection  = &AppError{Code: http.StatusBadGateway,       Message: "cannot connect to server"}
    ErrSSHAuth        = &AppError{Code: http.StatusBadGateway,       Message: "ssh authentication failed"}
    ErrSSHTimeout     = &AppError{Code: http.StatusGatewayTimeout,   Message: "ssh command timed out"}
    ErrSSHExec        = &AppError{Code: http.StatusInternalServerError, Message: "command execution failed"}
)
```

---

---

## Middleware-Level Auth Errors

Auth middleware (`internal/server/middleware/auth.go`) writes errors directly via `writeAuthError()`, NOT through `server.WriteError()`. This is because middleware runs before the handler chain.

```go
// Correct: middleware writes auth errors inline
writeAuthError(w, http.StatusUnauthorized, "invalid api key")
writeAuthError(w, http.StatusForbidden, "insufficient scope: requires servers:write")
writeAuthError(w, http.StatusForbidden, "api key not permitted on this endpoint")
```

Scope check errors use HTTP 403 with message format:
- Missing scope: `"insufficient scope: requires <scope_name>"`
- JWT-only endpoint accessed by API key: `"api key not permitted on this endpoint"`

## Relay Handler: WriteJSON Bypass

The service relay endpoint (`POST /api/v1/services/{id}/relay`) is the **only** handler that does NOT use `WriteJSON` or `WriteError` for success responses. It writes directly to `http.ResponseWriter` to pass through the target service's raw response unchanged.

```go
// internal/handler/service.go — Relay handler
func (h *ServiceHandler) Relay(w http.ResponseWriter, r *http.Request) {
    // ... parse request, validate ...

    relayErr := h.svc.Relay(r.Context(), uint(id), service.RelayInput{...}, w)
    if relayErr != nil {
        server.WriteError(w, r, relayErr)
    }
    // On success: nothing to do — Relay already wrote the response.
}
```

```go
// internal/service/service_relay.go — the passthrough
func (s *ServiceRelayService) Relay(ctx context.Context, serviceID uint, input RelayInput, w http.ResponseWriter) error {
    // ... decrypt, build request, execute ...

    // Bypass WriteJSON — write raw downstream response directly.
    copyHeaders(w.Header(), resp.Header)
    w.WriteHeader(resp.StatusCode)
    io.Copy(w, resp.Body)
    return nil
}
```

### Rules for Relay Endpoints

- **Success path**: service writes status code, Content-Type, and body directly to `ResponseWriter`. The handler does NOT call `WriteJSON`.
- **Error path**: service returns an `AppError`, handler calls `WriteError` normally.
- **NEVER** wrap a relay response in `{"data": ...}`. The client expects the raw downstream payload.
- **NEVER** call `WriteJSON` or `WriteError` on the success path of a relay handler. Doing so would double-write headers and panic.

### Why This Exists

Relay endpoints proxy to arbitrary external services (Grafana, Prometheus, etc.). Each target has its own response format. Wrapping in a JSON envelope would break clients that expect `Content-Type: text/html`, `image/png`, or vendor-specific JSON shapes.

### Common Mistake

```go
// ❌ WRONG: wrapping a relay response in the JSON envelope
resp, _ := http.DefaultClient.Do(proxyReq)
server.WriteJSON(w, resp.StatusCode, resp.Body) // destroys Content-Type, corrupts binary responses

// ✅ CORRECT: passthrough
copyHeaders(w.Header(), resp.Header)
w.WriteHeader(resp.StatusCode)
io.Copy(w, resp.Body)
```

---

## Common Mistakes

- ❌ `return err` from handler — must call `WriteError(w, r, err)`.
- ❌ `log.Fatal()` or `panic()` in a handler — let the error propagate.
- ❌ Leaking internal error messages to the API response (DB connection strings, file paths).
- ❌ Checking `err.Error()` string — always use `errors.Is()` or `errors.As()`.
- ❌ Returning `AppError` from repository — repository returns raw errors; service wraps.
- ❌ `log.Println(err)` then `return err` — logging happens once in middleware, don't double-log.
