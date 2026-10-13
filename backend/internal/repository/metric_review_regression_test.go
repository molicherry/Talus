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

// TestReviewRetentionEventuallyPrunesOfflineServers pins R09: once a node stops
// reporting, its already aggregated history must still be prunable — the
// checkpoint is not required to keep following wall-clock time.
func TestReviewRetentionEventuallyPrunesOfflineServers(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(61)
	old := time.Date(2026, 1, 2, 0, 10, 0, 0, time.UTC)
	if err := db.Create(&model.Metric{
		ServerID: server, Time: old, CPUPercent: f64(10), NetBytesRecv: i64(0),
	}).Error; err != nil {
		t.Fatal(err)
	}

	// First cycle materializes the bucket and advances the checkpoint.
	first := time.Date(2026, 1, 2, 6, 1, 0, 0, time.UTC)
	if _, err := repo.RefreshRollups(ctx, first, RollupRefreshWindow); err != nil {
		t.Fatal(err)
	}

	// The node goes silent; 38 days later the sample is beyond retention.
	later := first.Add(38 * 24 * time.Hour)
	if _, err := repo.RefreshRollups(ctx, later, RollupRefreshWindow); err != nil {
		t.Fatal(err)
	}
	plan, err := repo.PlanPrune(ctx, later, MetricsRetention)
	if err != nil {
		t.Fatalf("PlanPrune: %v", err)
	}
	if len(plan.Pending) != 0 {
		t.Fatalf("pending = %v, want an offline node's aggregated history to be prunable", plan.Pending)
	}
	if plan.RawRows != 1 {
		t.Fatalf("prunable raw rows = %d, want 1", plan.RawRows)
	}
	result, err := repo.PruneMetrics(ctx, plan)
	if err != nil {
		t.Fatalf("PruneMetrics: %v", err)
	}
	if result.Paused || result.RawRows != 1 {
		t.Fatalf("prune result = %+v, want an applied prune of 1 row", result)
	}
	var remaining int64
	if err := db.Raw(`SELECT COUNT(*) FROM metrics WHERE server_id = ?`, server).Scan(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("raw rows left = %d, want the expired sample pruned", remaining)
	}
}

// TestReviewGapThresholdTracksConfiguredCollectionInterval pins R07: the
// sampling-gap threshold follows the effective collection interval instead of a
// fixed three minutes, in both directions.
func TestReviewGapThresholdTracksConfiguredCollectionInterval(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(71)
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)

	// A healthy 600s cadence: 6000 bytes over 600s is 10 B/s.
	for _, sample := range []model.Metric{
		{ServerID: server, Time: base, NetBytesRecv: i64(0), CPUPercent: f64(1)},
		{ServerID: server, Time: base.Add(600 * time.Second), NetBytesRecv: i64(6000), CPUPercent: f64(1)},
	} {
		row := sample
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	query := func() *float64 {
		t.Helper()
		rows, err := repo.Query(ctx, server, base.Add(-time.Second), base.Add(1200*time.Second), "15m")
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		for _, row := range rows {
			if row.NetRecvRate != nil {
				return row.NetRecvRate
			}
		}
		return nil
	}

	// The default 3 minute threshold rejects the 10 minute spacing.
	if rate := query(); rate != nil {
		t.Fatalf("rate = %v with the default threshold, want unknown", *rate)
	}
	// A 600s collection interval tolerates it.
	repo.SetDeltaGapThreshold(600 * time.Second)
	if rate := query(); rate == nil || *rate != 10 {
		t.Fatalf("rate = %v, want 10 B/s once the threshold follows the interval", rate)
	}
	// A dense 30s configuration must not accept a two-minute gap.
	repo.SetDeltaGapThreshold(30 * time.Second)
	if rate := query(); rate != nil {
		t.Fatalf("rate = %v with a 30s configuration, want the gap rejected", *rate)
	}
	// The derived values themselves are clamped.
	if got := DeltaGapForInterval(600 * time.Second); got != 30*time.Minute {
		t.Fatalf("600s interval → %v, want 30m", got)
	}
	if got := DeltaGapForInterval(2 * time.Second); got != time.Minute {
		t.Fatalf("2s interval → %v, want the 1m floor", got)
	}
}

// TestReviewSampleTimeIsNotTheBucketStart pins R18: responses carry the real
// sample time so freshness never has to be derived from an aggregation boundary.
func TestReviewSampleTimeIsNotTheBucketStart(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(81)
	bucket := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	lastSample := bucket.Add(2 * time.Minute)

	for i, offset := range []time.Duration{time.Minute, 2 * time.Minute} {
		row := model.Metric{
			ServerID: server, Time: bucket.Add(offset), CPUPercent: f64(float64(10 * (i + 1))),
			NetBytesRecv: i64(int64(60 * i)),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Raw path: sampled_at is the newest sample in the bucket.
	result, err := repo.QueryWithBudget(ctx, QueryRequest{
		ServerID: server, From: bucket, To: bucket.Add(RollupBucket), Requested: "6h",
	})
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}
	if len(result.Points) != 1 || result.Points[0].SampledAt == nil {
		t.Fatalf("raw point = %+v, want a sampled_at", result.Points)
	}
	if !result.Points[0].SampledAt.UTC().Equal(lastSample) {
		t.Fatalf("raw sampled_at = %v, want the newest sample %v", result.Points[0].SampledAt, lastSample)
	}

	// Rollup path: the materialized last_sample is preserved.
	if _, err := repo.RefreshRollups(ctx, bucket.Add(RollupBucket).Add(time.Minute), RollupRefreshWindow); err != nil {
		t.Fatal(err)
	}
	points, _, err := repo.queryRollupWithEdges(ctx, server, bucket, bucket.Add(RollupBucket))
	if err != nil {
		t.Fatalf("rollup query: %v", err)
	}
	if len(points) != 1 || points[0].SampledAt == nil {
		t.Fatalf("rollup point = %+v, want a sampled_at", points)
	}
	if !points[0].SampledAt.UTC().Equal(lastSample) {
		t.Fatalf("rollup sampled_at = %v, want %v", points[0].SampledAt, lastSample)
	}
	if points[0].SampledAt.UTC().Equal(points[0].Time.UTC()) {
		t.Fatal("sampled_at must differ from the bucket start here")
	}
}
