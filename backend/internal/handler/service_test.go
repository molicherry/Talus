package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vpsmanager/backend/internal/service"
)

func TestCreateServiceRejectsOversizedUsageGuide(t *testing.T) {
	h := NewServiceHandler(service.NewServiceRelayService(nil, nil), nil)

	body := map[string]any{
		"name":        "grafana",
		"base_url":    "http://grafana.internal:3000",
		"credentials": map[string]string{"api_key": "secret"},
		"usage_guide": strings.Repeat("界", 20001),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/services", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	// NewValidationError → 422 Unprocessable Entity.
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
	if !strings.Contains(rec.Body.String(), "usage_guide") {
		t.Errorf("response should mention usage_guide, got: %s", rec.Body.String())
	}
}
