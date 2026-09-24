package handler

import (
	"encoding/json"
	"testing"
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
