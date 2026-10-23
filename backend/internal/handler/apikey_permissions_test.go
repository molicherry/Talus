package handler

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIKeyPermissionPatchPresence(t *testing.T) {
	for _, tc := range []struct {
		name, body             string
		valid, scopes, servers bool
	}{
		{"scopes only", `{"expected_revision":"1","scopes":["servers:read"]}`, true, true, false},
		{"explicit all servers", `{"expected_revision":"1","server_ids":[]}`, true, false, true},
		{"empty scopes reaches business validation", `{"expected_revision":"1","scopes":[]}`, true, true, false},
		{"null scopes", `{"expected_revision":"1","scopes":null}`, false, false, false},
		{"null servers", `{"expected_revision":"1","server_ids":null}`, false, false, false},
		{"empty patch", `{"expected_revision":"1"}`, false, false, false},
		{"missing revision", `{"scopes":["servers:read"]}`, false, false, false},
		{"numeric revision", `{"expected_revision":1,"server_ids":[]}`, false, false, false},
		{"overflow", `{"expected_revision":"9223372036854775808","server_ids":[]}`, false, false, false},
		{"negative id", `{"expected_revision":"1","server_ids":[-1]}`, false, false, false},
		{"unknown field", `{"expected_revision":"1","name":"new","server_ids":[]}`, false, false, false},
		{"trailing JSON", `{"expected_revision":"1","server_ids":[]} {}`, false, false, false},
		{"oversized", `{"expected_revision":"1","scopes":["` + strings.Repeat("x", 128*1024) + `"]}`, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := decodeAPIKeyPermissions(httptest.NewRecorder(), httptest.NewRequest("PATCH", "/", strings.NewReader(tc.body)))
			if (err == nil) != tc.valid {
				t.Fatalf("error = %v", err)
			}
			if tc.valid && ((input.Scopes != nil) != tc.scopes || (input.ServerIDs != nil) != tc.servers) {
				t.Fatal("omission and explicit lists conflated")
			}
		})
	}
}

func TestAPIKeyRotationRequest(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"expected_revision":1}`, `{"expected_revision":null}`, `{"expected_revision":"0"}`, `{"expected_revision":"01"}`, `{"expected_revision":"-1"}`, `{"expected_revision":"9223372036854775808"}`, `{"expected_revision":"1","scopes":[]}`, `{"expected_revision":"1"} {}`, `{"expected_revision":"` + strings.Repeat("1", 1024) + `"}`} {
		if _, err := decodeAPIKeyRotation(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(body))); err == nil {
			t.Fatal("accepted malformed rotation request")
		}
	}
	if revision, err := decodeAPIKeyRotation(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(`{"expected_revision":"7"}`))); err != nil || revision != 7 {
		t.Fatal("valid rotation rejected")
	}
}
