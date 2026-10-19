package repository

import (
	"fmt"
	"time"
)

// Delta and rollup constants.
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

// deltaSamplesCTE builds the shared adjacent-sample CTE. It exposes two
// relations for the aggregators:
//
//   - samples: one row per sample (plus the single predecessor row), with the
//     adjacent-pair rate of every counter. Gauges are aggregated from here, so a
//     sample is counted exactly once in exactly one bucket — the bucket that
//     contains its own timestamp.
//   - pairs: one row per (pair, overlapping bucket) with the pair's rate and the
//     weight it contributes to that bucket. Rates are aggregated from here.
//
// The weight is the overlap of the pair interval with three bounds: the bucket it
// is attributed to, the request window, and the sample times themselves. Weighting
// by the full pair interval let time outside the requested window inflate both the
// average and the reported coverage (F02), and attributing a pair entirely to the
// bucket of its later sample charged a boundary-crossing interval to one bucket
// (N02). A pair whose counters reset, decrease or span a gap longer than the
// threshold contributes no weight at all. A real zero increment yields 0, not NULL.
//
// Parameters: $1=server id, $2=scan start, $3=scan end (exclusive), $4=gap
// threshold. bucketIntervalExpr is the bucket width as an SQL interval expression
// (a literal, or a bound parameter in the legacy Query API) and windowStartParam/
// windowEndParam are the request window bounds.
func deltaSamplesCTE(bucketIntervalExpr, windowStartParam, windowEndParam string) string {
	step := bucketIntervalExpr
	scale := fmt.Sprintf("EXTRACT(EPOCH FROM (%s))", bucketIntervalExpr)
	rate := func(counter string) string {
		return fmt.Sprintf(`CASE
			WHEN prev_%[1]s IS NULL OR %[1]s IS NULL THEN NULL
			WHEN boot_id <> prev_boot_id THEN NULL
			WHEN %[1]s < prev_%[1]s THEN NULL
			WHEN EXTRACT(EPOCH FROM (time - prev_time)) <= 0 THEN NULL
			WHEN time - prev_time > $4::interval THEN NULL
			ELSE (%[1]s - prev_%[1]s)::float8 / EXTRACT(EPOCH FROM (time - prev_time))
		END AS %[1]s_rate`, counter)
	}
	rates := ""
	for i, counter := range rateCounters {
		if i > 0 {
			rates += ",\n"
		}
		rates += "\t\t" + rate(counter)
	}
	// Every counter of a pair is valid or invalid independently, but they share the
	// pair interval, so one overlap expression feeds all four weights.
	weighted := ""
	for _, counter := range rateCounters {
		weighted += fmt.Sprintf(`,
			CASE
				WHEN r.%[1]s_rate IS NOT NULL
				THEN GREATEST(0, EXTRACT(EPOCH FROM (
					LEAST(r.time, b.bucket_start + %[2]s, %[3]s::timestamptz)
					- GREATEST(r.prev_time, b.bucket_start, %[4]s::timestamptz)
				)))
				ELSE 0
			END AS %[1]s_weight`, counter, step, windowEndParam, windowStartParam)
	}
	return fmt.Sprintf(`
	WITH bounded AS (
		(SELECT time, memory_used, memory_total, disk_used, disk_total, swap_used, swap_total,
		        cpu_percent, load_1, load_5, load_15,
		        net_bytes_recv, net_bytes_sent, disk_read_bytes, disk_write_bytes, boot_id
		 FROM metrics
		 WHERE server_id = $1 AND time >= $2 AND time < $3)
		UNION ALL
		-- Exactly one predecessor row: rate context for the first in-window sample.
		-- It is never aggregated (the window filter below excludes it), and it keeps
		-- the planned input bounded without widening the gap semantics.
		(SELECT time, memory_used, memory_total, disk_used, disk_total, swap_used, swap_total,
		        cpu_percent, load_1, load_5, load_15,
		        net_bytes_recv, net_bytes_sent, disk_read_bytes, disk_write_bytes, boot_id
		 FROM metrics
		 WHERE server_id = $1 AND time < $2
		 ORDER BY time DESC
		 LIMIT 1)
	),
	lagged AS (
		SELECT *,
		       lag(time) OVER w AS prev_time,
		       lag(boot_id) OVER w AS prev_boot_id,
		       lag(net_bytes_recv) OVER w AS prev_net_bytes_recv,
		       lag(net_bytes_sent) OVER w AS prev_net_bytes_sent,
		       lag(disk_read_bytes) OVER w AS prev_disk_read_bytes,
		       lag(disk_write_bytes) OVER w AS prev_disk_write_bytes
		FROM bounded
		WINDOW w AS (ORDER BY time)
	),
	rates AS (
		SELECT *,
%[1]s
		FROM lagged
	),
	samples AS (
		SELECT * FROM rates
	),
	-- The bucket grid of the request window, generated in SQL so any budget-
	-- resolved interval (1m .. 6h) splits pairs without a bound bucket list.
	-- Named apart from the rollup job's own bucket grid CTE.
	pair_buckets AS (
		SELECT generate_series(
			to_timestamp(floor(EXTRACT(EPOCH FROM %[3]s::timestamptz) / (%[4]s)) * (%[4]s)),
			to_timestamp(floor(EXTRACT(EPOCH FROM %[5]s::timestamptz) / (%[4]s)) * (%[4]s)),
			%[6]s
		) AS bucket_start
	),
	pairs AS (
		SELECT b.bucket_start AS bucket,
		       r.time AS sample_time,
		       r.prev_time,
		       r.net_bytes_recv_rate, r.net_bytes_sent_rate,
		       r.disk_read_bytes_rate, r.disk_write_bytes_rate%[2]s
		FROM rates r
		JOIN pair_buckets b
		  ON r.prev_time IS NOT NULL
		 AND r.prev_time < b.bucket_start + %[6]s
		 AND r.time > b.bucket_start
		-- The pair must touch the window and the bucket at the same time: a pair
		-- that only overlaps one of them contributes nothing anywhere.
		WHERE r.prev_time < %[5]s::timestamptz
		  AND r.time > %[3]s::timestamptz
		  AND LEAST(r.time, b.bucket_start + %[6]s, %[5]s::timestamptz)
		      > GREATEST(r.prev_time, b.bucket_start, %[3]s::timestamptz)
		  -- ...and it must carry at least one usable counter. A pair across a gap
		  -- beyond the threshold has no valid rate in any series, so it is not rate
		  -- data and must not create buckets: doing so reported a long outage as
		  -- fully covered (P2-01). Any series counts, not just CPU or one counter.
		  AND (r.net_bytes_recv_rate IS NOT NULL OR r.net_bytes_sent_rate IS NOT NULL
		       OR r.disk_read_bytes_rate IS NOT NULL OR r.disk_write_bytes_rate IS NOT NULL)
	)`, rates, weighted, windowStartParam, scale, windowEndParam, step)
}

// rateAggregateSelect builds the per-bucket rate aggregate over the split pairs:
// each pair contributes its rate for exactly the seconds it covers inside that
// bucket, so a pair crossing a bucket boundary is split instead of being charged
// entirely to the later bucket (N02).
func rateAggregateSelect() string {
	return fmt.Sprintf(`SELECT bucket,
		%s AS net_recv_rate, %s AS net_recv_peak, %s AS net_recv_coverage,
		%s AS net_sent_rate, %s AS net_sent_peak, %s AS net_sent_coverage,
		%s AS disk_read_rate, %s AS disk_read_peak, %s AS disk_read_coverage,
		%s AS disk_write_rate, %s AS disk_write_peak, %s AS disk_write_coverage
	FROM pairs
	GROUP BY bucket`,
		metricRateExpression("net_bytes_recv"), metricPeakExpression("net_bytes_recv"), metricCoverageExpression("net_bytes_recv"),
		metricRateExpression("net_bytes_sent"), metricPeakExpression("net_bytes_sent"), metricCoverageExpression("net_bytes_sent"),
		metricRateExpression("disk_read_bytes"), metricPeakExpression("disk_read_bytes"), metricCoverageExpression("disk_read_bytes"),
		metricRateExpression("disk_write_bytes"), metricPeakExpression("disk_write_bytes"), metricCoverageExpression("disk_write_bytes"))
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
// Gauges are aggregated per sample and rates per split pair, then joined, so a
// boundary-crossing counter interval is materialized with the correct per-bucket
// weight instead of being stored whole in the later bucket (N02).
func rollupAggregateSelect(bucketExpr string) string {
	gauge := fmt.Sprintf(`
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
		SUM(load_15) AS load15_sum, COUNT(load_15)::int AS load15_count, MAX(load_15) AS load15_max
	FROM samples
	GROUP BY bucket_start`, bucketExpr)
	return fmt.Sprintf(`
	SELECT COALESCE(g.bucket_start, r.bucket) AS bucket_start,
		g.samples, g.first_sample, g.last_sample,
		g.cpu_sum, g.cpu_count, g.cpu_max,
		g.mem_sum, g.mem_count, g.mem_max,
		g.disk_sum, g.disk_count, g.disk_max,
		g.swap_sum, g.swap_count, g.swap_max,
		g.load1_sum, g.load1_count, g.load1_max,
		g.load5_sum, g.load5_count, g.load5_max,
		g.load15_sum, g.load15_count, g.load15_max,
		r.net_recv_rate, r.net_recv_peak, r.net_recv_coverage,
		r.net_sent_rate, r.net_sent_peak, r.net_sent_coverage,
		r.disk_read_rate, r.disk_read_peak, r.disk_read_coverage,
		r.disk_write_rate, r.disk_write_peak, r.disk_write_coverage
	FROM ( %s ) g
	FULL OUTER JOIN ( %s ) r ON r.bucket = g.bucket_start`, gauge, rateAggregateSelect())
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
