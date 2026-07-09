package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// ErrorDetail describes a single field-level validation error.
type ErrorDetail struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// AppError is a structured application error with an HTTP status code.
type AppError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Err     error  `json:"-"`
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%d] %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
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
	ErrUnauthorized  = &AppError{Code: http.StatusUnauthorized, Message: "unauthorized"}
	ErrForbidden     = &AppError{Code: http.StatusForbidden, Message: "forbidden"}
	ErrNotFound      = &AppError{Code: http.StatusNotFound, Message: "resource not found"}
	ErrConflict      = &AppError{Code: http.StatusConflict, Message: "resource conflict"}
	ErrValidation    = &AppError{Code: http.StatusUnprocessableEntity, Message: "validation failed"}
	ErrInternal      = &AppError{Code: http.StatusInternalServerError, Message: "internal server error"}
	ErrSSHConnection = &AppError{Code: http.StatusBadGateway, Message: "ssh connection failed"}
	ErrSSHAuth       = &AppError{Code: http.StatusBadGateway, Message: "ssh authentication failed"}
	ErrSSHTimeout    = &AppError{Code: http.StatusGatewayTimeout, Message: "ssh command timed out"}
)

// NewAppError creates a new AppError with the given code and message.
func NewAppError(code int, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

// NewValidationError creates a new ValidationError.
func NewValidationError(details []ErrorDetail) *ValidationError {
	return &ValidationError{
		AppError: AppError{Code: http.StatusUnprocessableEntity, Message: "validation failed"},
		Details:  details,
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
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Details []ErrorDetail  `json:"details,omitempty"`
	RequestID string       `json:"request_id,omitempty"`
}

// marshalError serializes an error into the JSON error envelope.
func marshalError(err error, requestID string) []byte {
	code := StatusCode(err)
	msg := err.Error()

	body := errorBody{
		Code:      code,
		Message:   msg,
		RequestID: requestID,
	}

	var appErr *AppError
	if errors.As(err, &appErr) {
		body.Message = appErr.Message
	}
	var valErr *ValidationError
	if errors.As(err, &valErr) {
		body.Message = valErr.Message
		body.Details = valErr.Details
	}

	data, err := json.Marshal(errorResponse{Error: body})
	if err != nil {
		slog.Error("failed to marshal error response", "error", err)
		return []byte(`{"error":{"code":500,"message":"internal server error"}}`)
	}
	return data
}
