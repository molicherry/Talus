package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// Stable, machine-readable error reasons.
//
// Every error the API returns carries one, so a client can translate it instead
// of showing the English `message` verbatim. `message` stays English for logs
// and API agents. The frontend mirrors these in its locales as
// `errors.<reason>`; frontend/tests/error-reasons.test.mjs fails if the two
// sides drift, so a new reason cannot ship untranslated.
const (
	ReasonSftpInvalidUpload       = "sftp_invalid_upload"
	ReasonSftpUploadExpired       = "sftp_upload_expired"
	ReasonSftpChecksumMismatch    = "sftp_checksum_mismatch"
	ReasonSftpTextTooLarge        = "sftp_text_too_large"
	ReasonSftpNotText             = "sftp_not_text"
	ReasonSftpNotDirectory        = "sftp_not_directory"
	ReasonSftpInvalidRange        = "sftp_invalid_range"
	ReasonSftpInvalidPath         = "sftp_invalid_path"
	ReasonSftpNotFound            = "sftp_not_found"
	ReasonSftpPermissionDenied    = "sftp_permission_denied"
	ReasonSftpExists              = "sftp_exists"
	ReasonSftpConflict            = "sftp_conflict"
	ReasonSftpNotEmpty            = "sftp_not_empty"
	ReasonSftpBusy                = "sftp_busy"
	ReasonSftpTooLarge            = "sftp_too_large"
	ReasonSftpDirectoryTooLarge   = "sftp_directory_too_large"
	ReasonSftpUnavailable         = "sftp_unavailable"
	ReasonSftpFailed              = "sftp_failed"
	ReasonSftpNotRegular          = "sftp_not_regular"
	ReasonSftpAtomicUnsupported   = "sftp_atomic_unsupported"
	ReasonSftpSizeMismatch        = "sftp_size_mismatch"
	ReasonUnauthorized            = "unauthorized"
	ReasonInvalidCredentials      = "invalid_credentials"
	ReasonWrongCurrentPassword    = "current_password_incorrect"
	ReasonForbidden               = "forbidden"
	ReasonUnavailable             = "unavailable"
	ReasonNotFound                = "not_found"
	ReasonConflict                = "conflict"
	ReasonValidationFailed        = "validation_failed"
	ReasonInternal                = "internal_error"
	ReasonRateLimited             = "rate_limited"
	ReasonInvalidRequest          = "invalid_request"
	ReasonInvalidServerID         = "invalid_server_id"
	ReasonInvalidServiceID        = "invalid_service_id"
	ReasonInvalidCredentialID     = "invalid_credential_id"
	ReasonInvalidKeyID            = "invalid_key_id"
	ReasonInvalidRelayRequest     = "invalid_relay_request"
	ReasonInvalidRelayPath        = "invalid_relay_path"
	ReasonInvalidScopes           = "invalid_scopes"
	ReasonScopesRequired          = "scopes_required"
	ReasonServersNotFound         = "servers_not_found"
	ReasonMethodRequired          = "method_required"
	ReasonMetricsQueryBudget      = "metrics_query_budget_exceeded"
	ReasonMetricsRollupNotReady   = "metrics_rollup_not_ready"
	ReasonMetricsQueryTimeout     = "metrics_query_timeout"
	ReasonCommandRequired         = "command_required"
	ReasonServiceNotFound         = "service_not_found"
	ReasonCredentialNotFound      = "credential_not_found"
	ReasonAPIKeyNotFound          = "api_key_not_found"
	ReasonRawKeyUnavailable       = "raw_key_unavailable"
	ReasonCredentialDecrypt       = "credential_decrypt_failed"
	ReasonAPIKeyServerDenied      = "api_key_server_denied"
	ReasonAPIKeyServiceDenied     = "api_key_service_denied"
	ReasonRelayTimeout            = "relay_timeout"
	ReasonRelayUnreachable        = "relay_unreachable"
	ReasonInvalidQuery            = "invalid_query"
	ReasonInvalidCursor           = "invalid_cursor"
	ReasonCursorFilterMismatch    = "cursor_filter_mismatch"
	ReasonCursorExpired           = "cursor_expired"
	ReasonSSHConnection           = "ssh_connection_failed"
	ReasonSSHAuth                 = "ssh_authentication_failed"
	ReasonSSHTimeout              = "ssh_timeout"
	ReasonSSHHostKeyMismatch      = "ssh_host_key_mismatch"
	ReasonSSHHostKeyUnverified    = "ssh_host_key_unverified"
	ReasonNoHostKeyMismatch       = "no_host_key_mismatch"
	ReasonHostKeyChanged          = "host_key_changed"
	ReasonInvalidConnection       = "invalid_connection_config"
	ReasonConnectionConfigChanged = "connection_config_changed"
	ReasonConnectionInUse         = "connection_in_use"
	ReasonProxyConnection         = "proxy_connection_failed"
	ReasonProxyAuth               = "proxy_auth_failed"
	ReasonProxyConnectRejected    = "proxy_connect_rejected"
	ReasonJumpForwardFailed       = "jump_forward_failed"

	// Field-level validation reasons. These render through {{params}} on both
	// sides so the numbers live in one place.
	ReasonRequired       = "required"
	ReasonLength         = "length"
	ReasonMaxLength      = "max_length"
	ReasonAtLeastOne     = "at_least_one_required"
	ReasonPasswordAuth   = "password_required_for_auth_type"
	ReasonPrivateKeyAuth = "private_key_required_for_auth_type"
)

// reasonMessages holds the English text for every reason — the single place the
// API's untranslated wording lives. {{name}} placeholders are filled from
// Params, both here and (independently translated) on the client.
var reasonMessages = map[string]string{
	ReasonSftpInvalidUpload:    "Invalid upload session or chunk",
	ReasonSftpUploadExpired:    "The upload session expired; start a new upload",
	ReasonSftpChecksumMismatch: "File checksum mismatch; the destination was not replaced",
	ReasonSftpTextTooLarge:     "Text editing is limited to 1 MiB",
	ReasonSftpNotText:          "This file is not UTF-8 text",
	ReasonSftpNotDirectory:     "This operation requires a directory",
	ReasonSftpInvalidRange:     "The requested download range is invalid",

	ReasonSftpInvalidPath:       "Invalid remote path",
	ReasonSftpNotFound:          "Remote file or directory not found",
	ReasonSftpPermissionDenied:  "The SSH account cannot access this file",
	ReasonSftpExists:            "The destination already exists",
	ReasonSftpConflict:          "The file or SSH connection changed; refresh and try again",
	ReasonSftpNotEmpty:          "The directory is not empty",
	ReasonSftpBusy:              "Another file operation is running on this server",
	ReasonSftpTooLarge:          "The file exceeds the 64 MiB limit",
	ReasonSftpDirectoryTooLarge: "The directory exceeds the listing limit",
	ReasonSftpUnavailable:       "The server did not enable the SFTP subsystem",
	ReasonSftpFailed:            "The SFTP operation failed",
	ReasonSftpNotRegular:        "This operation requires a regular file",
	ReasonSftpAtomicUnsupported: "The server does not support safe replacement; choose a new filename",
	ReasonSftpSizeMismatch:      "The upload size did not match; the original file was preserved",

	ReasonUnauthorized:            "unauthorized",
	ReasonInvalidCredentials:      "invalid username or password",
	ReasonWrongCurrentPassword:    "current password is incorrect",
	ReasonForbidden:               "forbidden",
	ReasonUnavailable:             "authentication temporarily unavailable",
	ReasonNotFound:                "resource not found",
	ReasonConflict:                "resource conflict",
	ReasonValidationFailed:        "validation failed",
	ReasonInternal:                "internal server error",
	ReasonRateLimited:             "too many requests",
	ReasonInvalidRequest:          "invalid request body",
	ReasonInvalidServerID:         "invalid server id",
	ReasonInvalidServiceID:        "invalid service id",
	ReasonInvalidCredentialID:     "invalid credential id",
	ReasonInvalidKeyID:            "invalid key id",
	ReasonInvalidRelayRequest:     "invalid relay request",
	ReasonInvalidRelayPath:        "invalid relay path",
	ReasonInvalidScopes:           "invalid scopes: {{scopes}}",
	ReasonScopesRequired:          "at least one scope is required",
	ReasonServersNotFound:         "unknown server ids: {{servers}}",
	ReasonMethodRequired:          "method is required",
	ReasonMetricsQueryBudget:      "the requested range needs a coarser interval",
	ReasonMetricsRollupNotReady:   "metrics rollup is not ready for this range",
	ReasonMetricsQueryTimeout:     "metrics query exceeded its deadline",
	ReasonCommandRequired:         "command is required",
	ReasonServiceNotFound:         "service not found",
	ReasonCredentialNotFound:      "credential not found",
	ReasonAPIKeyNotFound:          "api key not found",
	ReasonRawKeyUnavailable:       "raw key not available",
	ReasonCredentialDecrypt:       "credential decryption failed",
	ReasonAPIKeyServerDenied:      "access denied: api key does not have access to this server",
	ReasonAPIKeyServiceDenied:     "access denied: api key does not have access to the service's server",
	ReasonRelayTimeout:            "target service timeout",
	ReasonRelayUnreachable:        "target service unreachable: {{detail}}",
	ReasonInvalidQuery:            "invalid query parameters: {{detail}}",
	ReasonInvalidCursor:           "invalid usage log cursor",
	ReasonCursorFilterMismatch:    "cursor filters do not match this query",
	ReasonCursorExpired:           "usage log cursor expired; refresh the list",
	ReasonSSHConnection:           "ssh connection failed",
	ReasonSSHAuth:                 "ssh authentication failed",
	ReasonSSHTimeout:              "ssh command timed out",
	ReasonSSHHostKeyMismatch:      "ssh host key changed: verify the new fingerprint before trusting it",
	ReasonSSHHostKeyUnverified:    "verify and confirm the SSH host key before connecting",
	ReasonNoHostKeyMismatch:       "no pending host key change to trust",
	ReasonHostKeyChanged:          "the pending host key changed again: re-verify the new fingerprint",
	ReasonInvalidConnection:       "invalid server connection configuration",
	ReasonConnectionConfigChanged: "connection configuration changed; reload and retry",
	ReasonConnectionInUse:         "server is used as an SSH jump host; update dependent servers first",
	ReasonProxyConnection:         "cannot connect to the proxy",
	ReasonProxyAuth:               "proxy authentication failed",
	ReasonProxyConnectRejected:    "proxy refused the target connection",
	ReasonJumpForwardFailed:       "SSH jump host could not forward to the target",

	ReasonRequired:       "{{field}} is required",
	ReasonLength:         "must be between {{min}} and {{max}} characters",
	ReasonMaxLength:      "must be at most {{max}} characters",
	ReasonAtLeastOne:     "at least one {{field}} is required",
	ReasonPasswordAuth:   "password is required for auth_type 'password'",
	ReasonPrivateKeyAuth: "private_key is required for auth_type 'private_key'",
}

// interpolate fills {{name}} placeholders; unknown names are left untouched.
func interpolate(tpl string, params map[string]any) string {
	if len(params) == 0 || !strings.Contains(tpl, "{{") {
		return tpl
	}
	for name, value := range params {
		tpl = strings.ReplaceAll(tpl, "{{"+name+"}}", fmt.Sprint(value))
	}
	return tpl
}

// textFor renders the English message for a reason, falling back to the reason
// itself so a half-added reason is still visible rather than blank.
func textFor(reason string, params map[string]any) string {
	tpl, ok := reasonMessages[reason]
	if !ok {
		return reason
	}
	return interpolate(tpl, params)
}

// ErrorDetail describes a single field-level validation error.
type ErrorDetail struct {
	Field   string         `json:"field"`
	Reason  string         `json:"reason"`
	Message string         `json:"message"`
	Params  map[string]any `json:"params,omitempty"`
}

// NewErrorDetail builds a field-level detail from a reason, rendering the
// English message from the reason table.
func NewErrorDetail(field, reason string, params map[string]any) ErrorDetail {
	if params == nil {
		params = map[string]any{}
	}
	if _, ok := params["field"]; !ok {
		params["field"] = field
	}
	return ErrorDetail{
		Field:   field,
		Reason:  reason,
		Message: textFor(reason, params),
		Params:  params,
	}
}

// AppError is a structured application error with an HTTP status code and a
// stable reason.
type AppError struct {
	Code    int            `json:"code"`
	Reason  string         `json:"reason"`
	Message string         `json:"message"`
	Params  map[string]any `json:"params,omitempty"`
	Err     error          `json:"-"`
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%d] %s (%s): %v", e.Code, e.Message, e.Reason, e.Err)
	}
	return fmt.Sprintf("[%d] %s (%s)", e.Code, e.Message, e.Reason)
}

func (e *AppError) Unwrap() error {
	return e.Err
}

// ValidationError extends AppError with field-level detail.
type ValidationError struct {
	AppError
	Details []ErrorDetail `json:"details,omitempty"`
}

// Sentinel errors.
var (
	ErrUnauthorized         = &AppError{Code: http.StatusUnauthorized, Reason: ReasonUnauthorized, Message: reasonMessages[ReasonUnauthorized]}
	ErrInvalidCredentials   = &AppError{Code: http.StatusUnauthorized, Reason: ReasonInvalidCredentials, Message: reasonMessages[ReasonInvalidCredentials]}
	ErrWrongCurrentPassword = &AppError{Code: http.StatusUnauthorized, Reason: ReasonWrongCurrentPassword, Message: reasonMessages[ReasonWrongCurrentPassword]}
	ErrForbidden            = &AppError{Code: http.StatusForbidden, Reason: ReasonForbidden, Message: reasonMessages[ReasonForbidden]}
	ErrNotFound             = &AppError{Code: http.StatusNotFound, Reason: ReasonNotFound, Message: reasonMessages[ReasonNotFound]}
	ErrConflict             = &AppError{Code: http.StatusConflict, Reason: ReasonConflict, Message: reasonMessages[ReasonConflict]}
	ErrValidation           = &AppError{Code: http.StatusUnprocessableEntity, Reason: ReasonValidationFailed, Message: reasonMessages[ReasonValidationFailed]}
	ErrInternal             = &AppError{Code: http.StatusInternalServerError, Reason: ReasonInternal, Message: reasonMessages[ReasonInternal]}
	ErrSSHConnection        = &AppError{Code: http.StatusBadGateway, Reason: ReasonSSHConnection, Message: reasonMessages[ReasonSSHConnection]}
	ErrSSHAuth              = &AppError{Code: http.StatusBadGateway, Reason: ReasonSSHAuth, Message: reasonMessages[ReasonSSHAuth]}
	ErrSSHTimeout           = &AppError{Code: http.StatusGatewayTimeout, Reason: ReasonSSHTimeout, Message: reasonMessages[ReasonSSHTimeout]}
	ErrSSHHostKeyMismatch   = &AppError{Code: http.StatusBadGateway, Reason: ReasonSSHHostKeyMismatch, Message: reasonMessages[ReasonSSHHostKeyMismatch]}
	ErrSSHHostKeyUnverified = &AppError{Code: http.StatusConflict, Reason: ReasonSSHHostKeyUnverified, Message: reasonMessages[ReasonSSHHostKeyUnverified]}
)

// NewAppError creates an AppError for the given status and reason. The message
// is the English fallback for that reason; clients should translate the reason.
func NewAppError(code int, reason string) *AppError {
	return &AppError{Code: code, Reason: reason, Message: textFor(reason, nil)}
}

// NewAppErrorParams is NewAppError with values interpolated into the English
// message (e.g. which ids were unknown).
func NewAppErrorParams(code int, reason string, params map[string]any) *AppError {
	return &AppError{Code: code, Reason: reason, Message: textFor(reason, params), Params: params}
}

// NewValidationError creates a ValidationError from field details.
func NewValidationError(details []ErrorDetail) *ValidationError {
	return &ValidationError{
		AppError: AppError{
			Code:    http.StatusUnprocessableEntity,
			Reason:  ReasonValidationFailed,
			Message: reasonMessages[ReasonValidationFailed],
		},
		Details: details,
	}
}

// StatusCode extracts the HTTP status code from an error, defaulting to 500.
func StatusCode(err error) int {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	var valErr *ValidationError
	if errors.As(err, &valErr) {
		return valErr.Code
	}
	return http.StatusInternalServerError
}

// errorResponse is the JSON envelope for error responses.
type errorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      int            `json:"code"`
	Reason    string         `json:"reason"`
	Message   string         `json:"message"`
	Params    map[string]any `json:"params,omitempty"`
	Details   []ErrorDetail  `json:"details,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
}

// marshalError serializes an error into the JSON error envelope.
func marshalError(err error, requestID string) []byte {
	code := StatusCode(err)
	msg := err.Error()

	body := errorBody{
		Code:      code,
		Reason:    ReasonInternal,
		Message:   msg,
		RequestID: requestID,
	}

	var appErr *AppError
	if errors.As(err, &appErr) {
		body.Reason = appErr.Reason
		body.Message = appErr.Message
		body.Params = appErr.Params
	}
	var valErr *ValidationError
	if errors.As(err, &valErr) {
		body.Reason = valErr.Reason
		body.Message = valErr.Message
		body.Details = valErr.Details
	}
	if body.Reason == "" {
		body.Reason = ReasonInternal
	}

	data, err := json.Marshal(errorResponse{Error: body})
	if err != nil {
		slog.Error("failed to marshal error response", "error", err)
		return []byte(fmt.Sprintf(`{"error":{"code":500,"reason":%q,"message":"internal server error"}}`, ReasonInternal))
	}
	return data
}
