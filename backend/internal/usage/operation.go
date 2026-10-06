// Package usage captures bounded, best-effort operation summaries. It never
// receives command text, decrypted credentials, bodies, or terminal frames.
package usage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vpsmanager/backend/internal/model"
)

type Principal struct {
	AuthType     string
	UserID       *uint
	Username     string
	APIKeyID     *uint
	APIKeyName   string
	APIKeyPrefix string
	Role         string
	ServerIDs    []uint
	Scopes       []string
}

type contextKey struct{}

func WithOperation(ctx context.Context, op *Operation) context.Context {
	return context.WithValue(ctx, contextKey{}, op)
}
func FromContext(ctx context.Context) *Operation {
	op, _ := ctx.Value(contextKey{}).(*Operation)
	return op
}

// Operation is shared by outer middleware, authentication, and handlers. Its
// sequence and final snapshot are protected by one lock.
type Operation struct {
	mu             sync.Mutex
	row            model.UsageLog
	principal      Principal
	started        time.Time
	metadata       map[string]any
	finished       bool
	beginAttempted bool
	captureDecided bool
	admitted       bool
	recorder       *Recorder
}

func NewOperation(action, route, requestID, method, client string) *Operation {
	now := time.Now()
	if host, _, err := net.SplitHostPort(client); err == nil {
		client = host
	}
	if net.ParseIP(client) == nil {
		client = ""
	}
	op := &Operation{started: now, metadata: map[string]any{}, principal: Principal{AuthType: "unauthenticated"}}
	op.row = model.UsageLog{OperationID: newUUID(), Action: boundedText(action, 96), RoutePattern: boundedText(route, 256), RequestID: boundedText(requestID, 128), Method: safeMethod(method), ClientAddress: client, StartedAt: now.UTC(), RecordedAt: now.UTC(), AuthType: "unauthenticated", Source: "operation", Outcome: "running", Phase: "preparing", StateSeq: 1}
	return op
}

func (op *Operation) OperationID() string {
	if op == nil {
		return ""
	}
	return op.row.OperationID
}
func (op *Operation) Begin() bool {
	if op == nil || op.recorder == nil {
		return false
	}
	return op.recorder.Begin(op)
}
func (op *Operation) WriteAudit(ctx context.Context, event *model.AuditEvent) error {
	if op == nil || op.recorder == nil {
		return ErrRecorderUnavailable
	}
	id := op.OperationID()
	event.OperationID = &id
	return op.recorder.WriteAudit(ctx, event)
}

// Principal returns a copy; API key IDs and owners are deliberately separate.
func (op *Operation) Principal() Principal {
	if op == nil {
		return Principal{AuthType: "unauthenticated"}
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	return clonePrincipal(op.principal)
}
func (op *Operation) SetPrincipal(p Principal) {
	if op == nil || (p.AuthType != "jwt" && p.AuthType != "api_key") {
		return
	}
	op.mu.Lock()
	if op.finished || op.row.AuthType != "unauthenticated" {
		op.mu.Unlock()
		return
	}
	p.UserID = copyPtr(p.UserID)
	p.APIKeyID = copyPtr(p.APIKeyID)
	if p.UserID != nil && *p.UserID == 0 {
		p.UserID = nil
	}
	if p.APIKeyID != nil && *p.APIKeyID == 0 {
		p.APIKeyID = nil
	}
	if p.AuthType == "jwt" {
		p.APIKeyID = nil
		p.APIKeyName = ""
		p.APIKeyPrefix = ""
	}
	p.Username = boundedText(p.Username, 256)
	p.APIKeyName = boundedText(p.APIKeyName, 256)
	p.APIKeyPrefix = boundedText(p.APIKeyPrefix, 32)
	op.principal = clonePrincipal(p)
	op.row.AuthType = p.AuthType
	op.row.UserID = p.UserID
	op.row.UsernameSnapshot = p.Username
	op.row.APIKeyID = p.APIKeyID
	op.row.APIKeyNameSnapshot = p.APIKeyName
	op.row.APIKeyPrefixSnapshot = p.APIKeyPrefix
	op.row.StateSeq++
	recorder := op.recorder
	op.mu.Unlock()
	if recorder != nil {
		recorder.AdmitShort(op)
	}
}
func (op *Operation) SetResource(kind string, id *uint, name string, serverID *uint) {
	if op == nil {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.finished {
		return
	}
	op.row.ResourceType = boundedText(kind, 64)
	op.row.ResourceID = copyPtr(id)
	if op.row.ResourceID != nil && *op.row.ResourceID == 0 {
		op.row.ResourceID = nil
	}
	op.row.ResourceNameSnapshot = boundedText(name, 256)
	op.row.ServerID = copyPtr(serverID)
	if op.row.ServerID != nil && *op.row.ServerID == 0 {
		op.row.ServerID = nil
	}
	op.row.StateSeq++
}
func (op *Operation) SetResult(outcome, reason string) {
	if op == nil || !validFinal(outcome) {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.finished {
		return
	}
	op.row.Outcome = outcome
	op.row.ErrorReason = safeReason(reason)
	op.row.StateSeq++
}
func (op *Operation) SetHTTPStatus(status int) {
	if op == nil || status < 100 || status > 599 {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.finished {
		return
	}
	op.row.HTTPStatus = &status
	op.row.StateSeq++
}

// SetHTTPError supplies a transport fallback while preserving an explicit
// business result (for example a committed mutation whose response failed).
func (op *Operation) SetHTTPError(status int, reason string) {
	if op == nil {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.finished {
		return
	}
	if op.row.Outcome != "running" {
		if op.row.Outcome == "succeeded" {
			op.metadata["response_write_failed"] = true
		}
		return
	}
	if status >= 500 {
		op.row.Outcome = "failed"
	} else if status >= 400 {
		op.row.Outcome = "rejected"
	} else {
		return
	}
	op.row.ErrorReason = safeReason(reason)
	op.row.StateSeq++
}
func (op *Operation) SetExecResult(code *int) {
	if op == nil {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.finished {
		return
	}
	op.row.ExitCode = copyPtr(code)
	op.row.StateSeq++
}
func (op *Operation) SetRelayResult(status int, bytes int64) {
	if op == nil {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.finished {
		return
	}
	if status >= 100 && status <= 599 {
		op.row.UpstreamStatus = &status
	}
	if bytes >= 0 {
		op.metadata["bytes_copied"] = bytes
	}
	op.row.StateSeq++
}
func (op *Operation) SetPhase(phase string) {
	if op == nil || (phase != "preparing" && phase != "handshake" && phase != "authenticated" && phase != "ready") {
		return
	}
	op.mu.Lock()
	if op.finished || op.row.Phase == phase {
		op.mu.Unlock()
		return
	}
	op.row.Phase = phase
	op.row.StateSeq++
	if phase != "preparing" {
		key := phase + "_at"
		if _, exists := op.metadata[key]; !exists {
			op.metadata[key] = time.Now().UTC().Format(time.RFC3339Nano)
		}
	}
	admitted := op.admitted
	recorder := op.recorder
	op.mu.Unlock()
	if admitted && recorder != nil {
		recorder.schedulePhase(op.OperationID())
	}
}
func (op *Operation) SetMetadata(key string, value any) {
	if op == nil {
		return
	}
	normalized, ok := normalizeMetadata(key, value)
	if !ok {
		return
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.finished {
		return
	}
	op.metadata[key] = normalized
	op.row.StateSeq++
}

func (op *Operation) snapshot() model.UsageLog {
	op.mu.Lock()
	defer op.mu.Unlock()
	return op.snapshotLocked()
}
func (op *Operation) snapshotLocked() model.UsageLog {
	row := op.row
	// Business facts may be supplied before transport teardown. Only Finish
	// turns them into a persisted terminal state with its true end time.
	if !op.finished {
		row.Outcome = "running"
		row.ErrorReason = ""
	}
	row.ResourceID = copyPtr(row.ResourceID)
	row.ServerID = copyPtr(row.ServerID)
	row.UserID = copyPtr(row.UserID)
	row.APIKeyID = copyPtr(row.APIKeyID)
	row.HTTPStatus = copyPtr(row.HTTPStatus)
	row.UpstreamStatus = copyPtr(row.UpstreamStatus)
	row.ExitCode = copyPtr(row.ExitCode)
	row.OwnerInstanceID = copyPtr(row.OwnerInstanceID)
	row.FinishedAt = copyPtr(row.FinishedAt)
	row.DurationMS = copyPtr(row.DurationMS)
	row.RetentionAt = copyPtr(row.RetentionAt)
	md := make(map[string]any, len(op.metadata)+1)
	md["version"] = 1
	for key, value := range op.metadata {
		md[key] = value
	}
	data, _ := json.Marshal(md)
	if len(data) > 4096 {
		data = []byte(`{"version":1}`)
	}
	row.Metadata = data
	return row
}
func (op *Operation) finish(status int) (model.UsageLog, bool) {
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.finished {
		return model.UsageLog{}, false
	}
	op.finished = true
	if op.row.HTTPStatus == nil && status >= 100 && status <= 599 {
		op.row.HTTPStatus = &status
	}
	if op.row.Outcome == "running" {
		op.row.Outcome = "succeeded"
		if status >= 500 {
			op.row.Outcome = "failed"
			op.row.ErrorReason = "internal_error"
		} else if status >= 400 {
			op.row.Outcome = "rejected"
			op.row.ErrorReason = "request_rejected"
		}
	}
	now := time.Now().UTC()
	duration := time.Since(op.started).Milliseconds()
	op.row.FinishedAt = &now
	op.row.RetentionAt = &now
	op.row.DurationMS = &duration
	op.row.Phase = "closed"
	op.row.StateSeq++
	op.metadata["closed_at"] = now.Format(time.RFC3339Nano)
	return op.snapshotLocked(), true
}
func (op *Operation) refreshRunning() (model.UsageLog, bool) {
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.finished {
		return model.UsageLog{}, false
	}
	op.row.StateSeq++
	return op.snapshotLocked(), true
}

func clonePrincipal(p Principal) Principal {
	p.UserID = copyPtr(p.UserID)
	p.APIKeyID = copyPtr(p.APIKeyID)
	p.ServerIDs = append([]uint(nil), p.ServerIDs...)
	p.Scopes = append([]string(nil), p.Scopes...)
	return p
}
func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
func validFinal(s string) bool {
	return s == "succeeded" || s == "failed" || s == "rejected" || s == "cancelled" || s == "unknown"
}
func boundedText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto random source unavailable")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:], b[10:])
	return string(out[:])
}
func safeMethod(s string) string {
	switch s {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		return s
	}
	return ""
}
func normalizeMetadata(key string, value any) (any, bool) {
	switch key {
	case "response_write_failed":
		v, ok := value.(bool)
		return v, ok
	case "handshake_at", "authenticated_at", "ready_at", "closed_at":
		var t time.Time
		switch v := value.(type) {
		case time.Time:
			t = v
		case string:
			parsed, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return nil, false
			}
			t = parsed
		default:
			return nil, false
		}
		return t.UTC().Format(time.RFC3339Nano), true
	case "method":
		v, ok := value.(string)
		if !ok || safeMethod(v) == "" {
			return nil, false
		}
		return v, true
	case "close_reason":
		v, ok := value.(string)
		if !ok || safeReason(v) != v {
			return nil, false
		}
		return v, true
	case "timeout_seconds", "exit_code", "bytes_copied", "http_status", "upstream_status":
		var n int64
		switch v := value.(type) {
		case int:
			n = int64(v)
		case int64:
			n = v
		case uint:
			n = int64(v)
		case uint64:
			if v > uint64(^uint64(0)>>1) {
				return nil, false
			}
			n = int64(v)
		default:
			return nil, false
		}
		if key == "timeout_seconds" && (n < 0 || n > 86400) {
			return nil, false
		}
		if key == "bytes_copied" && n < 0 {
			return nil, false
		}
		if (key == "http_status" || key == "upstream_status") && (n < 100 || n > 599) {
			return nil, false
		}
		return n, true
	}
	return nil, false
}
func safeReason(reason string) string {
	if reason == "" {
		return ""
	}
	switch reason {
	case "unauthorized", "invalid_credentials", "current_password_incorrect", "forbidden", "not_found", "conflict", "validation_failed", "internal_error", "rate_limited", "invalid_request", "invalid_server_id", "invalid_service_id", "invalid_credential_id", "invalid_key_id", "invalid_relay_request", "invalid_relay_path", "invalid_scopes", "scopes_required", "servers_not_found", "method_required", "command_required", "service_not_found", "credential_not_found", "api_key_not_found", "raw_key_unavailable", "credential_decrypt_failed", "api_key_server_denied", "api_key_service_denied", "relay_timeout", "relay_unreachable", "invalid_query", "ssh_connection_failed", "ssh_authentication_failed", "ssh_timeout", "ssh_host_key_mismatch", "no_host_key_mismatch", "host_key_changed", "request_rejected", "client_cancelled", "relay_copy_failed", "relay_upstream_failed", "terminal_auth_timeout", "terminal_auth_failed", "terminal_ssh_failed", "terminal_transport_failed", "terminal_closed", "terminal_cancelled", "websocket_upgrade_failed", "ssh_nonzero_exit", "client_closed", "remote_closed", "normal_close", "transport_error", "ssh_error", "server_shutdown":
		return reason
	case "terminal_permission_denied", "terminal_input_failed", "terminal_output_failed", "terminal_resize_failed":
		return reason
	}
	return "internal_error"
}
