package repository

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// bucketExpr builds the UTC/epoch-aligned bucket expression for a duration in
// seconds. Epoch alignment is used instead of time_bucket() so the same SQL runs
// on plain PostgreSQL test databases and on TimescaleDB.
func bucketExpr(seconds int64) string {
	return fmt.Sprintf(
		"to_timestamp(floor(EXTRACT(EPOCH FROM time) / %[1]d::float8) * %[1]d::float8)", seconds)
}

// rawBucketAggregateSQL aggregates the delta-aware sample CTE into buckets,
// returning both the average and the observed maximum of every series plus the
// valid coverage seconds of the rate columns.
func rawBucketAggregateSQL(intervalSeconds int64) string {
	ratePeak := func(counter string) string { return fmt.Sprintf("MAX(%s_rate)", counter) }
	rateCov := func(counter string) string { return fmt.Sprintf("SUM(%s_weight)", counter) }
	percent := func(used, total string) string {
		return fmt.Sprintf(`CASE WHEN SUM(CASE WHEN %[2]s > 0 THEN 1 ELSE 0 END) > 0
				THEN SUM(CASE WHEN %[2]s > 0 THEN %[1]s * 100.0 / %[2]s ELSE 0 END)
				     / SUM(CASE WHEN %[2]s > 0 THEN 1 ELSE 0 END)
				ELSE NULL END`, used, total)
	}
	percentPeak := func(used, total string) string {
		return fmt.Sprintf("MAX(CASE WHEN %[2]s > 0 THEN %[1]s * 100.0 / %[2]s END)", used, total)
	}
	return deltaSamplesCTE("") + fmt.Sprintf(`,
	aggregate AS (
		SELECT
			%s AS bucket,
			AVG(cpu_percent) AS cpu_percent, MAX(cpu_percent) AS cpu_peak,
			%s AS memory_percent, %s AS memory_peak,
			%s AS disk_percent, %s AS disk_peak,
			AVG(load_1) AS load_1, MAX(load_1) AS load_1_peak,
			AVG(load_5) AS load_5, MAX(load_5) AS load_5_peak,
			AVG(load_15) AS load_15, MAX(load_15) AS load_15_peak,
			%s AS swap_percent, %s AS swap_peak,
			%s AS net_recv_rate, %s AS net_recv_peak, %s AS net_recv_coverage,
			%s AS net_sent_rate, %s AS net_sent_peak, %s AS net_sent_coverage,
			%s AS disk_read_rate, %s AS disk_read_peak, %s AS disk_read_coverage,
			%s AS disk_write_rate, %s AS disk_write_peak, %s AS disk_write_coverage
		FROM samples
		WHERE time >= $5 AND time < $6
		GROUP BY bucket
	)
	SELECT bucket, cpu_percent, cpu_peak, memory_percent, memory_peak, disk_percent, disk_peak,
	       load_1, load_1_peak, load_5, load_5_peak, load_15, load_15_peak, swap_percent, swap_peak,
	       net_recv_rate, net_recv_peak, net_recv_coverage,
	       net_sent_rate, net_sent_peak, net_sent_coverage,
	       disk_read_rate, disk_read_peak, disk_read_coverage,
	       disk_write_rate, disk_write_peak, disk_write_coverage
	FROM aggregate
	ORDER BY bucket ASC`,
		bucketExpr(intervalSeconds),
		percent("memory_used", "memory_total"), percentPeak("memory_used", "memory_total"),
		percent("disk_used", "disk_total"), percentPeak("disk_used", "disk_total"),
		percent("swap_used", "swap_total"), percentPeak("swap_used", "swap_total"),
		metricRateExpression("net_bytes_recv"), ratePeak("net_bytes_recv"), rateCov("net_bytes_recv"),
		metricRateExpression("net_bytes_sent"), ratePeak("net_bytes_sent"), rateCov("net_bytes_sent"),
		metricRateExpression("disk_read_bytes"), ratePeak("disk_read_bytes"), rateCov("disk_read_bytes"),
		metricRateExpression("disk_write_bytes"), ratePeak("disk_write_bytes"), rateCov("disk_write_bytes"))
}

// queryRawBuckets aggregates bounded raw samples at the requested granularity.
func (r *MetricRepo) queryRawBuckets(ctx context.Context, serverID uint, from, to time.Time, interval string) ([]AggregatedMetric, error) {
	sql := rawBucketAggregateSQL(intervalSeconds[interval])
	var points []AggregatedMetric
	// The scan starts one gap threshold early so the first in-window sample still
	// has a predecessor, but only [from,to) is aggregated: the predecessor is
	// rate context and must not contribute gauge observations or extra points.
	err := r.db.WithContext(ctx).Raw(sql,
		serverID, from.Add(-DefaultDeltaGapThreshold), to,
		DefaultDeltaGapThreshold.String(), from, to).Scan(&points).Error
	if err != nil {
		return nil, classifyBudgetError(fmt.Errorf("raw metrics query: %w", err))
	}
	return points, nil
}

// queryRollupWithEdges assembles the coarsest plan: complete, materialized and
// checkpointed 6h buckets come from the rollup table, while the two edge buckets,
// unclosed buckets and buckets that were never materialized are recomputed from a
// bounded raw read — the source ranges are mutually exclusive, so no sample is
// counted twice or dropped.
func (r *MetricRepo) queryRollupWithEdges(ctx context.Context, serverID uint, from, to time.Time) ([]AggregatedMetric, bool, error) {
	bucketSeconds := intervalSeconds["6h"]
	start := BucketStart(from)
	closed := BucketStart(time.Now().UTC())

	// A bucket may only be served from the rollup when it is fully inside the
	// requested range, closed, AND the per-server checkpoint confirms it was
	// materialized. Anything else falls back to a bounded raw read, so a missing
	// or unconfirmed rollup can never silently drop samples.
	confirmed, err := r.confirmedBuckets(ctx, serverID, start, to)
	if err != nil {
		return nil, false, err
	}
	// The checkpoint claims a range is materialized; the rows must back that
	// claim. A confirmed bucket without a row is read from raw instead of being
	// reported as an empty hole.
	var rows []RollupRow
	if err := r.db.WithContext(ctx).Raw(
		`SELECT * FROM metrics_rollup_6h WHERE server_id = ? AND bucket_start >= ? AND bucket_start < ? ORDER BY bucket_start`,
		serverID, start, to).Scan(&rows).Error; err != nil {
		return nil, false, classifyBudgetError(fmt.Errorf("rollup metrics query: %w", err))
	}
	materialized := make(map[time.Time]bool, len(rows))
	for _, row := range rows {
		materialized[row.BucketStart.UTC()] = true
	}
	type span struct{ from, to time.Time }
	var rawSpans []span
	rollupBuckets := map[time.Time]bool{}
	for cursor := start; cursor.Before(to); cursor = cursor.Add(RollupBucket) {
		bucketEnd := cursor.Add(RollupBucket)
		rangeOK := !cursor.Before(from) && !bucketEnd.After(to) && !bucketEnd.After(closed)
		if rangeOK && confirmed[cursor] && materialized[cursor] {
			rollupBuckets[cursor] = true
			continue
		}
		// A partial edge bucket is only observed inside the requested window, so
		// statistics never include samples before from or after to.
		clippedFrom, clippedTo := cursor, bucketEnd
		if clippedFrom.Before(from) {
			clippedFrom = from
		}
		if clippedTo.After(to) {
			clippedTo = to
		}
		if !clippedFrom.Before(clippedTo) {
			continue
		}
		if len(rawSpans) > 0 && rawSpans[len(rawSpans)-1].to.Equal(clippedFrom) {
			rawSpans[len(rawSpans)-1].to = clippedTo
			continue
		}
		rawSpans = append(rawSpans, span{from: clippedFrom, to: clippedTo})
	}

	var points []AggregatedMetric
	usedRollup := false
	if len(rollupBuckets) > 0 {
		for _, row := range rows {
			if !rollupBuckets[row.BucketStart.UTC()] {
				continue
			}
			points = append(points, rollupRowToPoint(row, bucketSeconds))
			usedRollup = true
		}
	}

	// The budget applies to the plan's raw input as a whole, not per edge range:
	// several small bounded reads must still add up to at most 12,000 samples.
	totalRaw := 0
	for _, raw := range rawSpans {
		samples, overBudget, err := r.probeRawSamples(ctx, serverID, raw.from, raw.to)
		if err != nil {
			return nil, usedRollup, err
		}
		totalRaw += samples
		if overBudget || totalRaw > MaxRawSamples {
			// The coarsest granularity still needs too much raw input because the
			// rollup is not ready: report it instead of broadening the scan.
			return nil, usedRollup, &QueryBudgetError{Reason: ReasonRollupNotReady, Samples: totalRaw}
		}
		edge, err := r.queryRawBuckets(ctx, serverID, raw.from, raw.to, "6h")
		if err != nil {
			return nil, usedRollup, err
		}
		points = append(points, edge...)
	}

	sortPoints(points)
	return points, usedRollup, nil
}

// confirmedBuckets reports the buckets the per-server checkpoint proves were
// materialized, including explicitly confirmed empty ones.
func (r *MetricRepo) confirmedBuckets(ctx context.Context, serverID uint, from, to time.Time) (map[time.Time]bool, error) {
	var checkpoint time.Time
	err := r.db.WithContext(ctx).Raw(
		`SELECT completed_through FROM metrics_rollup_checkpoints WHERE server_id = ?`, serverID).Scan(&checkpoint).Error
	if err != nil {
		return nil, classifyBudgetError(fmt.Errorf("read rollup checkpoint: %w", err))
	}
	confirmed := map[time.Time]bool{}
	if checkpoint.IsZero() {
		return confirmed, nil
	}
	for cursor := BucketStart(from); cursor.Before(to); cursor = cursor.Add(RollupBucket) {
		if !cursor.Add(RollupBucket).After(checkpoint.UTC()) {
			confirmed[cursor] = true
		}
	}
	return confirmed, nil
}

// rollupRowToPoint turns a materialized bucket into the response point, keeping
// the average and the sampled maximum separate.
func rollupRowToPoint(row RollupRow, bucketSeconds int64) AggregatedMetric {
	avg := func(sum *float64, count int) *float64 {
		if sum == nil || count == 0 {
			return nil
		}
		value := *sum / float64(count)
		return &value
	}
	point := AggregatedMetric{
		Time:          row.BucketStart.UTC(),
		CPUPercent:    avg(row.CPUSum, row.CPUCount),
		CPUPeak:       row.CPUMax,
		MemoryPercent: avg(row.MemSum, row.MemCount),
		MemoryPeak:    row.MemMax,
		DiskPercent:   avg(row.DiskSum, row.DiskCount),
		DiskPeak:      row.DiskMax,
		SwapPercent:   avg(row.SwapSum, row.SwapCount),
		SwapPeak:      row.SwapMax,
		Load1:         avg(row.Load1Sum, row.Load1Count),
		Load1Peak:     row.Load1Max,
		Load5:         avg(row.Load5Sum, row.Load5Count),
		Load5Peak:     row.Load5Max,
		Load15:        avg(row.Load15Sum, row.Load15Count),
		Load15Peak:    row.Load15Max,
		NetRecvRate:   row.NetRecvRate,
		NetRecvPeak:   row.NetRecvPeak,
		NetRecvCov:    row.NetRecvCov,
		NetSentRate:   row.NetSentRate,
		NetSentPeak:   row.NetSentPeak,
		NetSentCov:    row.NetSentCov,
		DiskReadRate:  row.DiskReadRate,
		DiskReadPeak:  row.DiskReadPeak,
		DiskReadCov:   row.DiskReadCov,
		DiskWriteRate: row.DiskWrtRate,
		DiskWrtPeak:   row.DiskWrtPeak,
		DiskWrtCov:    row.DiskWrtCov,
	}
	_ = bucketSeconds
	return point
}

// classifyBudgetError maps a context expiry onto the documented timeout refusal.
func classifyBudgetError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &QueryBudgetError{Reason: ReasonQueryTimeout}
	}
	return err
}

func sortPoints(points []AggregatedMetric) {
	for i := 1; i < len(points); i++ {
		for j := i; j > 0 && points[j].Time.Before(points[j-1].Time); j-- {
			points[j], points[j-1] = points[j-1], points[j]
		}
	}
}
