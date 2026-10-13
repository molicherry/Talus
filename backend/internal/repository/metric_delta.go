package repository

import (
	"fmt"
	"time"
)

// Delta and rollup constants (REQUIREMENTS §8.3.1).
const (
	// DefaultDeltaGapThreshold is the largest spacing between two samples that
	// may contribute a rate: three times the 60s collection interval, so up to two
	// missed samples are tolerated and anything longer becomes a gap.
	DefaultDeltaGapThreshold = 3 * time.Minute

	// RollupBucket is the long-window aggregation bucket.
	RollupBucket = 6 * time.Hour

	// MetricsRetention is the logical retention for raw and rolled-up metrics.
	MetricsRetention = 35 * 24 * time.Hour

	// RollupRefreshWindow is the normal hourly recompute window.
	RollupRefreshWindow = 48 * time.Hour

	// RollupBackfillWindow is the first-deployment backfill (and the explicit
	// backfill range).
	RollupBackfillWindow = 35 * 24 * time.Hour
)

// rateCounters are the monotonic counters whose per-sample delta matters.
var rateCounters = []string{"net_bytes_recv", "net_bytes_sent", "disk_read_bytes", "disk_write_bytes"}

// deltaSamplesCTE builds the shared adjacent-sample CTE: one row per raw sample
// with, for every counter, an effective rate that is NULL when the pair must not
// participate (counter reset/decrease, zero or negative time delta, or a gap
// beyond the declared threshold). A real zero increment yields 0, not NULL.
//
// The caller must scan one predecessor sample before the requested start so the
// first bucket can be weighted correctly. $1=server id, $2=scan start (the
// window start minus the gap threshold), $3=scan end (exclusive), $4=gap
// threshold; the CTE also carries the observation window ($5=window start,
// $6=window end) so callers can exclude the predecessor rows from statistics:
// a predecessor is rate context, never an in-window observation.
// deltaSamplesCTE builds the shared adjacent-sample CTE: one row per raw sample
// with, for every counter, an effective rate that is NULL when the pair must not
// participate (counter reset/decrease, zero or negative time delta, or a gap
// beyond the declared threshold) plus the *clipped* weight of that pair.
//
// The weight is the overlap of the pair interval with three bounds: the bucket it
// is attributed to, the request window, and the sample times themselves. Weighting
// by the full pair interval let time outside the requested window inflate both the
// average and the reported coverage (F02). A real zero increment yields 0, not
// NULL.
//
// Parameters: $1=server id, $2=scan start, $3=scan end (exclusive), $4=gap
// threshold. Window bounds come from windowStartParam/windowEndParam, and the
// bucket grid either from a literal bucketSeconds or from bucketIntervalParam.
func deltaSamplesCTE(bucketSeconds int64, bucketIntervalParam, windowStartParam, windowEndParam string) string {
	rate := func(counter string) string {
		return fmt.Sprintf(`CASE
			WHEN prev_%[1]s IS NULL OR %[1]s IS NULL THEN NULL
			WHEN %[1]s < prev_%[1]s THEN NULL
			WHEN EXTRACT(EPOCH FROM (time - prev_time)) <= 0 THEN NULL
			WHEN time - prev_time > $4::interval THEN NULL
			ELSE (%[1]s - prev_%[1]s)::float8 / EXTRACT(EPOCH FROM (time - prev_time))
		END AS %[1]s_rate`, counter)
	}
	out := ""
	for i, counter := range rateCounters {
		if i > 0 {
			out += ",\n"
		}
		out += "\t\t" + rate(counter)
	}
	weighted := ""
	for _, counter := range rateCounters {
		weighted += fmt.Sprintf(`,
			CASE
				WHEN %[1]s_rate IS NOT NULL
				THEN GREATEST(0, EXTRACT(EPOCH FROM (
					LEAST(time, %[2]s::timestamptz)
					- GREATEST(prev_time, %[3]s::timestamptz)
				)))
				ELSE 0
			END AS %[1]s_weight`, counter, windowEndParam, windowStartParam)
	}
	_ = bucketSeconds
	_ = bucketIntervalParam
	return fmt.Sprintf(`
	WITH bounded AS (
		(SELECT time, memory_used, memory_total, disk_used, disk_total, swap_used, swap_total,
		        cpu_percent, load_1, load_5, load_15,
		        net_bytes_recv, net_bytes_sent, disk_read_bytes, disk_write_bytes
		 FROM metrics
		 WHERE server_id = $1 AND time >= $2 AND time < $3)
		UNION ALL
		-- Exactly one predecessor row: rate context for the first in-window sample.
		-- It is never aggregated (the window filter below excludes it), and it keeps
		-- the planned input bounded without widening the gap semantics.
		(SELECT time, memory_used, memory_total, disk_used, disk_total, swap_used, swap_total,
		        cpu_percent, load_1, load_5, load_15,
		        net_bytes_recv, net_bytes_sent, disk_read_bytes, disk_write_bytes
		 FROM metrics
		 WHERE server_id = $1 AND time < $2
		 ORDER BY time DESC
		 LIMIT 1)
	),
	lagged AS (
		SELECT *,
		       lag(time) OVER w AS prev_time,
		       lag(net_bytes_recv) OVER w AS prev_net_bytes_recv,
		       lag(net_bytes_sent) OVER w AS prev_net_bytes_sent,
		       lag(disk_read_bytes) OVER w AS prev_disk_read_bytes,
		       lag(disk_write_bytes) OVER w AS prev_disk_write_bytes
		FROM bounded
		WINDOW w AS (ORDER BY time)
	),
	rates AS (
		SELECT *,
%s
		FROM lagged
	),
	samples AS (
		SELECT *%s
		FROM rates
	)`, out, weighted)
}

// metricRateExpression yields the weighted-average expression for one counter's
// rate column: valid-coverage weighting, so a long valid pair is not outweighed
// by a short one and gaps contribute nothing.
func metricRateExpression(counter string) string {
	return fmt.Sprintf(`CASE WHEN SUM(%[1]s_weight) > 0
			THEN SUM(%[1]s_rate * %[1]s_weight) / SUM(%[1]s_weight)
			ELSE NULL END`, counter)
}

// metricPeakExpression keeps the sampled maximum of the effective rate.
func metricPeakExpression(counter string) string {
	return fmt.Sprintf("MAX(%s_rate)", counter)
}

// metricCoverageExpression reports the valid coverage seconds for a counter.
func metricCoverageExpression(counter string) string {
	return fmt.Sprintf("SUM(%s_weight)", counter)
}

// rollupAggregateSelect builds the bucket aggregation used by the 6h rollup job.
func rollupAggregateSelect(bucketExpr string) string {
	return fmt.Sprintf(`
	SELECT
		%s AS bucket_start,
		COUNT(*)::int AS samples,
		MIN(time) AS first_sample,
		MAX(time) AS last_sample,
		SUM(cpu_percent) AS cpu_sum, COUNT(cpu_percent)::int AS cpu_count, MAX(cpu_percent) AS cpu_max,
		SUM(CASE WHEN memory_total > 0 THEN memory_used * 100.0 / memory_total END) AS mem_sum,
		COUNT(CASE WHEN memory_total > 0 THEN 1 END)::int AS mem_count,
		MAX(CASE WHEN memory_total > 0 THEN memory_used * 100.0 / memory_total END) AS mem_max,
		SUM(CASE WHEN disk_total > 0 THEN disk_used * 100.0 / disk_total END) AS disk_sum,
		COUNT(CASE WHEN disk_total > 0 THEN 1 END)::int AS disk_count,
		MAX(CASE WHEN disk_total > 0 THEN disk_used * 100.0 / disk_total END) AS disk_max,
		SUM(CASE WHEN swap_total > 0 THEN swap_used * 100.0 / swap_total END) AS swap_sum,
		COUNT(CASE WHEN swap_total > 0 THEN 1 END)::int AS swap_count,
		MAX(CASE WHEN swap_total > 0 THEN swap_used * 100.0 / swap_total END) AS swap_max,
		SUM(load_1) AS load1_sum, COUNT(load_1)::int AS load1_count, MAX(load_1) AS load1_max,
		SUM(load_5) AS load5_sum, COUNT(load_5)::int AS load5_count, MAX(load_5) AS load5_max,
		SUM(load_15) AS load15_sum, COUNT(load_15)::int AS load15_count, MAX(load_15) AS load15_max,
		%s AS net_recv_rate, %s AS net_recv_peak, %s AS net_recv_coverage,
		%s AS net_sent_rate, %s AS net_sent_peak, %s AS net_sent_coverage,
		%s AS disk_read_rate, %s AS disk_read_peak, %s AS disk_read_coverage,
		%s AS disk_write_rate, %s AS disk_write_peak, %s AS disk_write_coverage
	FROM samples
	GROUP BY bucket_start`,
		bucketExpr,
		metricRateExpression("net_bytes_recv"), metricPeakExpression("net_bytes_recv"), metricCoverageExpression("net_bytes_recv"),
		metricRateExpression("net_bytes_sent"), metricPeakExpression("net_bytes_sent"), metricCoverageExpression("net_bytes_sent"),
		metricRateExpression("disk_read_bytes"), metricPeakExpression("disk_read_bytes"), metricCoverageExpression("disk_read_bytes"),
		metricRateExpression("disk_write_bytes"), metricPeakExpression("disk_write_bytes"), metricCoverageExpression("disk_write_bytes"))
}

// rollupBucketExpr aligns a timestamp to the 6h UTC boundary.
const rollupBucketExpr = "to_timestamp(floor(EXTRACT(EPOCH FROM time) / 21600) * 21600)"

// RollupRow is one materialized 6h bucket.
type RollupRow struct {
	ServerID     uint      `gorm:"column:server_id"`
	BucketStart  time.Time `gorm:"column:bucket_start"`
	Samples      int       `gorm:"column:samples"`
	FirstSample  time.Time `gorm:"column:first_sample"`
	LastSample   time.Time `gorm:"column:last_sample"`
	CPUSum       *float64  `gorm:"column:cpu_sum"`
	CPUCount     int       `gorm:"column:cpu_count"`
	CPUMax       *float64  `gorm:"column:cpu_max"`
	MemSum       *float64  `gorm:"column:mem_sum"`
	MemCount     int       `gorm:"column:mem_count"`
	MemMax       *float64  `gorm:"column:mem_max"`
	DiskSum      *float64  `gorm:"column:disk_sum"`
	DiskCount    int       `gorm:"column:disk_count"`
	DiskMax      *float64  `gorm:"column:disk_max"`
	SwapSum      *float64  `gorm:"column:swap_sum"`
	SwapCount    int       `gorm:"column:swap_count"`
	SwapMax      *float64  `gorm:"column:swap_max"`
	Load1Sum     *float64  `gorm:"column:load1_sum"`
	Load1Count   int       `gorm:"column:load1_count"`
	Load1Max     *float64  `gorm:"column:load1_max"`
	Load5Sum     *float64  `gorm:"column:load5_sum"`
	Load5Count   int       `gorm:"column:load5_count"`
	Load5Max     *float64  `gorm:"column:load5_max"`
	Load15Sum    *float64  `gorm:"column:load15_sum"`
	Load15Count  int       `gorm:"column:load15_count"`
	Load15Max    *float64  `gorm:"column:load15_max"`
	NetRecvRate  *float64  `gorm:"column:net_recv_rate"`
	NetRecvPeak  *float64  `gorm:"column:net_recv_peak"`
	NetRecvCov   *float64  `gorm:"column:net_recv_coverage"`
	NetSentRate  *float64  `gorm:"column:net_sent_rate"`
	NetSentPeak  *float64  `gorm:"column:net_sent_peak"`
	NetSentCov   *float64  `gorm:"column:net_sent_coverage"`
	DiskReadRate *float64  `gorm:"column:disk_read_rate"`
	DiskReadPeak *float64  `gorm:"column:disk_read_peak"`
	DiskReadCov  *float64  `gorm:"column:disk_read_coverage"`
	DiskWrtRate  *float64  `gorm:"column:disk_write_rate"`
	DiskWrtPeak  *float64  `gorm:"column:disk_write_peak"`
	DiskWrtCov   *float64  `gorm:"column:disk_write_coverage"`
}
