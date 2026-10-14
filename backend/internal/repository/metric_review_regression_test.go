package repository

import (
	"context"
	"math"
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
	// F03: the threshold follows the period exactly, without a clamp that would
	// change the meaning of legitimate configurations.
	if got := DeltaGapForInterval(2 * time.Second); got != 6*time.Second {
		t.Fatalf("2s interval → %v, want exactly 3x = 6s", got)
	}
	if got := DeltaGapForInterval(7200 * time.Second); got != 6*time.Hour {
		t.Fatalf("7200s interval → %v, want exactly 3x = 6h", got)
	}
	repo.SetDeltaGapThreshold(10 * time.Second)
	if got := repo.gapThreshold(); got != 30*time.Second {
		t.Fatalf("10s interval → %v, want 30s (a 40s gap must be a break)", got)
	}
	repo.SetDeltaGapThreshold(7200 * time.Second)
	if got := repo.gapThreshold(); got != 6*time.Hour {
		t.Fatalf("7200s interval → %v, want 6h (a healthy pair must count)", got)
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

// TestReviewRetentionDropsCompleteChunks pins gap A's physical reclamation: whole
// complete chunks are dropped, and a chunk holding unaggregated raw data for a
// pending server is kept.
func TestReviewRetentionDropsCompleteChunks(t *testing.T) {
	db := newTestDBWithExtensions(t)
	ctx := context.Background()
	// A hypertable with daily chunks, so the drop path is exercised for real:
	// create the model's table first, then convert it.
	if err := db.AutoMigrate(&model.Metric{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`SELECT create_hypertable('metrics', 'time', chunk_time_interval => interval '1 day', migrate_data => true)`).Error; err != nil {
		t.Skipf("hypertable not available in this environment: %v", err)
	}
	if err := PrepareMetricsRollupSchema(db); err != nil {
		t.Fatal(err)
	}
	repo := NewMetricRepo(db)

	settled := time.Date(2026, 1, 2, 0, 10, 0, 0, time.UTC)
	pending := time.Date(2026, 1, 3, 0, 10, 0, 0, time.UTC)
	for _, row := range []model.Metric{
		{ServerID: 91, Time: settled, CPUPercent: f64(10), NetBytesRecv: i64(0)},
		{ServerID: 92, Time: pending, CPUPercent: f64(10), NetBytesRecv: i64(0)},
	} {
		sample := row
		if err := db.Create(&sample).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Server 91's aggregation is confirmed past the cutoff; server 92's is not.
	if err := db.Exec(`INSERT INTO metrics_rollup_checkpoints (server_id, completed_through, updated_at)
		VALUES (91, ?, now())`, pending.Add(48*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	plan, err := repo.PlanPrune(ctx, now, MetricsRetention)
	if err != nil {
		t.Fatalf("PlanPrune: %v", err)
	}
	if len(plan.Pending) != 1 || plan.Pending[0] != 92 {
		t.Fatalf("pending = %v, want [92]", plan.Pending)
	}
	result, err := repo.PruneMetrics(ctx, plan)
	if err != nil {
		t.Fatalf("PruneMetrics: %v", err)
	}
	if result.ChunksDropped == 0 {
		t.Fatal("a complete chunk with no pending server must be dropped, not row-deleted")
	}
	var remaining []uint
	if err := db.Raw(`SELECT DISTINCT server_id FROM metrics ORDER BY server_id`).Scan(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0] != 92 {
		t.Fatalf("servers with raw data = %v, want only the pending server 92", remaining)
	}
}

// TestReviewStorageReportMeasuresAndProjects pins gap A's capacity step: the
// report measures real sizes and the projection is linear and window-based.
func TestReviewStorageReportMeasuresAndProjects(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 50; i++ {
		row := model.Metric{
			ServerID: 93, Time: base.Add(time.Duration(i) * time.Minute),
			CPUPercent: f64(10), NetBytesRecv: i64(int64(i * 60)),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	report, err := repo.StorageReport(ctx)
	if err != nil {
		t.Fatalf("StorageReport: %v", err)
	}
	if report.RawRows != 50 {
		t.Fatalf("raw rows = %d, want 50", report.RawRows)
	}
	if report.RawTotalBytes <= 0 || report.RawTableBytes <= 0 {
		t.Fatalf("sizes are not measured: %+v", report)
	}
	if report.OldestRaw == nil || report.NewestRaw == nil {
		t.Fatalf("sample boundaries missing: %+v", report)
	}

	// The projection is linear in the measured window and is not applied to a
	// window too short to represent steady state.
	if got := ProjectStorage(1000, 7*24*time.Hour, MetricsRetention); got != 5000 {
		t.Fatalf("projection = %d, want 5000 (35d/7d × 1000)", got)
	}
	if got := ProjectStorage(1000, 30*time.Minute, MetricsRetention); got != 1000 {
		t.Fatalf("short measurement = %d, want it not extrapolated", got)
	}
}

// TestReviewQueryBudgetIsSharedAcrossPlans pins the deadline rule: the total
// budget is inherited from the caller and never restarted for a second plan, so a
// request that already consumed its deadline is refused as a timeout instead of
// being granted a fresh two seconds.
func TestReviewQueryBudgetIsSharedAcrossPlans(t *testing.T) {
	repo, db := budgetTestRepo(t)
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	from := base
	to := base.Add(time.Hour)
	if err := db.Create(&model.Metric{
		ServerID: 95, Time: base.Add(time.Minute), CPUPercent: f64(10), NetBytesRecv: i64(0),
	}).Error; err != nil {
		t.Fatal(err)
	}

	// A caller-supplied deadline shorter than the built-in budget must be honoured.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	time.Sleep(60 * time.Millisecond)

	start := time.Now()
	_, err := repo.QueryWithBudget(ctx, QueryRequest{ServerID: 95, From: from, To: to, Requested: "6h"})
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("query ran for %v after its deadline: the budget was restarted", elapsed)
	}
	if err == nil {
		t.Fatal("a query whose deadline already expired must be refused")
	}
	var budget *QueryBudgetError
	if !asBudgetError(err, &budget) || budget.Reason != ReasonQueryTimeout {
		t.Fatalf("err = %v, want the timeout refusal", err)
	}
}

// TestRereviewPartialWindowClipsCounterWeights pins F02: a rate pair's weight is
// its overlap with the requested window, so time before the window can neither
// inflate the average nor the reported coverage.
func TestRereviewPartialWindowClipsCounterWeights(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(96)
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	// Counters: 0s→0, 40s→400, 50s→1400; two pairs, 10 B/s and 100 B/s.
	for _, sample := range []struct {
		at    time.Duration
		value int64
	}{
		{0, 0}, {40 * time.Second, 400}, {50 * time.Second, 1400},
	} {
		row := model.Metric{
			ServerID: server, Time: base.Add(sample.at),
			CPUPercent: f64(50), NetBytesRecv: i64(sample.value),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	// The window starts at 30s: each pair contributes exactly 10s of valid
	// coverage, so the weighted average is (10*10 + 100*10) / 20 = 55.
	result, err := repo.QueryWithBudget(ctx, QueryRequest{
		ServerID: server, From: base.Add(30 * time.Second), To: base.Add(60 * time.Second), Requested: "6h",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(result.Points) != 1 || result.Points[0].NetRecvRate == nil {
		t.Fatalf("points = %+v, want one bucket with a rate", result.Points)
	}
	if got := *result.Points[0].NetRecvRate; got < 54.9 || got > 55.1 {
		t.Fatalf("weighted rate = %v, want 55 (window-clipped weights)", got)
	}
	if result.Points[0].NetRecvCov == nil || *result.Points[0].NetRecvCov < 19.9 || *result.Points[0].NetRecvCov > 20.1 {
		t.Fatalf("coverage = %v, want 20s inside the 30s window", result.Points[0].NetRecvCov)
	}
}

// TestRereviewShortCollectionPeriodRejectsLongGap pins F03's short-period case: a
// 10s collection period must treat a 40s gap as a break, which the old 1m floor
// silently accepted.
func TestRereviewShortCollectionPeriodRejectsLongGap(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(97)
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	for _, sample := range []struct {
		at    time.Duration
		value int64
	}{{0, 0}, {40 * time.Second, 400}} {
		row := model.Metric{ServerID: server, Time: base.Add(sample.at), NetBytesRecv: i64(sample.value)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo.SetDeltaGapThreshold(10 * time.Second) // threshold 30s
	rows, err := repo.Query(ctx, server, base.Add(-time.Second), base.Add(time.Minute), "15m")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, row := range rows {
		if row.NetRecvRate != nil {
			t.Fatalf("rate = %v, want unknown for a 40s gap at a 10s period", *row.NetRecvRate)
		}
	}
}

// TestRereviewLongCollectionPeriodProducesValidRate pins F03's long-period case: a
// healthy adjacent pair at a 2h period must still produce a rate, which the old 1h
// cap suppressed.
func TestRereviewLongCollectionPeriodProducesValidRate(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(98)
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, sample := range []struct {
		at    time.Duration
		value int64
	}{{0, 0}, {2 * time.Hour, 72000}} {
		row := model.Metric{ServerID: server, Time: base.Add(sample.at), NetBytesRecv: i64(sample.value)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo.SetDeltaGapThreshold(7200 * time.Second) // threshold 6h
	rows, err := repo.Query(ctx, server, base.Add(-time.Second), base.Add(3*time.Hour), "6h")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.NetRecvRate != nil {
			found = true
			if *row.NetRecvRate < 9.9 || *row.NetRecvRate > 10.1 {
				t.Fatalf("rate = %v, want 10 B/s", *row.NetRecvRate)
			}
		}
	}
	if !found {
		t.Fatal("a healthy pair at a 2h period must produce a rate")
	}
}

// TestRereviewBucketStatisticsAreReturned pins the F06 contract: every bucket
// carries sum/count so a window average can be sample-weighted.
func TestRereviewBucketStatisticsAreReturned(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(99)
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	// Two samples in one bucket, one of them a real zero.
	for i, cpu := range []float64{0, 100} {
		row := model.Metric{
			ServerID: server, Time: base.Add(time.Duration(i) * time.Minute),
			CPUPercent: f64(cpu), NetBytesRecv: i64(int64(i) * 60),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	result, err := repo.QueryWithBudget(ctx, QueryRequest{
		ServerID: server, From: base, To: base.Add(RollupBucket), Requested: "6h",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Points) != 1 {
		t.Fatalf("points = %d, want 1", len(result.Points))
	}
	point := result.Points[0]
	if point.CPUCount != 2 || point.CPUSum == nil || *point.CPUSum != 100 {
		t.Fatalf("bucket statistics = sum %v count %d, want 100/2", point.CPUSum, point.CPUCount)
	}
	// The materialized bucket keeps the same statistics for long-range averages.
	if _, err := repo.RefreshRollups(ctx, base.Add(RollupBucket).Add(time.Minute), RollupRefreshWindow); err != nil {
		t.Fatal(err)
	}
	points, _, err := repo.queryRollupWithEdges(ctx, server, base, base.Add(RollupBucket))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].CPUCount != 2 || points[0].CPUSum == nil || *points[0].CPUSum != 100 {
		t.Fatalf("rollup statistics = %+v, want 100/2", points)
	}
}

// TestRereviewCapacityUsesHypertableChunks pins F09: on TimescaleDB the pieces
// that hold the data are the chunks, so the measured total and the projection must
// include them instead of the parent's shell size.
func TestRereviewCapacityUsesHypertableChunks(t *testing.T) {
	db := newTestDBWithExtensions(t)
	ctx := context.Background()
	if err := db.AutoMigrate(&model.Metric{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`SELECT create_hypertable('metrics', 'time', chunk_time_interval => interval '1 day', migrate_data => true)`).Error; err != nil {
		t.Skipf("hypertable not available in this environment: %v", err)
	}
	if err := PrepareMetricsRollupSchema(db); err != nil {
		t.Fatal(err)
	}
	repo := NewMetricRepo(db)
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	// Spread rows over several days so more than one chunk exists.
	for i := 0; i < 200; i++ {
		row := model.Metric{
			ServerID: 100, Time: base.Add(time.Duration(i) * 3 * time.Hour),
			CPUPercent: f64(10), NetBytesRecv: i64(int64(i * 60)),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	report, err := repo.StorageReport(ctx)
	if err != nil {
		t.Fatalf("StorageReport: %v", err)
	}
	parentOnly := int64(0)
	if err := db.Raw(`SELECT pg_total_relation_size('metrics')`).Scan(&parentOnly).Error; err != nil {
		t.Fatal(err)
	}
	if report.RawChunkBytes <= 0 {
		t.Fatalf("chunk bytes not measured: %+v", report)
	}
	if report.RawTotalBytes < report.RawChunkBytes {
		t.Fatalf("total %d excludes chunks %d", report.RawTotalBytes, report.RawChunkBytes)
	}
	if report.RawTotalBytes <= parentOnly {
		t.Fatalf("total %d is not larger than the parent shell %d (F09 regression)", report.RawTotalBytes, parentOnly)
	}
	if report.ProjectedRetentionBytes < report.RawTotalBytes {
		t.Fatalf("projection %d below the measured footprint %d", report.ProjectedRetentionBytes, report.RawTotalBytes)
	}
}

// TestRereviewBootstrapDiscardPolicyIsExplicit pins §4: raw older than the
// aggregation backfill window is discarded without being aggregated, and that is
// stated as an explicit policy rather than recorded as complete aggregation.
func TestRereviewBootstrapDiscardPolicyIsExplicit(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(101)
	now := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)
	old := now.Add(-40 * 24 * time.Hour) // outside the 35d backfill window
	recent := now.Add(-2 * time.Hour)

	for _, sample := range []model.Metric{
		{ServerID: server, Time: old, CPUPercent: f64(10), NetBytesRecv: i64(0)},
		{ServerID: server, Time: recent, CPUPercent: f64(10), NetBytesRecv: i64(60)},
	} {
		row := sample
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	// The first refresh backfills only the documented window.
	if _, err := repo.RefreshRollups(ctx, now, RollupBackfillWindow); err != nil {
		t.Fatal(err)
	}
	oldBucket := BucketStart(old)
	row, err := repo.RollupRowFor(ctx, server, oldBucket)
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("raw outside the backfill window must not be materialized, got %+v", row)
	}
	// The retained window is aggregated, so the node is not pending and retention
	// may proceed: the discard of the older raw is the explicit exception.
	plan, err := repo.PlanPrune(ctx, now, MetricsRetention)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Pending) != 0 {
		t.Fatalf("pending = %v, want none (the recent window is aggregated)", plan.Pending)
	}
	if plan.RawRows != 1 {
		t.Fatalf("prunable rows = %d, want the single out-of-window sample", plan.RawRows)
	}
}

// TestRereviewInternalBucketOverlapSplitsRate is the N02 reproduction: a counter
// pair that crosses an internal bucket boundary must be split by overlap, not
// charged entirely to the bucket of its later sample.
//
// Request 12:00-12:02 at 1m buckets; the counter reads 0 at 12:00:40, 300 at
// 12:01:10 and 1300 at 12:01:20. The first pair is 10 B/s over 30s (20s of it in
// the 12:00 bucket, 10s in the 12:01 bucket) and the second is 100 B/s over 10s.
func TestRereviewInternalBucketOverlapSplitsRate(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(120)
	base := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	for _, sample := range []struct {
		at    time.Duration
		value int64
	}{
		{40 * time.Second, 0},
		{70 * time.Second, 300},  // 12:01:10
		{80 * time.Second, 1300}, // 12:01:20
	} {
		row := model.Metric{ServerID: server, Time: base.Add(sample.at), NetBytesRecv: i64(sample.value)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	result, err := repo.QueryWithBudget(ctx, QueryRequest{
		ServerID: server, From: base, To: base.Add(2 * time.Minute), Requested: "1m",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	byBucket := map[string]AggregatedMetric{}
	for _, point := range result.Points {
		byBucket[point.Time.UTC().Format("15:04")] = point
	}
	first, ok := byBucket["12:00"]
	if !ok {
		t.Fatalf("no 12:00 bucket in %+v", result.Points)
	}
	if first.NetRecvRate == nil || math.Abs(*first.NetRecvRate-10) > 0.001 {
		t.Fatalf("12:00 rate = %v, want 10 B/s (20s of the crossing pair)", first.NetRecvRate)
	}
	if first.NetRecvCov == nil || math.Abs(*first.NetRecvCov-20) > 0.001 {
		t.Fatalf("12:00 coverage = %v, want 20s", first.NetRecvCov)
	}
	second, ok := byBucket["12:01"]
	if !ok {
		t.Fatalf("no 12:01 bucket in %+v", result.Points)
	}
	// (10 B/s × 10s + 100 B/s × 10s) / 20s = 55 B/s.
	if second.NetRecvRate == nil || math.Abs(*second.NetRecvRate-55) > 0.001 {
		t.Fatalf("12:01 rate = %v, want 55 B/s (weighted split)", second.NetRecvRate)
	}
	if second.NetRecvCov == nil || math.Abs(*second.NetRecvCov-20) > 0.001 {
		t.Fatalf("12:01 coverage = %v, want 20s (not the whole 40s)", second.NetRecvCov)
	}
	// The gauges of the single 12:00 sample must not be duplicated into 12:01.
	if second.CPUCount != 0 || second.CPUSum != nil {
		t.Fatalf("12:01 gauges = count %d sum %v, want none: gauges are counted once",
			second.CPUCount, second.CPUSum)
	}
}

// TestRereviewInternalRollupBucketOverlapSplitsRate is the N02 reproduction for the
// materialized path: a pair crossing a 6h boundary must be stored split, otherwise
// the wrong weight is persisted and served to every long-range chart.
func TestRereviewInternalRollupBucketOverlapSplitsRate(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(121)
	// Samples straddle the 12:00 boundary: 11:59:40 → 0, 12:00:10 → 300 (10 B/s
	// over 30s: 20s in the 06:00 bucket, 10s in the 12:00 bucket).
	base := time.Date(2026, 1, 2, 11, 59, 40, 0, time.UTC)
	for _, sample := range []struct {
		at    time.Duration
		value int64
	}{{0, 0}, {30 * time.Second, 300}} {
		row := model.Metric{ServerID: server, Time: base.Add(sample.at), NetBytesRecv: i64(sample.value)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	to := time.Date(2026, 1, 2, 23, 0, 0, 0, time.UTC)
	if _, err := repo.RefreshRollups(ctx, to, 24*time.Hour); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	var rows []RollupRow
	if err := db.WithContext(ctx).Raw(
		`SELECT * FROM metrics_rollup_6h WHERE server_id = ? AND bucket_start >= ? AND bucket_start < ?
		 ORDER BY bucket_start`, server,
		time.Date(2026, 1, 2, 6, 0, 0, 0, time.UTC), time.Date(2026, 1, 2, 18, 0, 0, 0, time.UTC),
	).Scan(&rows).Error; err != nil {
		t.Fatalf("rollup rows: %v", err)
	}
	var six, twelve *RollupRow
	for i := range rows {
		switch rows[i].BucketStart.UTC().Format("15:04") {
		case "06:00":
			six = &rows[i]
		case "12:00":
			twelve = &rows[i]
		}
	}
	if six == nil || twelve == nil {
		t.Fatalf("materialized rows = %+v, want the 06:00 and 12:00 buckets", rows)
	}
	if six.NetRecvRate == nil || math.Abs(*six.NetRecvRate-10) > 0.001 {
		t.Fatalf("06:00 stored rate = %v, want 10 B/s", six.NetRecvRate)
	}
	if six.NetRecvCov == nil || math.Abs(*six.NetRecvCov-20) > 0.001 {
		t.Fatalf("06:00 stored coverage = %v, want 20s", six.NetRecvCov)
	}
	if twelve.NetRecvRate == nil || math.Abs(*twelve.NetRecvRate-10) > 0.001 {
		t.Fatalf("12:00 stored rate = %v, want 10 B/s for the crossing pair", twelve.NetRecvRate)
	}
	if twelve.NetRecvCov == nil || math.Abs(*twelve.NetRecvCov-10) > 0.001 {
		t.Fatalf("12:00 stored coverage = %v, want 10s", twelve.NetRecvCov)
	}
}

// TestRereviewHypertableComponentsComeFromDetailedSize is the N04 reproduction:
// table and index bytes must be the real components of the hypertable, not the
// parent shell's sizes reported next to a chunk-inclusive total.
func TestRereviewHypertableComponentsComeFromDetailedSize(t *testing.T) {
	db := newTestDBWithExtensions(t)
	ctx := context.Background()
	if err := db.AutoMigrate(&model.Metric{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`SELECT create_hypertable('metrics', 'time', chunk_time_interval => interval '1 day', migrate_data => true)`).Error; err != nil {
		t.Skipf("hypertable not available in this environment: %v", err)
	}
	if err := PrepareMetricsRollupSchema(db); err != nil {
		t.Fatal(err)
	}
	repo := NewMetricRepo(db)
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 2000; i++ {
		row := model.Metric{
			ServerID: 300, Time: base.Add(time.Duration(i) * time.Second),
			CPUPercent: f64(float64(i % 100)), NetBytesRecv: i64(int64(i * 60)),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	report, err := repo.StorageReport(ctx)
	if err != nil {
		t.Fatalf("StorageReport: %v", err)
	}
	// The authoritative components.
	var want struct {
		Total int64 `gorm:"column:total_bytes"`
		Table int64 `gorm:"column:table_bytes"`
		Index int64 `gorm:"column:index_bytes"`
		Toast int64 `gorm:"column:toast_bytes"`
	}
	if err := db.Raw(`SELECT COALESCE(SUM(total_bytes),0) AS total_bytes,
		COALESCE(SUM(table_bytes),0) AS table_bytes,
		COALESCE(SUM(index_bytes),0) AS index_bytes,
		COALESCE(SUM(toast_bytes),0) AS toast_bytes
		FROM hypertable_detailed_size('metrics')`).Scan(&want).Error; err != nil {
		t.Fatal(err)
	}
	parentTable := int64(0)
	if err := db.Raw(`SELECT pg_relation_size('metrics')`).Scan(&parentTable).Error; err != nil {
		t.Fatal(err)
	}
	if report.RawTableBytes != want.Table {
		t.Fatalf("table bytes = %d, want %d (the parent shell reports %d)",
			report.RawTableBytes, want.Table, parentTable)
	}
	if report.RawIndexBytes != want.Index {
		t.Fatalf("index bytes = %d, want %d", report.RawIndexBytes, want.Index)
	}
	if report.RawToastBytes != want.Toast {
		t.Fatalf("toast bytes = %d, want %d", report.RawToastBytes, want.Toast)
	}
	// The components must add up to the total the projection is based on.
	if sum := report.RawTableBytes + report.RawIndexBytes + report.RawToastBytes; sum != report.RawTotalBytes {
		t.Fatalf("components %d+%d+%d do not sum to the total %d",
			report.RawTableBytes, report.RawIndexBytes, report.RawToastBytes, report.RawTotalBytes)
	}
	if want.Table <= parentTable {
		t.Fatalf("fixture is not exercising the bug: detailed table bytes %d is not larger than "+
			"the parent shell %d", want.Table, parentTable)
	}
}

// TestRereviewGapIsNotReportedAsFullCoverage is the P2-01 reproduction: two samples
// 2h30s apart (beyond the delta threshold) leave 119 one-minute buckets with no
// gauge and no valid counter rate. Those buckets must not appear, and coverage must
// reflect the two buckets that really hold an observation - not 121/121.
func TestRereviewGapIsNotReportedAsFullCoverage(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(140)
	base := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	for _, sample := range []struct {
		at    time.Duration
		value int64
	}{
		{0, 0},
		{2*time.Hour + 30*time.Second, 9000},
	} {
		row := model.Metric{
			ServerID: server, Time: base.Add(sample.at),
			CPUPercent: f64(10), NetBytesRecv: i64(sample.value),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	result, err := repo.QueryWithBudget(ctx, QueryRequest{
		ServerID: server, From: base, To: base.Add(121 * time.Minute), Requested: "1m",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// Only the two buckets that contain a sample are observations; the 119 buckets
	// inside the outage carry nothing and must not be listed as data.
	if len(result.Points) != 2 {
		t.Fatalf("points = %d, want 2 (%+v)", len(result.Points), bucketTimes(result.Points))
	}
	want := 2.0 / 121.0
	if result.Coverage > want+0.001 || result.Coverage < want-0.001 {
		t.Fatalf("coverage = %v, want %v (2 observed buckets out of 121)", result.Coverage, want)
	}
	if result.Coverage >= 0.5 {
		t.Fatalf("coverage = %v: a 2h30m outage must not look covered", result.Coverage)
	}
	// Every returned bucket must be a real observation, and none may claim coverage
	// without any data.
	for _, point := range result.Points {
		if point.SampledAt == nil {
			t.Fatalf("returned bucket %v has no sampled_at", point.BucketString())
		}
		if point.NetRecvCov != nil && *point.NetRecvCov == 0 && point.CPUPercent == nil {
			t.Fatalf("bucket %v claims zero-coverage rate data", point.BucketString())
		}
	}
}

// TestRereviewRateOnlyBucketStillCounts pins the other half of P2-01: a bucket that
// holds a genuine rate pair but no sample inside it is real data and must count.
func TestRereviewRateOnlyBucketStillCounts(t *testing.T) {
	repo, db := budgetTestRepo(t)
	ctx := context.Background()
	const server = uint(141)
	base := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	// One long valid pair spanning two 1m buckets, with no sample inside the second.
	for _, sample := range []struct {
		at    time.Duration
		value int64
	}{
		{10 * time.Second, 0},
		{70 * time.Second, 600},
	} {
		row := model.Metric{ServerID: server, Time: base.Add(sample.at), NetBytesRecv: i64(sample.value)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	result, err := repo.QueryWithBudget(ctx, QueryRequest{
		ServerID: server, From: base, To: base.Add(2 * time.Minute), Requested: "1m",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(result.Points) != 2 {
		t.Fatalf("points = %d, want both buckets (one has gauges, one only rate)", len(result.Points))
	}
	seenPositiveCoverage := false
	for _, point := range result.Points {
		if point.NetRecvCov != nil && *point.NetRecvCov > 0 {
			seenPositiveCoverage = true
		}
	}
	if !seenPositiveCoverage {
		t.Fatalf("no bucket reports counter coverage: %+v", result.Points)
	}
	if result.Coverage < 0.9 {
		t.Fatalf("coverage = %v, want both buckets counted as observations", result.Coverage)
	}
}

// bucketTimes lists the bucket starts of a point set, for failure messages.
func bucketTimes(points []AggregatedMetric) []string {
	out := make([]string, 0, len(points))
	for _, point := range points {
		out = append(out, point.Time.UTC().Format("15:04:05"))
	}
	return out
}

// BucketString renders a bucket start for assertions.
func (m AggregatedMetric) BucketString() string {
	return m.Time.UTC().Format(time.RFC3339)
}
