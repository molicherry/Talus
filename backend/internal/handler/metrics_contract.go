package handler

import "time"

// Metrics query contract (REQUIREMENTS §4.2.1). The field names are frozen and
// must match the frontend decoder: `interval`/`allow_coarsen`/`actual_interval`,
// not the earlier `bucket`/`allow_downsample`/`actual_bucket`.
const (
	MetricsErrBudgetExceeded = "metrics_query_budget_exceeded"
	MetricsErrRollupNotReady = "metrics_rollup_not_ready"

	MetricsSourceRaw    = "raw"
	MetricsSourceRollup = "rollup"
	MetricsSourceMixed  = "mixed"

	MetricsCoarsenBucketLimit   = "bucket_limit"
	MetricsCoarsenRawBudget     = "raw_budget"
	MetricsCoarsenRollupMissing = "rollup_required"
)

// MetricsQuery is the normalized, validated query. Interval is one of
// auto/1m/5m/15m/1h/6h; Stat is avg or max.
type MetricsQuery struct {
	Range        string
	Interval     string
	Stat         string
	AllowCoarsen bool
	From         time.Time
	To           time.Time
}

// MetricPoint is one bucket. A nil series value means "no valid sample"
// (a gap); a non-nil 0 is a real zero.
type MetricPoint struct {
	T      time.Time           `json:"t"`
	Series map[string]*float64 `json:"series"`
}

// MetricsResponse is the frozen query response envelope payload.
type MetricsResponse struct {
	Range             string        `json:"range"`
	RequestedInterval string        `json:"requested_interval"`
	ActualInterval    string        `json:"actual_interval"`
	CoarsenReason     string        `json:"coarsen_reason,omitempty"`
	Stat              string        `json:"stat"`
	Downsampled       bool          `json:"downsampled"`
	From              time.Time     `json:"from"`
	To                time.Time     `json:"to"`
	SampledAt         time.Time     `json:"sampled_at"`
	CoverageSeconds   int64         `json:"coverage_seconds"`
	Source            string        `json:"source"`
	Points            []MetricPoint `json:"points"`
}
