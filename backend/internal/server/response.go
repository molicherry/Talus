package server

import (
	"encoding/json"
	"net/http"
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
	requestID := ""
	if rid := r.Context().Value("request_id"); rid != nil {
		if s, ok := rid.(string); ok {
			requestID = s
		}
	}

	statusCode := StatusCode(err)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	w.Write(marshalError(err, requestID))
}
