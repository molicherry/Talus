package handler

import (
	"encoding/json"
	"testing"
)

// The metrics response field names are a frozen contract with the frontend
// decoder. This guards against reintroducing the pre-revision names.
func TestMetricsResponseJSONFieldNames(t *testing.T) {
	b, err := json.Marshal(MetricsResponse{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{
		"range", "requested_interval", "actual_interval", "stat",
		"downsampled", "from", "to", "sampled_at", "coverage_seconds",
		"source", "points",
	} {
		if _, ok := m[k]; !ok {
			t.Fatalf("metrics response is missing field %q", k)
		}
	}
	for _, stale := range []string{"bucket", "requested_bucket", "actual_bucket", "allow_downsample"} {
		if _, ok := m[stale]; ok {
			t.Fatalf("metrics response reintroduced stale field %q", stale)
		}
	}
}
