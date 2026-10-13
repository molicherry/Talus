package repository

import (
	"context"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
)

// TestReviewPredecessorIsRateContextOnly pins R03: the predecessor sample scanned
// for lag must not add output points, must not contribute gauge observations and
// must not widen the reported coverage.
func TestReviewPredecessorIsRateContextOnly(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(41)
	from := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)

	rows := []model.Metric{
		// Predecessor, strictly before the window: CPU 99 must never be observed.
		{ServerID: server, Time: from.Add(-time.Minute), CPUPercent: f64(99), NetBytesRecv: i64(0)},
		{ServerID: server, Time: from, CPUPercent: f64(10), NetBytesRecv: i64(600)},
		{ServerID: server, Time: from.Add(time.Minute), CPUPercent: f64(10), NetBytesRecv: i64(1200)},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	result, err := repo.QueryWithBudget(ctx, QueryRequest{
		ServerID: server, From: from, To: from.Add(2 * time.Minute), Requested: "1m",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(result.Points) != 2 {
		t.Fatalf("points = %d, want exactly the 2 in-window buckets", len(result.Points))
	}
	for _, point := range result.Points {
		if !point.Time.Equal(from) && !point.Time.Equal(from.Add(time.Minute)) {
			t.Fatalf("point outside the window: %v", point.Time)
		}
		if point.CPUPercent == nil || *point.CPUPercent != 10 {
			t.Fatalf("average = %v, want 10 (predecessor excluded)", point.CPUPercent)
		}
		if point.CPUPeak == nil || *point.CPUPeak != 10 {
			t.Fatalf("peak = %v, want 10 (predecessor excluded)", point.CPUPeak)
		}
	}
	if result.Coverage > 1 {
		t.Fatalf("coverage = %v, want at most 1", result.Coverage)
	}
}

// TestReviewPredecessorCountsAgainstRawBudget pins R03's budget half: the
// predecessor rows the query actually reads are part of the planned input.
func TestReviewPredecessorCountsAgainstRawBudget(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	to := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	from := to.Add(-9 * 24 * time.Hour)
	last := to.Add(-time.Minute)
	seedRawSeries(t, db, 42, last, MaxRawSamples)

	// One more sample just before the window completes 12,001 read rows once the
	// predecessor scan is counted.
	if err := db.Create(&model.Metric{
		ServerID: 42, Time: from.Add(-time.Minute), CPUPercent: f64(1), NetBytesRecv: i64(0),
	}).Error; err != nil {
		t.Fatal(err)
	}

	_, err := repo.QueryWithBudget(ctx, QueryRequest{ServerID: 42, From: from, To: to, Requested: "6h"})
	var budget *QueryBudgetError
	if err == nil {
		t.Fatal("the predecessor scan must count against the raw budget")
	}
	if !asBudgetError(err, &budget) || budget.Reason != ReasonRollupNotReady {
		t.Fatalf("err = %v, want %s", err, ReasonRollupNotReady)
	}
}

// TestReviewRollupEdgesUseRequestBounds pins R05: a partial edge bucket only
// observes samples inside the requested window.
func TestReviewRollupEdgesUseRequestBounds(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(43)
	// The request sits inside the 06:00–12:00 bucket, so that bucket is an edge
	// span rather than a materialized one.
	from := time.Date(2026, 1, 2, 6, 30, 0, 0, time.UTC)
	to := time.Date(2026, 1, 2, 6, 45, 0, 0, time.UTC)
	for _, sample := range []model.Metric{
		{ServerID: server, Time: time.Date(2026, 1, 2, 6, 20, 0, 0, time.UTC), CPUPercent: f64(99), NetBytesRecv: i64(0)},
		{ServerID: server, Time: time.Date(2026, 1, 2, 6, 35, 0, 0, time.UTC), CPUPercent: f64(10), NetBytesRecv: i64(60)},
		{ServerID: server, Time: time.Date(2026, 1, 2, 7, 5, 0, 0, time.UTC), CPUPercent: f64(77), NetBytesRecv: i64(600)},
	} {
		row := sample
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	points, _, err := repo.queryRollupWithEdges(ctx, server, from, to)
	if err != nil {
		t.Fatalf("stitched query: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("points = %d, want the single edge bucket", len(points))
	}
	if points[0].CPUPercent == nil || *points[0].CPUPercent != 10 {
		t.Fatalf("average = %v, want 10 (only the in-window sample)", points[0].CPUPercent)
	}
	if points[0].CPUPeak == nil || *points[0].CPUPeak != 10 {
		t.Fatalf("peak = %v, want 10", points[0].CPUPeak)
	}
}

// TestReviewFractionalEndpointCountsAllBuckets pins R08: a fractional-second
// endpoint must not drop a partial bucket.
func TestReviewFractionalEndpointCountsAllBuckets(t *testing.T) {
	from := time.Date(2026, 1, 2, 12, 0, 0, 500_000_000, time.UTC)
	to := time.Date(2026, 1, 2, 22, 0, 0, 500_000_000, time.UTC)
	if got := bucketCount(from, to, "1m"); got != 601 {
		t.Fatalf("fractional endpoints = %d buckets, want 601", got)
	}
	// An exactly aligned range stays at its own count.
	if got := bucketCount(from.Truncate(time.Minute), to.Truncate(time.Minute), "1m"); got != 600 {
		t.Fatalf("aligned range = %d buckets, want 600", got)
	}
}

func asBudgetError(err error, target **QueryBudgetError) bool {
	for err != nil {
		if typed, ok := err.(*QueryBudgetError); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// TestReviewRefreshRecoversGapBeyond48Hours pins R04: a checkpoint older than the
// normal refresh window resumes from the unconfirmed bucket instead of skipping
// the hole, and the checkpoint only advances over the range that was processed.
func TestReviewRefreshRecoversGapBeyond48Hours(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(51)

	gapBucket := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&model.Metric{
		ServerID: server, Time: gapBucket.Add(10 * time.Minute), CPUPercent: f64(10), NetBytesRecv: i64(0),
	}).Error; err != nil {
		t.Fatal(err)
	}
	// The checkpoint stops well before the gap and the failed cycles never ran.
	if err := db.Exec(`INSERT INTO metrics_rollup_checkpoints (server_id, completed_through, updated_at)
		VALUES (?, ?, now()) ON CONFLICT (server_id) DO UPDATE SET completed_through = EXCLUDED.completed_through`,
		server, time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 1, 7, 12, 0, 0, 0, time.UTC)
	if _, err := repo.RefreshRollups(ctx, now, RollupRefreshWindow); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	row, err := repo.RollupRowFor(ctx, server, gapBucket)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.Samples != 1 {
		t.Fatalf("the skipped bucket was not materialized: %+v", row)
	}
	var checkpoint time.Time
	if err := db.Raw(`SELECT completed_through FROM metrics_rollup_checkpoints WHERE server_id = ?`, server).
		Scan(&checkpoint).Error; err != nil {
		t.Fatal(err)
	}
	if !checkpoint.Equal(now) {
		t.Fatalf("checkpoint = %v, want %v", checkpoint, now)
	}
}

// TestReviewRefreshRecomputesRecentClosedBuckets pins R06: a late sample must be
// folded into an already materialized bucket on the next normal refresh.
func TestReviewRefreshRecomputesRecentClosedBuckets(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(52)
	bucket := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	now := bucket.Add(RollupBucket).Add(time.Minute)

	if err := db.Create(&model.Metric{
		ServerID: server, Time: bucket.Add(time.Minute), CPUPercent: f64(10), NetBytesRecv: i64(0),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RefreshRollups(ctx, now, RollupRefreshWindow); err != nil {
		t.Fatal(err)
	}
	first, err := repo.RollupRowFor(ctx, server, bucket)
	if err != nil || first == nil || first.CPUCount != 1 {
		t.Fatalf("initial materialization = %+v (%v)", first, err)
	}

	// A late sample arrives after the bucket was closed and materialized.
	if err := db.Create(&model.Metric{
		ServerID: server, Time: bucket.Add(2 * time.Minute), CPUPercent: f64(100), NetBytesRecv: i64(60),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RefreshRollups(ctx, now, RollupRefreshWindow); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	second, err := repo.RollupRowFor(ctx, server, bucket)
	if err != nil || second == nil {
		t.Fatalf("recomputed row missing: %v", err)
	}
	if second.CPUCount != 2 || second.CPUSum == nil || *second.CPUSum != 110 {
		t.Fatalf("recomputed bucket = count %d sum %v, want 2/110", second.CPUCount, second.CPUSum)
	}
}

// TestReviewMissingRollupFallsBackToRaw pins R04's read half: a checkpoint that
// claims a bucket is materialized while its row is missing must not produce an
// empty result — the raw fallback answers instead.
func TestReviewMissingRollupFallsBackToRaw(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(53)
	bucket := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	to := bucket.Add(RollupBucket)

	if err := db.Create(&model.Metric{
		ServerID: server, Time: bucket.Add(time.Minute), CPUPercent: f64(10), NetBytesRecv: i64(0),
	}).Error; err != nil {
		t.Fatal(err)
	}
	// The checkpoint claims completeness well past the bucket, but no rollup row
	// was ever written for it.
	if err := db.Exec(`INSERT INTO metrics_rollup_checkpoints (server_id, completed_through, updated_at)
		VALUES (?, ?, now())`, server, to.Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}

	points, usedRollup, err := repo.queryRollupWithEdges(ctx, server, bucket, to)
	if err != nil {
		t.Fatalf("stitched query: %v", err)
	}
	if usedRollup {
		t.Fatal("a bucket without a materialized row must not be served as rollup")
	}
	if len(points) != 1 {
		t.Fatalf("points = %d, want the raw fallback bucket", len(points))
	}
	if points[0].CPUPercent == nil || *points[0].CPUPercent != 10 {
		t.Fatalf("average = %v, want 10 from the raw fallback", points[0].CPUPercent)
	}
}
