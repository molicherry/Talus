package repository

import (
	"context"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
)

func rollupTestNow() time.Time {
	return time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
}

// TestPrepareMetricsRollupSchemaIsIdempotent pins the migration behaviour: the
// schema and its marker are applied once and re-running is a no-op.
func TestPrepareMetricsRollupSchemaIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	for i := 0; i < 2; i++ {
		if err := PrepareMetricsRollupSchema(db); err != nil {
			t.Fatalf("apply %d: %v", i+1, err)
		}
	}
	var markers int64
	if err := db.Raw(`SELECT COUNT(*) FROM schema_migrations WHERE id = ?`, metricsRollupMigrationID).Scan(&markers).Error; err != nil {
		t.Fatal(err)
	}
	if markers != 1 {
		t.Fatalf("migration markers = %d, want exactly 1", markers)
	}
	for _, table := range []string{"metrics_rollup_6h", "metrics_rollup_checkpoints"} {
		var exists bool
		if err := db.Raw(`SELECT to_regclass(?) IS NOT NULL`, table).Scan(&exists).Error; err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("table %s was not created", table)
		}
	}
}

// TestRefreshRollupsMaterializesBucketsAndCheckpoint pins TC-08-06's core: closed
// buckets are materialized with valid counts, sums, peaks and coverage, empty
// buckets are explicitly confirmed, the open bucket is excluded, the checkpoint
// advances in the same transaction, and recomputation is idempotent.
func TestRefreshRollupsMaterializesBucketsAndCheckpoint(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.Metric{}); err != nil {
		t.Fatal(err)
	}
	if err := PrepareMetricsRollupSchema(db); err != nil {
		t.Fatal(err)
	}
	repo := NewMetricRepo(db)
	ctx := context.Background()
	now := rollupTestNow()
	const server = uint(7)

	// Bucket 1 (00:00): two samples 60s apart with a real increase.
	// Bucket 2 (06:00): deliberately empty.
	// Bucket 3 (12:00): the open bucket, which must not be materialized yet.
	rows := []model.Metric{
		{ServerID: server, Time: time.Date(2026, 1, 2, 0, 10, 0, 0, time.UTC), CPUPercent: f64(10), NetBytesRecv: i64(0)},
		{ServerID: server, Time: time.Date(2026, 1, 2, 0, 11, 0, 0, time.UTC), CPUPercent: f64(30), NetBytesRecv: i64(600)},
		{ServerID: server, Time: time.Date(2026, 1, 2, 12, 5, 0, 0, time.UTC), CPUPercent: f64(99), NetBytesRecv: i64(9999)},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	result, err := repo.RefreshRollups(ctx, now, RollupRefreshWindow)
	if err != nil {
		t.Fatalf("RefreshRollups: %v", err)
	}
	if !result.Checkpoint.Equal(now) {
		t.Fatalf("checkpoint = %v, want %v", result.Checkpoint, now)
	}

	first := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	bucket, err := repo.RollupRowFor(ctx, server, first)
	if err != nil {
		t.Fatal(err)
	}
	if bucket == nil {
		t.Fatal("the first closed bucket was not materialized")
	}
	if bucket.Samples != 2 {
		t.Fatalf("samples = %d, want 2", bucket.Samples)
	}
	if bucket.CPUSum == nil || *bucket.CPUSum != 40 {
		t.Fatalf("cpu_sum = %v, want 40", bucket.CPUSum)
	}
	if bucket.CPUMax == nil || *bucket.CPUMax != 30 {
		t.Fatalf("cpu_max = %v, want 30", bucket.CPUMax)
	}
	if bucket.NetRecvCov == nil || *bucket.NetRecvCov != 60 {
		t.Fatalf("net coverage = %v, want 60s of valid coverage", bucket.NetRecvCov)
	}
	if bucket.NetRecvRate == nil || *bucket.NetRecvRate != 10 {
		t.Fatalf("net rate = %v, want 10 B/s", bucket.NetRecvRate)
	}
	if bucket.NetRecvPeak == nil || *bucket.NetRecvPeak != 10 {
		t.Fatalf("net peak = %v, want the sampled maximum 10", bucket.NetRecvPeak)
	}

	empty, err := repo.RollupRowFor(ctx, server, time.Date(2026, 1, 2, 6, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || empty.Samples != 0 {
		t.Fatalf("empty bucket = %+v, want an explicit zero-sample confirmation", empty)
	}

	open, err := repo.RollupRowFor(ctx, server, now)
	if err != nil {
		t.Fatal(err)
	}
	if open != nil {
		t.Fatalf("the open bucket %v must not be materialized", now)
	}

	// Idempotent recomputation: identical state, no duplicate rows.
	var before int64
	if err := db.Raw(`SELECT COUNT(*) FROM metrics_rollup_6h`).Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RefreshRollups(ctx, now, RollupRefreshWindow); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	var after int64
	if err := db.Raw(`SELECT COUNT(*) FROM metrics_rollup_6h`).Scan(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("rebuild changed the row count: %d -> %d", before, after)
	}
	repeated, err := repo.RollupRowFor(ctx, server, first)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.CPUSum == nil || *repeated.CPUSum != 40 || repeated.Samples != 2 {
		t.Fatalf("recomputed bucket = %+v, want the same values", repeated)
	}
}

// TestPruneMetricsPausesUntilAggregationIsComplete pins TC-08-06's retention
// guard: a dry run never deletes, incomplete aggregation pauses deletion, and a
// confirmed checkpoint lets retention proceed while rolled-up history survives.
func TestPruneMetricsPausesUntilAggregationIsComplete(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.Metric{}); err != nil {
		t.Fatal(err)
	}
	if err := PrepareMetricsRollupSchema(db); err != nil {
		t.Fatal(err)
	}
	repo := NewMetricRepo(db)
	ctx := context.Background()
	now := rollupTestNow()
	const server = uint(9)

	old := now.Add(-40 * 24 * time.Hour)
	recent := now.Add(-2 * time.Hour)
	for _, sample := range []model.Metric{
		{ServerID: server, Time: old, CPUPercent: f64(5), NetBytesRecv: i64(0)},
		{ServerID: server, Time: old.Add(time.Minute), CPUPercent: f64(6), NetBytesRecv: i64(60)},
		{ServerID: server, Time: recent, CPUPercent: f64(7), NetBytesRecv: i64(120)},
	} {
		if err := db.Create(&sample).Error; err != nil {
			t.Fatal(err)
		}
	}

	// No checkpoint yet: a dry run reports the prune but flags the server, and
	// nothing is deleted.
	plan, err := repo.PlanPrune(ctx, now, MetricsRetention)
	if err != nil {
		t.Fatalf("PlanPrune: %v", err)
	}
	if plan.RawRows != 2 {
		t.Fatalf("dry-run raw rows = %d, want the 2 expired samples", plan.RawRows)
	}
	if len(plan.Pending) != 1 || plan.Pending[0] != server {
		t.Fatalf("pending = %v, want the unaggregated server", plan.Pending)
	}
	result, err := repo.PruneMetrics(ctx, plan)
	if err != nil {
		t.Fatalf("PruneMetrics: %v", err)
	}
	if !result.Paused || result.RawRows != 0 {
		t.Fatalf("prune result = %+v, want a paused prune with no deletions", result)
	}
	var remaining int64
	if err := db.Raw(`SELECT COUNT(*) FROM metrics WHERE time < ?`, plan.Cutoff).Scan(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("expired rows before aggregation = %d, want them preserved", remaining)
	}

	// Once the aggregation checkpoints past the cutoff, retention proceeds.
	if _, err := repo.RefreshRollups(ctx, now, RollupBackfillWindow); err != nil {
		t.Fatalf("RefreshRollups: %v", err)
	}
	plan, err = repo.PlanPrune(ctx, now, MetricsRetention)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Pending) != 0 {
		t.Fatalf("pending = %v after a confirmed checkpoint, want none", plan.Pending)
	}
	result, err = repo.PruneMetrics(ctx, plan)
	if err != nil {
		t.Fatalf("PruneMetrics: %v", err)
	}
	if result.RawRows != 2 {
		t.Fatalf("deleted raw rows = %d, want the 2 expired samples", result.RawRows)
	}
	var keptRecent, keptRollup int64
	if err := db.Raw(`SELECT COUNT(*) FROM metrics WHERE server_id = ?`, server).Scan(&keptRecent).Error; err != nil {
		t.Fatal(err)
	}
	if keptRecent != 1 {
		t.Fatalf("recent raw rows = %d, want the retained sample", keptRecent)
	}
	if err := db.Raw(`SELECT COUNT(*) FROM metrics_rollup_6h WHERE server_id = ? AND bucket_start >= ?`,
		server, plan.Cutoff).Scan(&keptRollup).Error; err != nil {
		t.Fatal(err)
	}
	if keptRollup == 0 {
		t.Fatal("rolled-up history inside the retention window must survive pruning")
	}
}
