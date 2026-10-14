package main

import (
	"testing"

	"github.com/vpsmanager/backend/internal/agentproto"
)

// TestVerifyReportRejectsNonIntegerProtocols is the P2-04 regression: the protocol
// check must decode the JSON by type, so a fractional or exponent notation value
// (or a string) is rejected instead of being read as the prefix "1".
func TestVerifyReportRejectsNonIntegerProtocols(t *testing.T) {
	manifest := &agentproto.Manifest{BuildVersion: "v1.2.3", ProtocolVersion: 1}

	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{
		{"exact manifest", `{"protocol_version": 1, "agent_version": "v1.2.3"}`, true},
		{"pretty printed", "{\n  \"protocol_version\": 1,\n  \"agent_version\": \"v1.2.3\"\n}\n", true},
		{"extra fields are ignored", `{"protocol_version": 1, "agent_version": "v1.2.3", "hostname": "h"}`, true},
		// The two cases the prefix match accepted.
		{"fractional protocol", `{"protocol_version": 1.5, "agent_version": "v1.2.3"}`, false},
		{"exponent protocol", `{"protocol_version": 1e9, "agent_version": "v1.2.3"}`, false},
		{"fractional zero-ish protocol", `{"protocol_version": 1.0, "agent_version": "v1.2.3"}`, false},
		// Type and format negatives.
		{"string protocol", `{"protocol_version": "1", "agent_version": "v1.2.3"}`, false},
		{"null protocol", `{"protocol_version": null, "agent_version": "v1.2.3"}`, false},
		{"missing protocol", `{"agent_version": "v1.2.3"}`, false},
		{"wrong protocol", `{"protocol_version": 2, "agent_version": "v1.2.3"}`, false},
		{"protocol as ten", `{"protocol_version": 10, "agent_version": "v1.2.3"}`, false},
		{"empty version", `{"protocol_version": 1, "agent_version": ""}`, false},
		{"missing version", `{"protocol_version": 1}`, false},
		{"wrong version", `{"protocol_version": 1, "agent_version": "v9.9.9"}`, false},
		{"version prefix is not a match", `{"protocol_version": 1, "agent_version": "v1.2.30"}`, false},
		{"version type", `{"protocol_version": 1, "agent_version": 123}`, false},
		{"truncated document", `{"protocol_version": 1, "agent_version": "v1.2.3"`, false},
		{"not an object", `[1, "v1.2.3"]`, false},
		{"empty document", ``, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := agentproto.VerifyReport([]byte(tc.body), manifest)
			if tc.ok && err != nil {
				t.Fatalf("valid report rejected: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
}

// TestVerifyReportNeedsAManifest keeps the guard explicit.
func TestVerifyReportNeedsAManifest(t *testing.T) {
	if err := agentproto.VerifyReport([]byte(`{"protocol_version": 1, "agent_version": "v1"}`), nil); err == nil {
		t.Fatal("verifying without a manifest must fail")
	}
}
