package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
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
		{"missing_proof_invalid_fields", `{"username":"","password":""}`, http.StatusForbidden},
		{"incorrect_proof_invalid_fields", `{"username":"","password":"","token":"` + strings.Repeat("wrong-", 10) + `"}`, http.StatusForbidden},
		{"correct_proof_invalid_fields", `{"username":"","password":"","token":"` + proof + `"}`, http.StatusUnprocessableEntity},
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
			if tc.status == http.StatusForbidden && strings.Contains(w.Body.String(), `"field"`) {
				t.Fatal("unproved caller received credential field feedback")
			}
			if tc.status == http.StatusUnprocessableEntity && (!strings.Contains(w.Body.String(), `"username"`) || !strings.Contains(w.Body.String(), `"password"`)) {
				t.Fatal("proved caller did not receive credential field feedback")
			}
		})
	}
}

func TestSetupStatusReportsBootstrapAvailability(t *testing.T) {
	db := usageFlowDB(t)
	svc := service.NewAuthService(repository.NewUserRepo(db), nil, db)
	h := NewAuthHandler(svc)
	check := func(wantNeeded, wantEnabled bool) {
		t.Helper()
		w := httptest.NewRecorder()
		h.Setup(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/setup", nil))
		var response struct {
			Data SetupResponse `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusOK {
			t.Fatalf("setup status: status=%d body=%s error=%v", w.Code, w.Body.String(), err)
		}
		if !strings.Contains(w.Body.String(), `"bootstrap_enabled"`) {
			t.Fatal("setup status omitted the enabled/disabled contract")
		}
		if response.Data.Needed != wantNeeded || response.Data.BootstrapEnabled != wantEnabled {
			t.Fatalf("setup status=%+v want needed=%v enabled=%v", response.Data, wantNeeded, wantEnabled)
		}
	}
	check(true, false)
	svc.SetBootstrapToken(strings.Repeat("a", 64))
	check(true, true)
	if err := db.Create(&model.User{Username: "existing-owner", PasswordHash: "not-used-by-status", Role: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	svc.SetBootstrapToken("")
	check(false, false)
}
