package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// metricsRollupMigrationID is the explicit migration for the rollup tables.
const metricsRollupMigrationID = "2026-10-13-metrics-rollup-6h"

// PrepareMetricsRollupSchema creates the self-maintained 6h rollup table and its
// materialization checkpoints. Creation and the migration marker commit
// atomically and the statement is idempotent.
func PrepareMetricsRollupSchema(db *gorm.DB) error {
	_, err := ApplyOnce(db, metricsRollupMigrationID, `
		CREATE TABLE IF NOT EXISTS metrics_rollup_6h (
			server_id bigint NOT NULL,
			bucket_start timestamptz NOT NULL,
			samples integer NOT NULL DEFAULT 0,
			first_sample timestamptz,
			last_sample timestamptz,
			cpu_sum double precision, cpu_count integer NOT NULL DEFAULT 0, cpu_max double precision,
			mem_sum double precision, mem_count integer NOT NULL DEFAULT 0, mem_max double precision,
			disk_sum double precision, disk_count integer NOT NULL DEFAULT 0, disk_max double precision,
			swap_sum double precision, swap_count integer NOT NULL DEFAULT 0, swap_max double precision,
			load1_sum double precision, load1_count integer NOT NULL DEFAULT 0, load1_max double precision,
			load5_sum double precision, load5_count integer NOT NULL DEFAULT 0, load5_max double precision,
			load15_sum double precision, load15_count integer NOT NULL DEFAULT 0, load15_max double precision,
			net_recv_rate double precision, net_recv_peak double precision, net_recv_coverage double precision,
			net_sent_rate double precision, net_sent_peak double precision, net_sent_coverage double precision,
			disk_read_rate double precision, disk_read_peak double precision, disk_read_coverage double precision,
			disk_write_rate double precision, disk_write_peak double precision, disk_write_coverage double precision,
			PRIMARY KEY (server_id, bucket_start)
		);
		CREATE TABLE IF NOT EXISTS metrics_rollup_checkpoints (
			server_id bigint PRIMARY KEY,
			completed_through timestamptz NOT NULL,
			updated_at timestamptz NOT NULL DEFAULT now()
		);
	`)
	return err
}

// BucketStart aligns a timestamp to the 6h UTC/epoch boundary.
func BucketStart(t time.Time) time.Time {
	const bucket = int64(RollupBucket / time.Second)
	seconds := t.UTC().Unix()
	return time.Unix(seconds-seconds%bucket, 0).UTC()
}

// RollupRefreshResult summarises one refresh cycle.
type RollupRefreshResult struct {
	Buckets    int
	Servers    int
	Checkpoint time.Time
}

// RefreshRollups materializes every closed 6h bucket up to the last closed
// boundary and advances the per-server checkpoint in the same transaction. It is
// idempotent: recomputing a bucket overwrites it, and a failure never advances
// the checkpoint. window bounds how far back a server without a checkpoint is
// backfilled; concurrent runs serialize on an advisory lock.
func (r *MetricRepo) RefreshRollups(ctx context.Context, now time.Time, window time.Duration) (RollupRefreshResult, error) {
	result := RollupRefreshResult{}
	if window <= 0 || window > RollupBackfillWindow {
		window = RollupRefreshWindow
	}
	closed := BucketStart(now) // buckets strictly before this are closed
	if closed.IsZero() {
		return result, nil
	}
	result.Checkpoint = closed

	servers, err := r.serversNeedingRollup(ctx, closed.Add(-window), closed)
	if err != nil {
		return result, err
	}
	for _, serverID := range servers {
		from, err := r.rollupStart(ctx, serverID, closed, now, window)
		if err != nil {
			return result, err
		}
		if !from.Before(closed) {
			continue
		}
		var buckets int
		err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// One refresher at a time; a losing cycle simply waits and then
			// recomputes the same buckets.
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "talus_metrics_rollup").Error; err != nil {
				return fmt.Errorf("lock rollup refresh: %w", err)
			}
			var err error
			buckets, err = materializeBuckets(tx, serverID, from, closed)
			if err != nil {
				return err
			}
			if err := tx.Exec(`
				INSERT INTO metrics_rollup_checkpoints (server_id, completed_through, updated_at)
				VALUES (?, ?, now())
				ON CONFLICT (server_id) DO UPDATE
					SET completed_through = EXCLUDED.completed_through, updated_at = EXCLUDED.updated_at`,
				serverID, closed).Error; err != nil {
				return fmt.Errorf("advance rollup checkpoint: %w", err)
			}
			return nil
		})
		if err != nil {
			return result, err
		}
		result.Servers++
		result.Buckets += buckets
	}
	return result, nil
}

// rollupStart is the first bucket a refresh must (re)compute for a server: the
// checkpoint when it exists, otherwise the window start. After a failure the
// checkpoint still points at the last confirmed bucket, so the gap is redone.
func (r *MetricRepo) rollupStart(ctx context.Context, serverID uint, closed, now time.Time, window time.Duration) (time.Time, error) {
	var checkpoint time.Time
	err := r.db.WithContext(ctx).Raw(
		`SELECT completed_through FROM metrics_rollup_checkpoints WHERE server_id = ?`, serverID).Scan(&checkpoint).Error
	if err != nil {
		return time.Time{}, err
	}
	// Never recompute below the retention floor: once raw data has been pruned an
	// empty scan must not overwrite materialized history with zeros.
	retentionFloor := BucketStart(now.Add(-MetricsRetention))
	start := BucketStart(now.Add(-window))
	if start.Before(retentionFloor) {
		start = retentionFloor
	}
	if !checkpoint.IsZero() {
		// Resume from the oldest unconfirmed bucket (a failure gap may be older
		// than the normal window) while still recomputing the whole recent window,
		// so late or backfilled samples are folded into already closed buckets.
		if resumed := BucketStart(checkpoint); resumed.Before(start) {
			start = resumed
			if start.Before(retentionFloor) {
				start = retentionFloor
			}
		}
	}
	return start, nil
}

// materializeBuckets recomputes the closed buckets in [from,to) for one server,
// including buckets with no samples, which are recorded explicitly so the
// checkpoint's completeness claim is verifiable.
func materializeBuckets(tx *gorm.DB, serverID uint, from, to time.Time) (int, error) {
	sql := deltaSamplesCTE("") + "," + `
	aggregate AS (` + rollupAggregateSelect(rollupBucketExpr) + `
	),
	buckets AS (
		SELECT generate_series($5::timestamptz, $6::timestamptz - interval '6 hours', interval '6 hours') AS bucket_start
	)
	INSERT INTO metrics_rollup_6h (
		server_id, bucket_start, samples, first_sample, last_sample,
		cpu_sum, cpu_count, cpu_max, mem_sum, mem_count, mem_max,
		disk_sum, disk_count, disk_max, swap_sum, swap_count, swap_max,
		load1_sum, load1_count, load1_max, load5_sum, load5_count, load5_max,
		load15_sum, load15_count, load15_max,
		net_recv_rate, net_recv_peak, net_recv_coverage,
		net_sent_rate, net_sent_peak, net_sent_coverage,
		disk_read_rate, disk_read_peak, disk_read_coverage,
		disk_write_rate, disk_write_peak, disk_write_coverage)
	SELECT $7, b.bucket_start, COALESCE(a.samples, 0), a.first_sample, a.last_sample,
		a.cpu_sum, COALESCE(a.cpu_count, 0), a.cpu_max, a.mem_sum, COALESCE(a.mem_count, 0), a.mem_max,
		a.disk_sum, COALESCE(a.disk_count, 0), a.disk_max, a.swap_sum, COALESCE(a.swap_count, 0), a.swap_max,
		a.load1_sum, COALESCE(a.load1_count, 0), a.load1_max, a.load5_sum, COALESCE(a.load5_count, 0), a.load5_max,
		a.load15_sum, COALESCE(a.load15_count, 0), a.load15_max,
		a.net_recv_rate, a.net_recv_peak, a.net_recv_coverage,
		a.net_sent_rate, a.net_sent_peak, a.net_sent_coverage,
		a.disk_read_rate, a.disk_read_peak, a.disk_read_coverage,
		a.disk_write_rate, a.disk_write_peak, a.disk_write_coverage
	FROM buckets b
	LEFT JOIN aggregate a ON a.bucket_start = b.bucket_start
	ON CONFLICT (server_id, bucket_start) DO UPDATE SET
		samples = EXCLUDED.samples, first_sample = EXCLUDED.first_sample, last_sample = EXCLUDED.last_sample,
		cpu_sum = EXCLUDED.cpu_sum, cpu_count = EXCLUDED.cpu_count, cpu_max = EXCLUDED.cpu_max,
		mem_sum = EXCLUDED.mem_sum, mem_count = EXCLUDED.mem_count, mem_max = EXCLUDED.mem_max,
		disk_sum = EXCLUDED.disk_sum, disk_count = EXCLUDED.disk_count, disk_max = EXCLUDED.disk_max,
		swap_sum = EXCLUDED.swap_sum, swap_count = EXCLUDED.swap_count, swap_max = EXCLUDED.swap_max,
		load1_sum = EXCLUDED.load1_sum, load1_count = EXCLUDED.load1_count, load1_max = EXCLUDED.load1_max,
		load5_sum = EXCLUDED.load5_sum, load5_count = EXCLUDED.load5_count, load5_max = EXCLUDED.load5_max,
		load15_sum = EXCLUDED.load15_sum, load15_count = EXCLUDED.load15_count, load15_max = EXCLUDED.load15_max,
		net_recv_rate = EXCLUDED.net_recv_rate, net_recv_peak = EXCLUDED.net_recv_peak, net_recv_coverage = EXCLUDED.net_recv_coverage,
		net_sent_rate = EXCLUDED.net_sent_rate, net_sent_peak = EXCLUDED.net_sent_peak, net_sent_coverage = EXCLUDED.net_sent_coverage,
		disk_read_rate = EXCLUDED.disk_read_rate, disk_read_peak = EXCLUDED.disk_read_peak, disk_read_coverage = EXCLUDED.disk_read_coverage,
		disk_write_rate = EXCLUDED.disk_write_rate, disk_write_peak = EXCLUDED.disk_write_peak, disk_write_coverage = EXCLUDED.disk_write_coverage
	WHERE NOT (EXCLUDED.samples = 0 AND metrics_rollup_6h.samples > 0)`

	// The scan starts one gap threshold early so the first bucket's first sample
	// still has a predecessor, and never reaches the open bucket.
	err := tx.Exec(sql,
		serverID, from.Add(-DefaultDeltaGapThreshold), to,
		DefaultDeltaGapThreshold.String(), from, to, serverID).Error
	if err != nil {
		return 0, fmt.Errorf("materialize rollup buckets: %w", err)
	}
	var written int64
	if err := tx.Raw(`SELECT COUNT(*) FROM metrics_rollup_6h WHERE server_id = ? AND bucket_start >= ? AND bucket_start < ?`,
		serverID, from, to).Scan(&written).Error; err != nil {
		return 0, err
	}
	return int(written), nil
}

// serversNeedingRollup selects the nodes a refresh cycle must serve: those with
// recent samples, those whose checkpoint is behind (a failure gap or a quiet
// node), and those that hold retained samples but were never materialized at
// all. Selecting only "recently active" nodes would leave a backlog unaggregated
// forever, which later blocks retention (REQ §8.3.1).
func (r *MetricRepo) serversNeedingRollup(ctx context.Context, since, closed time.Time) ([]uint, error) {
	var ids []uint
	err := r.db.WithContext(ctx).Raw(`
		SELECT server_id FROM metrics WHERE time >= $1
		UNION
		SELECT server_id FROM metrics_rollup_checkpoints WHERE completed_through < $2
		UNION
		SELECT DISTINCT m.server_id FROM metrics m
			LEFT JOIN metrics_rollup_checkpoints c ON c.server_id = m.server_id
			WHERE c.server_id IS NULL AND m.time >= $3
		ORDER BY server_id`,
		since, closed, closed.Add(-window_or_default(RollupBackfillWindow))).Scan(&ids).Error
	return ids, err
}

// window_or_default keeps the helper usable from a single expression.
func window_or_default(window time.Duration) time.Duration {
	if window <= 0 || window > RollupBackfillWindow {
		return RollupBackfillWindow
	}
	return window
}

// PrunePlan is the dry-run result for retention: the cutoff and the rows that
// would be removed.
type PrunePlan struct {
	Cutoff     time.Time
	RawRows    int64
	RollupRows int64
	// Pending lists servers whose aggregation is not yet confirmed through the
	// cutoff. Their data is never deleted until they catch up.
	Pending []uint
}

// PruneResult reports the applied deletions.
type PruneResult struct {
	RawRows        int64
	RollupRows     int64
	Paused         bool
	SkippedServers []uint
}

// PlanPrune computes the retention cutoff and what a prune would remove. It
// never writes: the caller shows it (dry run) before PruneMetrics acts.
func (r *MetricRepo) PlanPrune(ctx context.Context, now time.Time, retention time.Duration) (PrunePlan, error) {
	if retention <= 0 {
		retention = MetricsRetention
	}
	plan := PrunePlan{Cutoff: now.UTC().Add(-retention)}
	if err := r.db.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM metrics WHERE time < ?`, plan.Cutoff).Scan(&plan.RawRows).Error; err != nil {
		return plan, fmt.Errorf("count prunable raw rows: %w", err)
	}
	if err := r.db.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM metrics_rollup_6h WHERE bucket_start < ?`, plan.Cutoff).Scan(&plan.RollupRows).Error; err != nil {
		return plan, fmt.Errorf("count prunable rollup rows: %w", err)
	}
	// Aggregation completeness per server: a server with raw data older than the
	// cutoff but no checkpoint covering it must not lose that raw data yet.
	var rows []struct {
		ServerID  uint
		CoveredTo *time.Time
	}
	if err := r.db.WithContext(ctx).Raw(`
		SELECT m.server_id, c.completed_through AS covered_to
		FROM (SELECT DISTINCT server_id FROM metrics WHERE time < ?) m
		LEFT JOIN metrics_rollup_checkpoints c ON c.server_id = m.server_id`,
		plan.Cutoff).Scan(&rows).Error; err != nil {
		return plan, fmt.Errorf("check aggregation completeness: %w", err)
	}
	for _, row := range rows {
		if row.CoveredTo == nil || row.CoveredTo.Before(plan.Cutoff) {
			plan.Pending = append(plan.Pending, row.ServerID)
		}
	}
	return plan, nil
}

// PruneMetrics applies a dry-run plan: it deletes rows older than the cutoff for
// servers whose aggregation is confirmed, and pauses (reporting the skipped
// servers) for the rest instead of deleting data that was never aggregated.
func (r *MetricRepo) PruneMetrics(ctx context.Context, plan PrunePlan) (PruneResult, error) {
	result := PruneResult{SkippedServers: plan.Pending}
	if len(plan.Pending) > 0 {
		result.Paused = true
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "talus_metrics_retention").Error; err != nil {
			return fmt.Errorf("lock retention: %w", err)
		}
		exclusion, excludedArgs := serverExclusion(plan.Pending)
		raw := tx.Exec(`DELETE FROM metrics WHERE time < ?`+exclusion,
			append([]any{plan.Cutoff}, excludedArgs...)...)
		if raw.Error != nil {
			return fmt.Errorf("prune raw metrics: %w", raw.Error)
		}
		result.RawRows = raw.RowsAffected

		rollup := tx.Exec(`DELETE FROM metrics_rollup_6h WHERE bucket_start < ?`+exclusion,
			append([]any{plan.Cutoff}, excludedArgs...)...)
		if rollup.Error != nil {
			return fmt.Errorf("prune rolled-up metrics: %w", rollup.Error)
		}
		result.RollupRows = rollup.RowsAffected
		return nil
	})
	if err != nil {
		return PruneResult{}, err
	}
	return result, nil
}

// serverExclusion builds an explicit "AND server_id NOT IN (...)" clause: GORM
// expands slice arguments into individual placeholders, so an array parameter
// cannot be used here.
func serverExclusion(ids []uint) (string, []any) {
	if len(ids) == 0 {
		return "", nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return " AND server_id NOT IN (" + placeholders + ")", args
}

// RollupRowFor reads one materialized bucket (used by the stitched query and by
// tests that verify retention and recomputation).
func (r *MetricRepo) RollupRowFor(ctx context.Context, serverID uint, bucketStart time.Time) (*RollupRow, error) {
	var row RollupRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT * FROM metrics_rollup_6h WHERE server_id = ? AND bucket_start = ?`, serverID, bucketStart).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.BucketStart.IsZero() {
		return nil, nil
	}
	return &row, nil
}
