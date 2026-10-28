package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vpsmanager/backend/internal/server"
)

func TestAuthJSONParsingBudget(t *testing.T) {
	valid := `{"username":"dummy-user","password":"dummy-password"}`
	for _, test := range []struct {
		name string
		body string
		code int
	}{
		{"valid", valid, http.StatusOK},
		{"at_limit", valid + strings.Repeat(" ", int(authJSONMaxBytes)-len(valid)), http.StatusOK},
		{"oversize_field", `{"username":"` + strings.Repeat("x", int(authJSONMaxBytes)) + `","password":"dummy-password"}`, http.StatusRequestEntityTooLarge},
		{"oversize_unknown_field", `{"ignored":"` + strings.Repeat("x", int(authJSONMaxBytes)) + `"}`, http.StatusRequestEntityTooLarge},
		{"oversize_suffix", valid + strings.Repeat(" ", int(authJSONMaxBytes)), http.StatusRequestEntityTooLarge},
		{"second_value", valid + `{}`, http.StatusBadRequest},
		{"null_suffix", valid + `null`, http.StatusBadRequest},
		{"junk_suffix", valid + `junk`, http.StatusBadRequest},
		{"empty", "", http.StatusBadRequest},
	} {
		for _, unknownLength := range []bool{false, true} {
			t.Run(test.name+map[bool]string{false: "/length", true: "/chunked"}[unknownLength], func(t *testing.T) {
				r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(test.body))
				if unknownLength {
					r.ContentLength = -1
					r.TransferEncoding = []string{"chunked"}
				}
				var req LoginRequest
				err := decodeAuthJSON(httptest.NewRecorder(), r, &req)
				code := http.StatusOK
				if err != nil {
					code = server.StatusCode(err)
				}
				if code != test.code {
					t.Fatalf("status = %d (%v), want %d", code, err, test.code)
				}
				if code == http.StatusOK && (req.Username != "dummy-user" || req.Password != "dummy-password") {
					t.Fatalf("decoded credentials changed: %+v", req)
				}
			})
		}
	}
}

func TestAuthHandlersRejectOversizeBeforeService(t *testing.T) {
	// The nil service deliberately makes reaching password hashing/database
	// work a test failure; both handlers must reject at the parsing boundary.
	h := NewAuthHandler(nil)
	for name, handle := range map[string]http.HandlerFunc{"login": h.Login, "change_password": h.ChangePassword} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ignored":"`+strings.Repeat("x", int(authJSONMaxBytes))+`"}`))
			w := httptest.NewRecorder()
			handle(w, r)
			if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), `"reason":"invalid_request"`) {
				t.Fatalf("response = %d %s", w.Code, w.Body.String())
			}
		})
	}
}
