package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
)

func uintPtr(v uint) *uint { return &v }

// TestUpdateServerRequestCredentialPresence guards the "clear a credential"
// contract: an absent field means "leave unchanged" while an explicit null means
// "unbind". A plain *uint decodes both to nil, which made clearing impossible.
func TestUpdateServerRequestCredentialPresence(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantSet   bool
		wantValue *uint
	}{
		{"absent", `{}`, false, nil},
		{"unrelated field", `{"name":"srv"}`, false, nil},
		{"explicit null", `{"credential_id":null}`, true, nil},
		{"value", `{"credential_id":5}`, true, uintPtr(5)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req UpdateServerRequest
			if err := json.Unmarshal([]byte(tt.body), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if req.CredentialID.set != tt.wantSet {
				t.Fatalf("set = %v, want %v", req.CredentialID.set, tt.wantSet)
			}
			switch {
			case tt.wantValue == nil && req.CredentialID.value != nil:
				t.Fatalf("value = %d, want nil", *req.CredentialID.value)
			case tt.wantValue != nil && req.CredentialID.value == nil:
				t.Fatalf("value = nil, want %d", *tt.wantValue)
			case tt.wantValue != nil && *req.CredentialID.value != *tt.wantValue:
				t.Fatalf("value = %d, want %d", *req.CredentialID.value, *tt.wantValue)
			}
		})
	}
}

func TestUpdateServerRequestRejectsBadCredentialID(t *testing.T) {
	var req UpdateServerRequest
	if err := json.Unmarshal([]byte(`{"credential_id":"nope"}`), &req); err == nil {
		t.Fatal("expected a non-numeric credential_id to fail decoding")
	}
}

func TestServerConnectionFieldPresence(t *testing.T) {
	for _, test := range []struct {
		body       string
		set, value bool
	}{
		{`{"name":"legacy"}`, false, false},
		{`{"connection":null}`, true, false},
		{`{"connection":{"mode":"direct"}}`, true, true},
		{`{"connection":{"mode":"socks5","host":"proxy","port":1080,"auth_type":"none"},"expected_connection_revision":5}`, true, true},
	} {
		var req UpdateServerRequest
		if err := json.Unmarshal([]byte(test.body), &req); err != nil {
			t.Fatal(err)
		}
		if req.Connection.Set != test.set || (req.Connection.Config != nil) != test.value {
			t.Fatalf("three-state connection lost for %s", test.body)
		}
	}
	for _, body := range []string{`{"connection":"direct"}`, `{"connection":{"mode":"socks5","password":123}}`} {
		var req UpdateServerRequest
		if err := json.Unmarshal([]byte(body), &req); err == nil {
			t.Fatalf("accepted malformed connection: %s", body)
		}
	}
}

func TestServerPathIDRejectsNativeOverflow(t *testing.T) {
	request := func(id string) (uint, error) {
		r := httptest.NewRequest("GET", "/", nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", id)
		return parseIDParam(r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route)))
	}
	max := ^uint(0)
	if got, err := request(strconv.FormatUint(uint64(max), 10)); err != nil || got != max {
		t.Fatalf("native maximum truncated: got=%d err=%v", got, err)
	}
	overflow := "18446744073709551616"
	if strconv.IntSize == 32 {
		overflow = "4294967296"
	}
	if _, err := request(overflow); err == nil {
		t.Fatal("native overflow was accepted")
	}
}
