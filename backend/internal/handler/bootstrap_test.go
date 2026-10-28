package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vpsmanager/backend/internal/service"
)

func TestBootstrapRejectsUnprovedAndOversizedRequests(t *testing.T) {
	proof := strings.Repeat("secret-", 10)
	svc := service.NewAuthService(nil, nil, nil)
	svc.SetBootstrapToken(proof)
	h := NewAuthHandler(svc)
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing_proof", `{"username":"owner","password":"dummy-password"}`, http.StatusForbidden},
		{"incorrect_proof", `{"username":"owner","password":"dummy-password","token":"` + strings.Repeat("wrong-", 10) + `"}`, http.StatusForbidden},
		{"huge_proof", `{"username":"owner","password":"dummy-password","token":"` + strings.Repeat("x", 9000) + `"}`, http.StatusRequestEntityTooLarge},
		{"trailing_json", `{"username":"owner","password":"dummy-password","token":"` + proof + `"} {}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			h.Bootstrap(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d", w.Code, tc.status)
			}
			if strings.Contains(w.Body.String(), proof) || strings.Contains(w.Body.String(), "wrong-wrong-") {
				t.Fatal("setup proof leaked into the response")
			}
		})
	}
}
