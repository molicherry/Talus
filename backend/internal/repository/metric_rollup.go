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
			buckets, err = materializeBuckets(tx, r, serverID, from, closed)
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
func materializeBuckets(tx *gorm.DB, repo *MetricRepo, serverID uint, from, to time.Time) (int, error) {
	sql := deltaSamplesCTE(intervalSeconds["6h"], "", "$5", "$6") + "," + `
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
	threshold := DefaultDeltaGapThreshold
	if repo != nil {
		threshold = repo.gapThreshold()
	}
	err := tx.Exec(sql,
		serverID, from.Add(-threshold), to,
		threshold.String(), from, to, serverID).Error
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
	ChunksDropped  int
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
	// Aggregation completeness per server: only raw data that the checkpoint has
	// not covered is protected. A server that stopped reporting (offline or
	// deleted) must not block its own retention forever just because its
	// checkpoint no longer follows wall-clock time (R09).
	var rows []struct {
		ServerID        uint
		CoveredTo       *time.Time
		HasUnaggregated bool
	}
	if err := r.db.WithContext(ctx).Raw(`
		SELECT m.server_id, c.completed_through AS covered_to,
		       EXISTS (
		         SELECT 1 FROM metrics raw
		         WHERE raw.server_id = m.server_id
		           AND raw.time < $1
		           AND (c.completed_through IS NULL OR raw.time >= c.completed_through)
		       ) AS has_unaggregated
		FROM (SELECT DISTINCT server_id FROM metrics WHERE time < $1) m
		LEFT JOIN metrics_rollup_checkpoints c ON c.server_id = m.server_id`,
		plan.Cutoff).Scan(&rows).Error; err != nil {
		return plan, fmt.Errorf("check aggregation completeness: %w", err)
	}
	for _, row := range rows {
		if row.HasUnaggregated {
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
		// Complete chunks are dropped whole, which is what actually releases disk
		// space; row deletes only mark space reusable inside a chunk. A chunk is
		// only dropped when the aggregation that covers its range is confirmed.
		dropped, err := dropPrunableChunks(tx, plan)
		if err != nil {
			return err
		}
		result.ChunksDropped = dropped
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

// chunkRange is one hypertable chunk candidate.
type chunkRange struct {
	Name       string
	RangeStart time.Time
	RangeEnd   time.Time
}

// dropPrunableChunks drops the longest prefix of complete chunks below the cutoff
// whose range holds no raw data for a server whose aggregation is still pending.
// It uses the official range API (drop_chunks only accepts a hypertable), so the
// completeness guard and the physical reclamation stay consistent. Tables that are
// not hypertables report no chunks and the caller falls back to row deletes.
func dropPrunableChunks(tx *gorm.DB, plan PrunePlan) (int, error) {
	var chunks []chunkRange
	err := tx.Raw(`
		SELECT chunk_name AS name, range_start, range_end
		FROM timescaledb_information.chunks
		WHERE hypertable_name = 'metrics' AND range_end <= ?
		ORDER BY range_start`, plan.Cutoff).Scan(&chunks).Error
	if err != nil {
		// A plain (non-TimescaleDB) database has no chunk catalogue.
		return 0, nil
	}

	var boundary time.Time
	droppable := 0
	for _, chunk := range chunks {
		held, err := chunkHoldsPendingRaw(tx, chunk, plan.Pending)
		if err != nil {
			return droppable, err
		}
		if held {
			// The prefix must stay contiguous: a later chunk is never dropped while
			// an older one still holds unaggregated data.
			break
		}
		boundary = chunk.RangeEnd
		droppable++
	}
	if droppable == 0 {
		return 0, nil
	}
	if err := tx.Exec(`SELECT drop_chunks('metrics', older_than => ?::timestamptz)`, boundary).Error; err != nil {
		return 0, fmt.Errorf("drop chunks older than %v: %w", boundary, err)
	}
	return droppable, nil
}

// chunkHoldsPendingRaw reports whether a chunk contains raw data for a server
// whose aggregation has not been confirmed yet.
func chunkHoldsPendingRaw(tx *gorm.DB, chunk chunkRange, pending []uint) (bool, error) {
	if len(pending) == 0 {
		return false, nil
	}
	exclusion, args := serverExclusion(pending)
	query := `SELECT EXISTS (SELECT 1 FROM metrics WHERE time >= ? AND time < ? AND server_id IN (` +
		strings.TrimPrefix(exclusion, " AND server_id NOT IN (") + `)`
	all := append([]any{chunk.RangeStart, chunk.RangeEnd}, args...)
	var held bool
	if err := tx.Raw(query, all...).Scan(&held).Error; err != nil {
		return false, fmt.Errorf("check chunk %s completeness: %w", chunk.Name, err)
	}
	return held, nil
}

// StorageReport is the measured on-disk footprint of the metrics storage plus the
// linear projection to the retention window (REQUIREMENTS §8.3.2).
type StorageReport struct {
	RawRows                 int64
	RawTotalBytes           int64
	RawTableBytes           int64
	RawIndexBytes           int64
	RawChunkBytes           int64
	RollupRows              int64
	RollupTotalBytes        int64
	OldestRaw               *time.Time
	NewestRaw               *time.Time
	MeasuredWindow          time.Duration
	ProjectedRetentionBytes int64
}

// StorageReport measures the storage instead of assuming a row width.
func (r *MetricRepo) StorageReport(ctx context.Context) (*StorageReport, error) {
	report := &StorageReport{}

	var raw struct {
		Rows       int64  `gorm:"column:raw_rows"`
		TotalBytes int64  `gorm:"column:raw_total_bytes"`
		TableBytes int64  `gorm:"column:raw_table_bytes"`
		IndexBytes int64  `gorm:"column:raw_index_bytes"`
		ChunkBytes *int64 `gorm:"column:raw_chunk_bytes"`
	}
	// hypertable_detailed_size only exists on TimescaleDB: fall back to the plain
	// relation sizes when the metrics table is not a hypertable.
	err := r.db.WithContext(ctx).Raw(`
		SELECT
			(SELECT COUNT(*) FROM metrics) AS raw_rows,
			(SELECT pg_total_relation_size('metrics')) AS raw_total_bytes,
			(SELECT pg_relation_size('metrics')) AS raw_table_bytes,
			(SELECT pg_indexes_size('metrics')) AS raw_index_bytes,
			(SELECT COALESCE(SUM(total_bytes), 0) FROM hypertable_detailed_size('metrics')) AS raw_chunk_bytes`,
	).Scan(&raw).Error
	if err != nil {
		err = r.db.WithContext(ctx).Raw(`
			SELECT
				(SELECT COUNT(*) FROM metrics) AS raw_rows,
				(SELECT pg_total_relation_size('metrics')) AS raw_total_bytes,
				(SELECT pg_relation_size('metrics')) AS raw_table_bytes,
				(SELECT pg_indexes_size('metrics')) AS raw_index_bytes`).Scan(&raw).Error
		if err != nil {
			return nil, fmt.Errorf("storage report: %w", err)
		}
	}
	report.RawRows = raw.Rows
	report.RawTableBytes = raw.TableBytes
	report.RawIndexBytes = raw.IndexBytes
	if raw.ChunkBytes != nil {
		// On TimescaleDB the hypertable's own relation size covers only the parent
		// shell; the chunks hold the data. hypertable_detailed_size already reports
		// the parent and every chunk, so it is the total — using the parent size
		// alone understated capacity by orders of magnitude (F09).
		report.RawChunkBytes = *raw.ChunkBytes
		report.RawTotalBytes = *raw.ChunkBytes
	} else {
		report.RawTotalBytes = raw.TotalBytes
	}

	var rollup struct {
		Rows       int64      `gorm:"column:rollup_rows"`
		TotalBytes int64      `gorm:"column:rollup_total_bytes"`
		OldestRaw  *time.Time `gorm:"column:oldest_raw"`
		NewestRaw  *time.Time `gorm:"column:newest_raw"`
	}
	if err := r.db.WithContext(ctx).Raw(`
		SELECT
			(SELECT COUNT(*) FROM metrics_rollup_6h) AS rollup_rows,
			(SELECT pg_total_relation_size('metrics_rollup_6h')) AS rollup_total_bytes,
			(SELECT MIN(time) FROM metrics) AS oldest_raw,
			(SELECT MAX(time) FROM metrics) AS newest_raw`).Scan(&rollup).Error; err != nil {
		return nil, fmt.Errorf("storage report: %w", err)
	}
	report.RollupRows = rollup.Rows
	report.RollupTotalBytes = rollup.TotalBytes
	report.OldestRaw = rollup.OldestRaw
	report.NewestRaw = rollup.NewestRaw

	if report.OldestRaw != nil && report.NewestRaw != nil {
		report.MeasuredWindow = report.NewestRaw.Sub(*report.OldestRaw)
		report.ProjectedRetentionBytes = ProjectStorage(report.RawTotalBytes, report.MeasuredWindow, MetricsRetention)
	}
	return report, nil
}

// ProjectStorage linearly projects a measured footprint onto a longer window. A
// measurement window shorter than an hour is not extrapolated (it is dominated by
// freshly written pages, not by steady-state row width).
func ProjectStorage(measuredBytes int64, measuredWindow, targetWindow time.Duration) int64 {
	if measuredBytes <= 0 || measuredWindow < time.Hour || targetWindow <= 0 {
		return measuredBytes
	}
	scaled := float64(measuredBytes) * (float64(targetWindow) / float64(measuredWindow))
	return int64(scaled)
}
