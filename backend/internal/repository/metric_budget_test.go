package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

// seedRawSeries inserts n samples one minute apart ending at end, with a simple
// ramp so the average and the maximum differ.
func seedRawSeries(t *testing.T, db *gorm.DB, serverID uint, end time.Time, n int) {
	t.Helper()
	// One statement keeps the 12,001-row boundary fixtures fast.
	err := db.Exec(`
		INSERT INTO metrics (server_id, time, cpu_percent, net_bytes_recv)
		SELECT ?, ?::timestamptz - (i || ' minutes')::interval, 10 + (i % 7)::float8, (i * 600)::bigint
		FROM generate_series(0, ?) AS i`,
		serverID, end, n-1).Error
	if err != nil {
		t.Fatalf("seed %d samples: %v", n, err)
	}
}

func budgetTestRepo(t *testing.T) (*MetricRepo, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.Metric{}); err != nil {
		t.Fatal(err)
	}
	if err := PrepareMetricsRollupSchema(db); err != nil {
		t.Fatal(err)
	}
	return NewMetricRepo(db), db
}

// TestBucketBudgetBoundary pins the 600/601 bucket rule including both partial
// edge buckets.
func TestBucketBudgetBoundary(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := bucketCount(start, start.Add(600*time.Hour), "1h"); got != 600 {
		t.Fatalf("600 buckets in 600h at 1h, got %d", got)
	}
	if got := bucketCount(start, start.Add(601*time.Hour), "1h"); got != 601 {
		t.Fatalf("601 buckets in 601h at 1h, got %d", got)
	}
	// A range that is not bucket-aligned still counts both partial edges.
	if got := bucketCount(start.Add(30*time.Minute), start.Add(90*time.Minute), "1h"); got != 2 {
		t.Fatalf("partial edges = %d buckets, want 2", got)
	}
	if recommended, ok := recommendedInterval(start, start.Add(601*time.Hour)); !ok || recommended != "6h" {
		t.Fatalf("recommendation = %q (%v), want 6h", recommended, ok)
	}
	if got := bucketCount(start, start.Add(30*24*time.Hour), "6h"); got != 120 {
		t.Fatalf("30d at 6h = %d buckets, want 120", got)
	}
}

// TestStrictBucketBudgetIsRefusedWithRecommendation pins the 422-class refusal:
// an explicit granularity that cannot fit is never silently changed.
func TestStrictBucketBudgetIsRefusedWithRecommendation(t *testing.T) {
	repo, _ := budgetTestRepo(t)
	ctx := context.Background()
	to := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	from := to.Add(-601 * time.Hour)

	_, err := repo.QueryWithBudget(ctx, QueryRequest{
		ServerID: 1, From: from, To: to, Requested: "1h", Stat: "avg",
	})
	var budget *QueryBudgetError
	if !errors.As(err, &budget) || budget.Reason != ReasonBucketBudget {
		t.Fatalf("err = %v, want a bucket-budget refusal", err)
	}
	if budget.Recommended != "6h" {
		t.Fatalf("recommended = %q, want 6h", budget.Recommended)
	}

	// With allow_coarsen the same request succeeds at the coarser granularity.
	result, err := repo.QueryWithBudget(ctx, QueryRequest{
		ServerID: 1, From: from, To: to, Requested: "1h", AllowCoarsen: true, Stat: "avg",
	})
	if err != nil {
		t.Fatalf("coarsened query: %v", err)
	}
	if result.ActualInterval != "6h" || !result.Downsampled {
		t.Fatalf("result = interval %s downsampled %v, want 6h/downsampled", result.ActualInterval, result.Downsampled)
	}
	if result.CoarsenReason == "" {
		t.Fatal("a coarsened response must explain why")
	}
}

// TestRawSampleBudgetBoundary pins 12,000/12,001 planned raw samples and the
// rollup-not-ready refusal when the coarsest plan still needs too much raw data.
func TestRawSampleBudgetBoundary(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	to := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	// One-minute samples occupy ~8.4 days, so the window must cover them and the
	// newest sample must stay strictly inside [from,to).
	from := to.Add(-9 * 24 * time.Hour)
	last := to.Add(-time.Minute)

	// 12,000 samples fit exactly: the raw plan answers.
	seedRawSeries(t, db, 1, last, MaxRawSamples)
	result, err := repo.QueryWithBudget(ctx, QueryRequest{ServerID: 1, From: from, To: to, Requested: "1h"})
	if err != nil {
		t.Fatalf("12,000 samples must fit the raw budget: %v", err)
	}
	if result.Source != "raw" {
		t.Fatalf("source = %s, want raw", result.Source)
	}

	// One more sample exceeds the probe budget, so the coarsest plan must refuse
	// instead of scanning beyond it.
	seedRawSeries(t, db, 2, last, MaxRawSamples+1)
	_, err = repo.QueryWithBudget(ctx, QueryRequest{ServerID: 2, From: from, To: to, Requested: "6h"})
	var budget *QueryBudgetError
	if !errors.As(err, &budget) || budget.Reason != ReasonRollupNotReady {
		t.Fatalf("err = %v, want %s", err, ReasonRollupNotReady)
	}
	if budget.Samples <= MaxRawSamples {
		t.Fatalf("reported samples = %d, want the over-budget count", budget.Samples)
	}
}

// TestExpiredDeadlineIsReportedAsTimeout pins the shared 2s deadline mapping.
func TestExpiredDeadlineIsReportedAsTimeout(t *testing.T) {
	repo, _ := budgetTestRepo(t)
	// An already-passed deadline is the documented timeout case.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	to := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	_, err := repo.QueryWithBudget(ctx, QueryRequest{ServerID: 1, From: to.Add(-time.Hour), To: to, Requested: "1m"})
	var budget *QueryBudgetError
	if !errors.As(err, &budget) || budget.Reason != ReasonQueryTimeout {
		t.Fatalf("err = %v, want %s", err, ReasonQueryTimeout)
	}
}

// TestAverageAndObservedPeakAreSeparateAndSurviveRollup pins TC-04-02: the raw
// plan returns the average and the observed maximum separately, and a long-range
// query served from the rollup keeps the sampled maximum.
func TestAverageAndObservedPeakAreSeparateAndSurviveRollup(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(5)
	bucket := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	// Four samples inside one 6h bucket: cpu 10/20/30/40 → average 25, peak 40.
	for i, cpu := range []float64{10, 20, 30, 40} {
		row := model.Metric{
			ServerID: server, Time: bucket.Add(time.Duration(i) * time.Minute),
			CPUPercent: f64(cpu), NetBytesRecv: i64(int64(i) * 600),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	result, err := repo.QueryWithBudget(ctx, QueryRequest{
		ServerID: server, From: bucket, To: bucket.Add(RollupBucket), Requested: "6h",
	})
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}
	if len(result.Points) == 0 {
		t.Fatal("no points returned")
	}
	point := result.Points[0]
	if point.CPUPercent == nil || *point.CPUPercent != 25 {
		t.Fatalf("average = %v, want 25", point.CPUPercent)
	}
	if point.CPUPeak == nil || *point.CPUPeak != 40 {
		t.Fatalf("peak = %v, want the observed maximum 40", point.CPUPeak)
	}

	// Materialize the bucket and query the long range: the peak must survive.
	now := bucket.Add(RollupBucket).Add(time.Minute)
	if _, err := repo.RefreshRollups(ctx, now, RollupBackfillWindow); err != nil {
		t.Fatalf("RefreshRollups: %v", err)
	}
	// Read the long range through the stitched coarsest plan directly: the raw
	// plan itself is legitimately preferred while a range stays inside the raw
	// budget, so the rollup reader is exercised here.
	points, usedRollup, err := repo.queryRollupWithEdges(ctx, server, bucket.Add(-12*time.Hour), bucket.Add(RollupBucket))
	if err != nil {
		t.Fatalf("rollup query: %v", err)
	}
	if !usedRollup {
		t.Fatalf("the materialized bucket must be served from the rollup")
	}
	var found *AggregatedMetric
	for i := range points {
		if points[i].Time.Equal(bucket) {
			found = &points[i]
		}
	}
	if found == nil {
		t.Fatalf("the materialized bucket is missing from the long-range response: %+v", points)
	}
	if found.CPUPercent == nil || *found.CPUPercent != 25 {
		t.Fatalf("rollup average = %v, want 25", found.CPUPercent)
	}
	if found.CPUPeak == nil || *found.CPUPeak != 40 {
		t.Fatalf("rollup peak = %v, want the retained maximum 40", found.CPUPeak)
	}
}
