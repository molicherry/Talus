package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vpsmanager/backend/internal/server"
	"github.com/vpsmanager/backend/internal/server/middleware"
)

func TestStructuredErrorUsesMiddlewareRequestID(t *testing.T) {
	response := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/servers", nil)
	req.Header.Set("X-Request-ID", "request:caller.123")
	middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.WriteError(w, r, server.ErrForbidden)
	})).ServeHTTP(response, req)
	var envelope struct {
		Error struct {
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.RequestID != "request:caller.123" || envelope.Error.RequestID != response.Header().Get("X-Request-ID") {
		t.Fatal("error envelope lost typed-context request ID")
	}
}
