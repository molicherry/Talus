package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/usage"
)

// envelope wraps data in a "data" key for JSON responses.
type envelope struct {
	Data any `json:"data"`
}

// WriteJSON writes a JSON response with the given status code and data wrapped in {"data": ...}.
func WriteJSON(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	resp := envelope{Data: data}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, `{"error":{"code":500,"message":"internal server error"}}`, http.StatusInternalServerError)
	}
}

// WriteError writes a structured JSON error response.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	requestID := middleware.GetRequestID(r.Context())

	statusCode := StatusCode(err)
	if op := usage.FromContext(r.Context()); op != nil {
		reason := ReasonInternal
		var appErr *AppError
		var validationErr *ValidationError
		if errors.As(err, &appErr) && appErr.Reason != "" {
			reason = appErr.Reason
		}
		if errors.As(err, &validationErr) && validationErr.Reason != "" {
			reason = validationErr.Reason
		}
		op.SetHTTPError(statusCode, reason)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(marshalError(err, requestID))
}
