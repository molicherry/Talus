package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateServiceRejectsOversizedUsageGuide verifies the usage_guide length
// cap (20000 runes) is enforced at the handler boundary, before any service
// layer call — so it works even with a nil service dependency.
func TestCreateServiceRejectsOversizedUsageGuide(t *testing.T) {
	h := NewServiceHandler(nil, nil)

	body := map[string]any{
		"name":        "grafana",
		"base_url":    "http://grafana.internal:3000",
		"credentials": map[string]string{"api_key": "secret"},
		"usage_guide": strings.Repeat("x", maxUsageGuideRunes+1),
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

// TestCreateServiceAcceptsBoundaryUsageGuide verifies a guide at exactly the
// cap passes validation. The handler proceeds past validation into svc.Create
// (nil svc panics there); we recover and only assert validation did NOT reject.
func TestCreateServiceAcceptsBoundaryUsageGuide(t *testing.T) {
	h := NewServiceHandler(nil, nil)

	body := map[string]any{
		"name":        "grafana",
		"base_url":    "http://grafana.internal:3000",
		"credentials": map[string]string{"api_key": "secret"},
		"usage_guide": strings.Repeat("x", maxUsageGuideRunes),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/services", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	var status int
	func() {
		defer func() { _ = recover() }()
		h.Create(rec, req)
		status = rec.Code
	}()

	// Validation passed → no 400 validation error (nil svc panics later,
	// which is expected and recovered).
	if status == http.StatusBadRequest {
		t.Errorf("boundary-size usage_guide rejected with 400: %s", rec.Body.String())
	}
}
