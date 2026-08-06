package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vpsmanager/backend/internal/server"
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

// TestValidateServiceRequestUsageGuideBoundary verifies the usage_guide length
// cap directly on the shared validation function: exactly 20000 runes passes,
// 20001 runes is rejected with a usage_guide error detail.
func TestValidateServiceRequestUsageGuideBoundary(t *testing.T) {
	base := createServiceRequest{
		Name:        "grafana",
		BaseURL:     "http://grafana.internal:3000",
		Credentials: map[string]string{"api_key": "secret"},
	}

	// Exactly at the cap → no usage_guide error.
	req := base
	guide := strings.Repeat("x", maxUsageGuideRunes)
	req.UsageGuide = &guide
	if d := validateServiceRequest(req); hasField(d, "usage_guide") {
		t.Fatalf("boundary-size usage_guide rejected: %v", d)
	}

	// One rune over the cap → usage_guide error.
	req.UsageGuide = strPtr(strings.Repeat("x", maxUsageGuideRunes+1))
	d := validateServiceRequest(req)
	if !hasField(d, "usage_guide") {
		t.Errorf("oversized usage_guide not rejected, details=%v", d)
	}
}

func hasField(details []server.ErrorDetail, field string) bool {
	for _, d := range details {
		if d.Field == field {
			return true
		}
	}
	return false
}

func strPtr(s string) *string {
	return &s
}
