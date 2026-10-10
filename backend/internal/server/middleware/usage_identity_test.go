package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/usage"
)

func TestRequestIDValidationAndCorrelation(t *testing.T) {
	for _, id := range []string{"caller-ID:abc_123.xyz", "a", strings.Repeat("a", 128)} {
		if !ValidRequestID(id) {
			t.Errorf("valid correlation ID rejected: %q", id)
		}
	}
	for _, id := range []string{"", strings.Repeat("a", 129), "hello world", "nonascii-中文", "bad\tvalue", "slash/id", "line\r\nvalue"} {
		if ValidRequestID(id) {
			t.Errorf("invalid correlation ID accepted: %q", id)
		}
	}
	for _, input := range []string{"correlation:trusted", "hello world", strings.Repeat("a", 129)} {
		response := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Request-ID", input)
		RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := GetRequestID(r.Context()); got == "" || !ValidRequestID(got) || got != w.Header().Get("X-Request-ID") {
				t.Errorf("context/response ID mismatch: %q", got)
			}
			_, _ = w.Write([]byte(GetRequestID(r.Context())))
		})).ServeHTTP(response, req)
		id := response.Header().Get("X-Request-ID")
		if ValidRequestID(input) && id != input {
			t.Error("valid caller correlation ID changed")
		}
		if !ValidRequestID(input) && id == input {
			t.Error("invalid caller correlation ID echoed")
		}
		if response.Body.String() != id {
			t.Error("handler received a different ID")
		}
	}
}

func TestAPIKeyOwnerAndCallerAreSeparate(t *testing.T) {
	owner, keyID := uint(7), uint(91)
	validator := APIKeyIdentityValidatorFunc(func(context.Context, string) (usage.Principal, error) {
		return usage.Principal{AuthType: "api_key", UserID: &owner, APIKeyID: &keyID, Username: "owner", APIKeyName: "automation", APIKeyPrefix: "tk_short", Role: "admin", Scopes: []string{"servers:read"}, ServerIDs: []uint{33}}, nil
	})
	called := false
	handler := Auth(token.NewJWTService("secret", time.Hour), validator, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		principal := GetPrincipal(r.Context())
		claims := GetUserClaims(r.Context())
		if principal.UserID == nil || *principal.UserID != owner || principal.APIKeyID == nil || *principal.APIKeyID != keyID {
			t.Fatalf("incorrect principal: %#v", principal)
		}
		if claims == nil || claims.UserID != owner || claims.UserID == keyID || CheckServerAccess(claims, 34) || !CheckServerAccess(claims, 33) {
			t.Fatalf("identity/server restrictions lost: %#v", claims)
		}
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest("GET", "/api/v1/servers", nil)
	req.Header.Set("X-API-Key", "raw-secret")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("valid API key did not reach handler")
	}
}

func TestLegacyKeyValidatorNeverInventsOwner(t *testing.T) {
	validator := APIKeyValidatorFunc(func(context.Context, string) (uint, string, string, []string, []uint, error) {
		return 91, "legacy-key", "admin", []string{"*"}, nil, nil
	})
	handler := Auth(nil, validator, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := GetPrincipal(r.Context())
		if principal.UserID != nil || principal.APIKeyID == nil || *principal.APIKeyID != 91 || GetUserClaims(r.Context()).UserID != 0 {
			t.Fatalf("legacy key became a user: %#v", principal)
		}
	}))
	req := httptest.NewRequest("GET", "/api/v1/servers", nil)
	req.Header.Set("X-API-Key", "raw-secret")
	handler.ServeHTTP(httptest.NewRecorder(), req)
}

func TestVerifiedKeyIdentitySurvivesScopeDenial(t *testing.T) {
	keyID := uint(91)
	validator := APIKeyIdentityValidatorFunc(func(context.Context, string) (usage.Principal, error) {
		return usage.Principal{APIKeyID: &keyID, APIKeyName: "restricted", Role: "admin"}, nil
	})
	op := usage.NewOperation("server.exec", "/api/v1/servers/{id}/exec", "corr", "POST", "127.0.0.1")
	req := httptest.NewRequest("POST", "/api/v1/servers/5/exec", nil)
	req.Header.Set("X-API-Key", "raw-secret")
	req = req.WithContext(usage.WithOperation(req.Context(), op))
	response := httptest.NewRecorder()
	reached := false
	Auth(nil, validator, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })).ServeHTTP(response, req)
	principal := op.Principal()
	if reached || response.Code != 403 || principal.AuthType != "api_key" || principal.APIKeyID == nil || *principal.APIKeyID != keyID {
		t.Fatalf("denial lost caller: %d %#v", response.Code, principal)
	}
	if strings.Contains(response.Body.String(), "raw-secret") {
		t.Fatal("raw key in response")
	}
}

func TestUsageRoutesRejectEveryAPIKey(t *testing.T) {
	validator := APIKeyIdentityValidatorFunc(func(context.Context, string) (usage.Principal, error) {
		id := uint(91)
		return usage.Principal{APIKeyID: &id, Role: "admin", Scopes: []string{"*"}}, nil
	})
	for _, path := range []string{"/api/v1/usage-logs", "/api/v1/usage-logs/filter-options", "/api/v1/usage-logs/123", "/api/v1/usage-logs/bad-id"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-API-Key", "raw-secret")
		response := httptest.NewRecorder()
		Auth(nil, validator, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("API key reached usage log handler") })).ServeHTTP(response, req)
		if response.Code != 403 {
			t.Fatalf("%s status %d", path, response.Code)
		}
	}
}

func TestAuthFailureCarriesValidatedRequestID(t *testing.T) {
	response := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/servers", nil)
	req.Header.Set("X-Request-ID", "invalid space")
	handler := RequestID(Auth(nil, nil, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("missing credentials reached handler") })))
	handler.ServeHTTP(response, req)
	var envelope struct {
		Error struct {
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.RequestID != response.Header().Get("X-Request-ID") || !ValidRequestID(envelope.Error.RequestID) {
		t.Fatal("request ID missing from auth error")
	}
}

func TestUsageRouteWhitelistAndInvalidResourceIDs(t *testing.T) {
	for _, tc := range []struct {
		method, path, action string
		id                   bool
	}{
		{"POST", "/api/v1/servers", "server.create", false},
		{"POST", "/api/v1/servers/12/exec", "server.exec", true},
		{"GET", "/api/v1/servers/12/terminal", "server.terminal", true},
		{"GET", "/api/v1/services/4/credentials", "service.credentials", true},
		{"GET", "/api/v1/credentials/bad/reveal", "credential.reveal", false},
	} {
		route, id, ok := usageRoute(tc.method, tc.path)
		if !ok || route.action != tc.action || (id != nil) != tc.id {
			t.Fatalf("%s %s => %#v %v %v", tc.method, tc.path, route, id, ok)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/servers"}, {"GET", "/api/v1/servers/4/metrics"}, {"GET", "/api/v1/services/4"}, {"GET", "/api/v1/usage-logs"}, {"GET", "/healthz"}, {"GET", "/assets/file.js"}, {"POST", "/api/v1/unknown/4"},
	} {
		if _, _, ok := usageRoute(tc.method, tc.path); ok {
			t.Errorf("read/unlisted endpoint captured: %#v", tc)
		}
	}
}

func TestOperationIDIsIndependentFromCorrelationID(t *testing.T) {
	a := usage.NewOperation("server.create", "/api/v1/servers", "shared-request-id", "POST", "127.0.0.1")
	b := usage.NewOperation("server.create", "/api/v1/servers", "shared-request-id", "POST", "127.0.0.1")
	if a.OperationID() == b.OperationID() || a.OperationID() == "shared-request-id" {
		t.Fatal("operation deduplicated by caller correlation ID")
	}
}
