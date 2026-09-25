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
	ReasonUnauthorized         = "unauthorized"
	ReasonInvalidCredentials   = "invalid_credentials"
	ReasonWrongCurrentPassword = "current_password_incorrect"
	ReasonForbidden            = "forbidden"
	ReasonNotFound             = "not_found"
	ReasonConflict             = "conflict"
	ReasonValidationFailed     = "validation_failed"
	ReasonInternal             = "internal_error"
	ReasonRateLimited          = "rate_limited"
	ReasonInvalidRequest       = "invalid_request"
	ReasonInvalidServerID      = "invalid_server_id"
	ReasonInvalidServiceID     = "invalid_service_id"
	ReasonInvalidCredentialID  = "invalid_credential_id"
	ReasonInvalidKeyID         = "invalid_key_id"
	ReasonInvalidRelayRequest  = "invalid_relay_request"
	ReasonInvalidRelayPath     = "invalid_relay_path"
	ReasonInvalidScopes        = "invalid_scopes"
	ReasonScopesRequired       = "scopes_required"
	ReasonServersNotFound      = "servers_not_found"
	ReasonMethodRequired       = "method_required"
	ReasonCommandRequired      = "command_required"
	ReasonServiceNotFound      = "service_not_found"
	ReasonCredentialNotFound   = "credential_not_found"
	ReasonAPIKeyNotFound       = "api_key_not_found"
	ReasonRawKeyUnavailable    = "raw_key_unavailable"
	ReasonCredentialDecrypt    = "credential_decrypt_failed"
	ReasonAPIKeyServerDenied   = "api_key_server_denied"
	ReasonAPIKeyServiceDenied  = "api_key_service_denied"
	ReasonRelayTimeout         = "relay_timeout"
	ReasonRelayUnreachable     = "relay_unreachable"
	ReasonInvalidQuery         = "invalid_query"
	ReasonSSHConnection        = "ssh_connection_failed"
	ReasonSSHAuth              = "ssh_authentication_failed"
	ReasonSSHTimeout           = "ssh_timeout"
	ReasonSSHHostKeyMismatch   = "ssh_host_key_mismatch"
	ReasonNoHostKeyMismatch    = "no_host_key_mismatch"
	ReasonHostKeyChanged       = "host_key_changed"

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
	ReasonUnauthorized:         "unauthorized",
	ReasonInvalidCredentials:   "invalid username or password",
	ReasonWrongCurrentPassword: "current password is incorrect",
	ReasonForbidden:            "forbidden",
	ReasonNotFound:             "resource not found",
	ReasonConflict:             "resource conflict",
	ReasonValidationFailed:     "validation failed",
	ReasonInternal:             "internal server error",
	ReasonRateLimited:          "too many requests",
	ReasonInvalidRequest:       "invalid request body",
	ReasonInvalidServerID:      "invalid server id",
	ReasonInvalidServiceID:     "invalid service id",
	ReasonInvalidCredentialID:  "invalid credential id",
	ReasonInvalidKeyID:         "invalid key id",
	ReasonInvalidRelayRequest:  "invalid relay request",
	ReasonInvalidRelayPath:     "invalid relay path",
	ReasonInvalidScopes:        "invalid scopes: {{scopes}}",
	ReasonScopesRequired:       "at least one scope is required",
	ReasonServersNotFound:      "unknown server ids: {{servers}}",
	ReasonMethodRequired:       "method is required",
	ReasonCommandRequired:      "command is required",
	ReasonServiceNotFound:      "service not found",
	ReasonCredentialNotFound:   "credential not found",
	ReasonAPIKeyNotFound:       "api key not found",
	ReasonRawKeyUnavailable:    "raw key not available",
	ReasonCredentialDecrypt:    "credential decryption failed",
	ReasonAPIKeyServerDenied:   "access denied: api key does not have access to this server",
	ReasonAPIKeyServiceDenied:  "access denied: api key does not have access to the service's server",
	ReasonRelayTimeout:         "target service timeout",
	ReasonRelayUnreachable:     "target service unreachable: {{detail}}",
	ReasonInvalidQuery:         "invalid query parameters: {{detail}}",
	ReasonSSHConnection:        "ssh connection failed",
	ReasonSSHAuth:              "ssh authentication failed",
	ReasonSSHTimeout:           "ssh command timed out",
	ReasonSSHHostKeyMismatch:   "ssh host key changed: verify the new fingerprint before trusting it",
	ReasonNoHostKeyMismatch:    "no pending host key change to trust",
	ReasonHostKeyChanged:       "the pending host key changed again: re-verify the new fingerprint",

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
