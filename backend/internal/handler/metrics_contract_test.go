package handler

import (
	"encoding/json"
	"testing"
	"time"
)

// The metrics response shape is a frozen contract with the frontend decoder:
// flat series values next to `t`, and a `coarsen_reason` that is always present
// (null when no coarsening happened).
func TestMetricsResponseJSONContract(t *testing.T) {
	value := 1.5
	resp := MetricsResponse{
		Points: []MetricPoint{
			{T: time.Unix(0, 0).UTC(), Series: map[string]*float64{"cpu_percent": &value, "load_1": nil}},
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, k := range []string{
		"range", "requested_interval", "actual_interval", "coarsen_reason", "stat",
		"downsampled", "from", "to", "sampled_at", "coverage_seconds", "source", "points",
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
	if m["coarsen_reason"] != nil {
		t.Fatalf("coarsen_reason must be null when nothing was coarsened, got %v", m["coarsen_reason"])
	}

	points, ok := m["points"].([]any)
	if !ok || len(points) != 1 {
		t.Fatalf("points must be a one-element array, got %v", m["points"])
	}
	p0, ok := points[0].(map[string]any)
	if !ok {
		t.Fatalf("point must be an object, got %v", points[0])
	}
	if _, ok := p0["t"]; !ok {
		t.Fatal("point must carry t")
	}
	if _, ok := p0["cpu_percent"]; !ok {
		t.Fatal("series values must be flattened next to t")
	}
	if _, ok := p0["series"]; ok {
		t.Fatal("series must not be nested under a `series` key")
	}
	if v, present := p0["load_1"]; !present || v != nil {
		t.Fatalf("a missing sample must marshal as null, got present=%v value=%v", present, v)
	}
}
