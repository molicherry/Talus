package main

import (
	"encoding/json"
	"testing"
)

// TestAgentOutputCarriesVersionContract pins TC-08-05's protocol output: every
// agent build reports a non-empty build version and the exact protocol version
// the Hub validates against.
func TestAgentOutputCarriesVersionContract(t *testing.T) {
	output := AgentOutput{
		AgentVersion:    "v1.2.3-test.4",
		ProtocolVersion: 1,
		Timestamp:       "2026-01-01T00:00:00Z",
		Hostname:        "node",
	}
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	version, ok := decoded["agent_version"].(string)
	if !ok || version == "" {
		t.Fatalf("agent_version = %v, want a non-empty string", decoded["agent_version"])
	}
	protocol, ok := decoded["protocol_version"].(float64)
	if !ok || int(protocol) != 1 {
		t.Fatalf("protocol_version = %v, want the integer 1", decoded["protocol_version"])
	}
}
