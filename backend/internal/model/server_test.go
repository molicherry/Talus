package model

import (
	"encoding/json"
	"testing"
)

// TestServerSummaryJSONContract guards the wire contract of ServerSummary
// against drift: the JSON field names emitted by the backend must exactly
// match the frontend ServerSummarySchema (id, name, description, host,
// credential_id, status). Renaming a field here silently breaks the list
// page / dropdown consumers.
func TestServerSummaryJSONContract(t *testing.T) {
	id := uint(7)
	s := ServerSummary{
		ID:           id,
		Name:         "prod-1",
		Description:  nil,
		Host:         "10.0.0.1",
		CredentialID: &id,
		Status:       "online",
	}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	required := []string{"id", "name", "host", "credential_id", "status"}
	for _, field := range required {
		if _, ok := m[field]; !ok {
			t.Errorf("ServerSummary JSON missing required field %q (raw: %s)", field, b)
		}
	}

	// credential_id must be present as null when unset (schema declares
	// z.number().nullable()), never omitted.
	s2 := ServerSummary{ID: 1, Name: "x", Host: "h", Status: "unknown"}
	b2, err := json.Marshal(s2)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(b2, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, ok := m["credential_id"]; !ok || v != nil {
		t.Errorf("expected credential_id:null when unset, got %v (raw: %s)", m["credential_id"], b2)
	}
}
