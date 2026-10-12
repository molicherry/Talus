package repository

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Query budget (REQUIREMENTS §4.2.1). These are the first-phase limits frozen by
// the acceptance fixtures: 600 output buckets, 12,000 planned raw samples and a
// two-second total deadline shared by probing, reading and aggregating.
const (
	MaxQueryBuckets   = 600
	MaxRawSamples     = 12000
	MaxQueryRange     = 30 * 24 * time.Hour
	QueryDeadline     = 2 * time.Second
	rawProbeLimit     = MaxRawSamples + 1
	coarsenReasonNone = ""
)

// MetricIntervals are the legal granularities, coarsest last.
var MetricIntervals = []string{"1m", "5m", "15m", "1h", "6h"}

var intervalSeconds = map[string]int64{
	"1m":  60,
	"5m":  300,
	"15m": 900,
	"1h":  3600,
	"6h":  21600,
}

// IsMetricInterval reports whether the value is one of the legal granularities.
func IsMetricInterval(value string) bool {
	_, ok := intervalSeconds[value]
	return ok
}

// QueryBudgetReason classifies a refused query so the handler can map it to the
// documented HTTP status.
type QueryBudgetReason string

const (
	// ReasonBucketBudget: the request cannot fit the bucket budget and coarsening
	// is not allowed. Maps to 422 with a recommendation.
	ReasonBucketBudget QueryBudgetReason = "metrics_query_budget_exceeded"
	// ReasonRollupNotReady: at the coarsest granularity the raw fallback still
	// exceeds the raw budget because rollups are missing. Maps to 503.
	ReasonRollupNotReady QueryBudgetReason = "metrics_rollup_not_ready"
	// ReasonQueryTimeout: the shared deadline expired. Maps to 503.
	ReasonQueryTimeout QueryBudgetReason = "metrics_query_timeout"
)

// QueryBudgetError carries the refusal class and, when applicable, the
// granularity the caller should request instead.
type QueryBudgetError struct {
	Reason      QueryBudgetReason
	Recommended string
	Samples     int
}

func (e *QueryBudgetError) Error() string {
	if e.Recommended != "" {
		return fmt.Sprintf("%s: recommended interval %s", e.Reason, e.Recommended)
	}
	return string(e.Reason)
}

// QueryRequest is one budgeted metrics query.
type QueryRequest struct {
	ServerID uint
	From     time.Time
	To       time.Time
	// Requested is "auto" (or empty) or one of MetricIntervals. An explicit
	// interval is strict unless AllowCoarsen is set.
	Requested    string
	AllowCoarsen bool
	// Stat selects which retained statistic the UI renders: "avg" (default) or
	// "peak". Both are always returned.
	Stat string
}

// QueryResult is the response contract of §4.2.1: the requested and actual
// granularity, why they differ, the statistic, coverage, the data source and
// whether the series was downsampled.
type QueryResult struct {
	ServerID          uint               `json:"server_id"`
	From              time.Time          `json:"from"`
	To                time.Time          `json:"to"`
	RequestedInterval string             `json:"requested_interval"`
	ActualInterval    string             `json:"actual_interval"`
	CoarsenReason     string             `json:"coarsen_reason"`
	Stat              string             `json:"stat"`
	Coverage          float64            `json:"coverage"`
	Source            string             `json:"source"`
	Downsampled       bool               `json:"downsampled"`
	Points            []AggregatedMetric `json:"points"`
}

// bucketCount counts UTC-aligned buckets covering [from,to), including the two
// partial edge buckets, which is what the budget is enforced against.
func bucketCount(from, to time.Time, interval string) int {
	seconds := intervalSeconds[interval]
	if seconds <= 0 || !to.After(from) {
		return 0
	}
	first := from.UTC().Unix() / seconds
	last := (to.UTC().Unix() - 1) / seconds
	if last < first {
		return 0
	}
	return int(last-first) + 1
}

// recommendedInterval returns the coarsest legal granularity that fits the
// bucket budget, plus whether one exists.
func recommendedInterval(from, to time.Time) (string, bool) {
	for _, interval := range MetricIntervals {
		if bucketCount(from, to, interval) <= MaxQueryBuckets {
			return interval, true
		}
	}
	return "", false
}

// defaultIntervalFor is the starting granularity of an "auto" query, matching the
// UI's previous defaults.
func defaultIntervalFor(from, to time.Time) string {
	switch span := to.Sub(from); {
	case span <= time.Hour:
		return "1m"
	case span <= 6*time.Hour:
		return "5m"
	case span <= 24*time.Hour:
		return "15m"
	case span <= 7*24*time.Hour:
		return "1h"
	default:
		return "1h"
	}
}

// QueryWithBudget runs the §4.2.1 query plan: it validates the range and
// granularity, enforces the bucket budget, probes at most 12,001 candidate keys
// before reading anything, and either answers from raw samples or switches to
// the materialized 6h rollup plus bounded raw edges. Probing, reading and
// aggregating share one 2s deadline.
func (r *MetricRepo) QueryWithBudget(ctx context.Context, req QueryRequest) (*QueryResult, error) {
	if req.Stat == "" {
		req.Stat = "avg"
	}
	if req.Stat != "avg" && req.Stat != "peak" {
		return nil, fmt.Errorf("invalid stat %q", req.Stat)
	}
	if !req.To.After(req.From) {
		return nil, errors.New("'to' must be after 'from'")
	}
	if req.To.Sub(req.From) > MaxQueryRange {
		return nil, errors.New("range exceeds 30 days")
	}
	requested := req.Requested
	auto := requested == "" || requested == "auto"
	if !auto && !IsMetricInterval(requested) {
		return nil, fmt.Errorf("invalid interval %q", requested)
	}

	ctx, cancel := context.WithTimeout(ctx, QueryDeadline)
	defer cancel()

	interval := requested
	if auto {
		interval = defaultIntervalFor(req.From, req.To)
	}
	result := &QueryResult{
		ServerID: req.ServerID, From: req.From.UTC(), To: req.To.UTC(),
		RequestedInterval: func() string {
			if auto {
				return "auto"
			}
			return requested
		}(),
		ActualInterval: interval,
		Stat:           req.Stat,
	}

	// The bucket budget is absolute: a request that cannot fit is refused with a
	// recommendation instead of silently returning a truncated curve.
	if bucketCount(req.From, req.To, interval) > MaxQueryBuckets {
		recommended, ok := recommendedInterval(req.From, req.To)
		if !ok {
			return nil, &QueryBudgetError{Reason: ReasonBucketBudget, Samples: bucketCount(req.From, req.To, interval)}
		}
		if !auto && !req.AllowCoarsen {
			return nil, &QueryBudgetError{Reason: ReasonBucketBudget, Recommended: recommended}
		}
		if !auto {
			result.CoarsenReason = "bucket_budget"
		}
		interval = recommended
		result.ActualInterval = interval
	}

	samples, overBudget, err := r.probeRawSamples(ctx, req.ServerID, req.From, req.To)
	if err != nil {
		return nil, err
	}

	if !overBudget {
		points, err := r.queryRawBuckets(ctx, req.ServerID, req.From, req.To, interval)
		if err != nil {
			return nil, err
		}
		result.Points = points
		result.Source = "raw"
		result.Downsampled = result.ActualInterval != result.RequestedInterval && result.RequestedInterval != "auto"
		result.Coverage, result.Downsampled = summarize(result.Points, req.From, req.To, interval, result.Downsampled)
		return result, nil
	}

	// Over the raw budget: only a change of data source may reduce raw input. The
	// rollup covers complete materialized buckets; the edges and any unmaterialized
	// bucket fall back to bound raw reads.
	if interval != "6h" {
		if !auto && !req.AllowCoarsen {
			return nil, &QueryBudgetError{Reason: ReasonBucketBudget, Recommended: "6h", Samples: samples}
		}
		interval = "6h"
		if !auto {
			result.CoarsenReason = "raw_budget"
		}
		result.ActualInterval = interval
	}
	points, usedRollup, err := r.queryRollupWithEdges(ctx, req.ServerID, req.From, req.To)
	if err != nil {
		return nil, err
	}
	result.Points = points
	switch {
	case usedRollup && len(points) > 0:
		result.Source = "rollup"
	case usedRollup:
		result.Source = "rollup"
	default:
		result.Source = "raw"
	}
	result.Downsampled = true
	result.Coverage, result.Downsampled = summarize(result.Points, req.From, req.To, interval, true)
	return result, nil
}

// probeRawSamples reads at most 12,001 candidate keys, so an over-budget range is
// detected without an unbounded COUNT or a full scan.
func (r *MetricRepo) probeRawSamples(ctx context.Context, serverID uint, from, to time.Time) (int, bool, error) {
	var keys []time.Time
	err := r.db.WithContext(ctx).Raw(
		`SELECT time FROM metrics WHERE server_id = ? AND time >= ? AND time < ? ORDER BY time LIMIT ?`,
		serverID, from, to, rawProbeLimit).Scan(&keys).Error
	if err != nil {
		return 0, false, classifyBudgetError(fmt.Errorf("probe raw samples: %w", err))
	}
	if len(keys) > MaxRawSamples {
		return len(keys), true, nil
	}
	return len(keys), false, nil
}

// summarize derives coverage (valid sampled fraction of the requested span) and
// keeps the caller's downsampled flag.
func summarize(points []AggregatedMetric, from, to time.Time, interval string, downsampled bool) (float64, bool) {
	if len(points) == 0 {
		return 0, downsampled
	}
	buckets := bucketCount(from, to, interval)
	if buckets <= 0 {
		return 0, downsampled
	}
	withData := 0
	for _, point := range points {
		if point.CPUPercent != nil || point.NetRecvCov != nil {
			withData++
		}
	}
	return float64(withData) / float64(buckets), downsampled
}
