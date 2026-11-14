package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// explainProbeNode is the subset of PostgreSQL's JSON plan used by this test.
type explainProbeNode struct {
	NodeType   string             `json:"Node Type"`
	IndexName  string             `json:"Index Name"`
	ActualRows float64            `json:"Actual Rows"`
	Plans      []explainProbeNode `json:"Plans"`
}

func (n explainProbeNode) find(kind string) []explainProbeNode {
	var matches []explainProbeNode
	if n.NodeType == kind {
		matches = append(matches, n)
	}
	for _, child := range n.Plans {
		matches = append(matches, child.find(kind)...)
	}
	return matches
}

// TestRawSampleProbePlanKeepsLimitAndUsesTimeIndex verifies the actual probe SQL
// avoids a sort, stops returning candidates at its cap, and can use the time
// index for a narrow window. The broad plan may legitimately use a sequential
// scan and examine more than the candidate cap to apply its server filter.
func TestRawSampleProbePlanKeepsLimitAndUsesTimeIndex(t *testing.T) {
	repo, db := budgetTestRepo(t)
	// TimescaleDB creates a descending time index for a default hypertable.
	// Model it explicitly on this isolated PostgreSQL table; do not assert that
	// the optimizer must use an index when a broad scan is cheaper.
	if err := db.Exec(`CREATE INDEX idx_metrics_probe_time ON metrics (time DESC)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO metrics (server_id, time)
		SELECT g % 2, now() - g * interval '3 minutes'
		FROM generate_series(1, 40000) AS g`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`ANALYZE metrics`).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	explain := func(from, to time.Time) explainProbeNode {
		t.Helper()
		var raw string
		if err := db.Raw(`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) `+rawSampleProbeSQL,
			1, from, to, rawProbeLimit).Row().Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var result []struct {
			Plan explainProbeNode `json:"Plan"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil || len(result) != 1 {
			t.Fatalf("invalid probe plan: %v: %s", err, raw)
		}
		if sorts := append(result[0].Plan.find("Sort"), result[0].Plan.find("Incremental Sort")...); len(sorts) != 0 {
			t.Fatalf("probe requires no ordering, but its plan sorts: %s", raw)
		}
		return result[0].Plan
	}
	from, to := now.Add(-90*24*time.Hour), now.Add(time.Minute)
	broad := explain(from, to)
	limits := broad.find("Limit")
	if len(limits) != 1 || limits[0].ActualRows != float64(rawProbeLimit) {
		t.Fatalf("broad probe must return exactly %d candidate rows at its limit: %+v", rawProbeLimit, limits)
	}
	samples, overBudget, err := repo.probeRawSamples(context.Background(), 1, from, to)
	if err != nil || !overBudget || samples != rawProbeLimit {
		t.Fatalf("probe = %d, over budget %v, error %v; want %d/true/nil", samples, overBudget, err, rawProbeLimit)
	}
	// A selective time window should use the representative time index with the
	// planner's default settings, including its server_id filter.
	narrow := explain(now.Add(-time.Hour), to)
	var usesTimeIndex bool
	for _, kind := range []string{"Index Scan", "Index Only Scan", "Bitmap Index Scan"} {
		for _, node := range narrow.find(kind) {
			usesTimeIndex = usesTimeIndex || node.IndexName == "idx_metrics_probe_time"
		}
	}
	if !usesTimeIndex {
		t.Fatalf("selective time window did not use the time index: %+v", narrow)
	}
}
