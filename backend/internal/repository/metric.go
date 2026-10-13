package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

// MetricRepo provides database access for time-series metrics.
type MetricRepo struct {
	db *gorm.DB
	// deltaGapThreshold is the largest spacing between two samples that may
	// contribute a rate. It tracks the effective collection interval (R07); zero
	// falls back to DefaultDeltaGapThreshold.
	deltaGapThreshold time.Duration
}

// SetDeltaGapThreshold configures the gap threshold from the effective
// collection interval: three intervals, clamped to [1m, 1h] so a pathological
// configuration cannot widen the predecessor scan unboundedly.
func (r *MetricRepo) SetDeltaGapThreshold(interval time.Duration) {
	r.deltaGapThreshold = DeltaGapForInterval(interval)
}

// DeltaGapForInterval derives the sampling-gap threshold from the effective
// collection interval: three intervals exactly, never clamped (§8.3 requires the
// threshold to follow the configured period, and clamping changed the semantics of
// legitimate periods — F03). The scan budget is protected by reading only the
// actual predecessor row instead of widening the threshold.
func DeltaGapForInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return DefaultDeltaGapThreshold
	}
	return 3 * interval
}

// gapThreshold is the threshold in force for this repository.
func (r *MetricRepo) gapThreshold() time.Duration {
	if r.deltaGapThreshold > 0 {
		return r.deltaGapThreshold
	}
	return DefaultDeltaGapThreshold
}

// NewMetricRepo creates a MetricRepo backed by the given database handle.
func NewMetricRepo(db *gorm.DB) *MetricRepo {
	return &MetricRepo{db: db}
}

// MetricFull carries all possible metric fields for a single insert.
type MetricFull struct {
	ServerID       uint
	CPUPercent     *float64
	MemoryUsed     *int64
	MemoryTotal    *int64
	DiskUsed       *int64
	DiskTotal      *int64
	Load1          *float64
	Load5          *float64
	Load15         *float64
	SwapUsed       *int64
	SwapTotal      *int64
	NetBytesRecv   *int64
	NetBytesSent   *int64
	DiskReadBytes  *int64
	DiskWriteBytes *int64
	OS             *string
	KernelVersion  *string
	CPUModel       *string
	UptimeSeconds  *int64
}

// InsertFull stores a complete metrics row.
func (r *MetricRepo) InsertFull(ctx context.Context, m *MetricFull) error {
	row := model.Metric{
		Time:           time.Now().UTC(),
		ServerID:       m.ServerID,
		CPUPercent:     m.CPUPercent,
		MemoryUsed:     m.MemoryUsed,
		MemoryTotal:    m.MemoryTotal,
		DiskUsed:       m.DiskUsed,
		DiskTotal:      m.DiskTotal,
		Load1:          m.Load1,
		Load5:          m.Load5,
		Load15:         m.Load15,
		SwapUsed:       m.SwapUsed,
		SwapTotal:      m.SwapTotal,
		NetBytesRecv:   m.NetBytesRecv,
		NetBytesSent:   m.NetBytesSent,
		DiskReadBytes:  m.DiskReadBytes,
		DiskWriteBytes: m.DiskWriteBytes,
		OS:             m.OS,
		KernelVersion:  m.KernelVersion,
		CPUModel:       m.CPUModel,
		UptimeSeconds:  m.UptimeSeconds,
	}
	return r.db.WithContext(ctx).Create(&row).Error
}

// Insert stores a single aggregated metrics row for a server.
// All numeric values are nullable — pass nil for fields the agent did not collect.
func (r *MetricRepo) Insert(
	ctx context.Context,
	serverID uint,
	cpuPercent *float64,
	memUsed *int64,
	memTotal *int64,
	diskUsed *int64,
	diskTotal *int64,
) error {
	return r.InsertFull(ctx, &MetricFull{
		ServerID:    serverID,
		CPUPercent:  cpuPercent,
		MemoryUsed:  memUsed,
		MemoryTotal: memTotal,
		DiskUsed:    diskUsed,
		DiskTotal:   diskTotal,
	})
}

// AggregatedMetric holds time-bucketed, averaged metrics for API responses.
type AggregatedMetric struct {
	Time time.Time `gorm:"column:bucket" json:"time"`
	// SampledAt is the latest *real* sample time inside the bucket. A bucket start
	// is an aggregation boundary, not an observation, so freshness must never be
	// derived from it (R18).
	SampledAt     *time.Time `gorm:"column:sampled_at" json:"sampled_at,omitempty"`
	CPUPercent    *float64   `gorm:"column:cpu_percent" json:"cpu_percent"`
	MemoryPercent *float64   `gorm:"column:memory_percent" json:"memory_percent"`
	DiskPercent   *float64   `gorm:"column:disk_percent" json:"disk_percent"`
	Load1         *float64   `gorm:"column:load_1" json:"load_1"`
	Load5         *float64   `gorm:"column:load_5" json:"load_5"`
	Load15        *float64   `gorm:"column:load_15" json:"load_15"`
	SwapPercent   *float64   `gorm:"column:swap_percent" json:"swap_percent"`
	NetRecvRate   *float64   `gorm:"column:net_recv_rate" json:"net_recv_rate"`
	NetSentRate   *float64   `gorm:"column:net_sent_rate" json:"net_sent_rate"`
	DiskReadRate  *float64   `gorm:"column:disk_read_rate" json:"disk_read_rate"`
	DiskWriteRate *float64   `gorm:"column:disk_write_rate" json:"disk_write_rate"`

	// Observed maxima of the sampled interval (§8.3): retained separately from the
	// average so long-range charts can still show peaks.
	CPUPeak      *float64 `gorm:"column:cpu_peak" json:"cpu_peak"`
	MemoryPeak   *float64 `gorm:"column:memory_peak" json:"memory_peak"`
	DiskPeak     *float64 `gorm:"column:disk_peak" json:"disk_peak"`
	SwapPeak     *float64 `gorm:"column:swap_peak" json:"swap_peak"`
	Load1Peak    *float64 `gorm:"column:load_1_peak" json:"load_1_peak"`
	Load5Peak    *float64 `gorm:"column:load_5_peak" json:"load_5_peak"`
	Load15Peak   *float64 `gorm:"column:load_15_peak" json:"load_15_peak"`
	NetRecvPeak  *float64 `gorm:"column:net_recv_peak" json:"net_recv_peak"`
	NetSentPeak  *float64 `gorm:"column:net_sent_peak" json:"net_sent_peak"`
	DiskReadPeak *float64 `gorm:"column:disk_read_peak" json:"disk_read_peak"`
	DiskWrtPeak  *float64 `gorm:"column:disk_write_peak" json:"disk_write_peak"`

	// Per-bucket sums and counts. A window average must be sample-weighted
	// (sum/count), never the mean of bucket means (F06).
	CPUSum             *float64 `gorm:"column:cpu_sum" json:"cpu_sum"`
	CPUCount           int      `gorm:"column:cpu_count" json:"cpu_count"`
	MemoryPercentSum   *float64 `gorm:"column:memory_percent_sum" json:"memory_percent_sum"`
	MemoryPercentCount int      `gorm:"column:memory_percent_count" json:"memory_percent_count"`
	DiskPercentSum     *float64 `gorm:"column:disk_percent_sum" json:"disk_percent_sum"`
	DiskPercentCount   int      `gorm:"column:disk_percent_count" json:"disk_percent_count"`
	SwapPercentSum     *float64 `gorm:"column:swap_percent_sum" json:"swap_percent_sum"`
	SwapPercentCount   int      `gorm:"column:swap_percent_count" json:"swap_percent_count"`
	Load1Sum           *float64 `gorm:"column:load1_sum" json:"load1_sum"`
	Load1Count         int      `gorm:"column:load1_count" json:"load1_count"`
	Load5Sum           *float64 `gorm:"column:load5_sum" json:"load5_sum"`
	Load5Count         int      `gorm:"column:load5_count" json:"load5_count"`
	Load15Sum          *float64 `gorm:"column:load15_sum" json:"load15_sum"`
	Load15Count        int      `gorm:"column:load15_count" json:"load15_count"`

	// Valid coverage seconds behind the rate columns, so a gap is visible.
	NetRecvCov  *float64 `gorm:"column:net_recv_coverage" json:"net_recv_coverage"`
	NetSentCov  *float64 `gorm:"column:net_sent_coverage" json:"net_sent_coverage"`
	DiskReadCov *float64 `gorm:"column:disk_read_coverage" json:"disk_read_coverage"`
	DiskWrtCov  *float64 `gorm:"column:disk_write_coverage" json:"disk_write_coverage"`
}

// LatestMetricRow holds the most recent metric snapshot for a single server.
type LatestMetricRow struct {
	ServerID      uint      `gorm:"column:server_id" json:"-"`
	Time          time.Time `gorm:"column:time" json:"time"`
	CPUPercent    *float64  `gorm:"column:cpu_percent" json:"cpu_percent"`
	MemoryPercent *float64  `gorm:"column:memory_percent" json:"memory_percent"`
	DiskPercent   *float64  `gorm:"column:disk_percent" json:"disk_percent"`
	OS            *string   `gorm:"column:os" json:"os"`
	CPUModel      *string   `gorm:"column:cpu_model" json:"cpu_model"`
	UptimeSeconds *int64    `gorm:"column:uptime_seconds" json:"uptime_seconds"`
}

// FindLatestByServerIDs returns the most recent metric row for each server ID.
// Servers with no metrics are absent from the returned map.
func (r *MetricRepo) FindLatestByServerIDs(
	ctx context.Context,
	ids []uint,
) (map[uint]*LatestMetricRow, error) {
	if len(ids) == 0 {
		return map[uint]*LatestMetricRow{}, nil
	}

	sql := `
		SELECT DISTINCT ON (server_id)
			server_id,
			time,
			cpu_percent,
			CASE WHEN memory_total > 0
				 THEN memory_used * 100.0 / memory_total
				 ELSE NULL END AS memory_percent,
			CASE WHEN disk_total > 0
				 THEN disk_used * 100.0 / disk_total
				 ELSE NULL END AS disk_percent,
			os,
			cpu_model,
			uptime_seconds
		FROM metrics
		WHERE server_id = ANY($1)
		ORDER BY server_id, time DESC
	`

	rows := make([]LatestMetricRow, 0, len(ids))
	if err := r.db.WithContext(ctx).Raw(sql, ids).Scan(&rows).Error; err != nil {
		return nil, err
	}

	result := make(map[uint]*LatestMetricRow, len(rows))
	for i := range rows {
		row := rows[i]
		result[row.ServerID] = &row
	}
	return result, nil
}

// FindLatestTimesByServerIDs returns the timestamp of the most recent metric
// row for each server ID — a lightweight variant used to compute server
// status without transferring full metric payloads. Servers with no metrics
// are absent from the returned map.
func (r *MetricRepo) FindLatestTimesByServerIDs(ctx context.Context, ids []uint) (map[uint]time.Time, error) {
	if len(ids) == 0 {
		return map[uint]time.Time{}, nil
	}

	type row struct {
		ServerID uint      `gorm:"column:server_id"`
		Time     time.Time `gorm:"column:time"`
	}
	sql := `
		SELECT DISTINCT ON (server_id)
			server_id,
			time
		FROM metrics
		WHERE server_id = ANY($1)
		ORDER BY server_id, time DESC
	`

	rows := make([]row, 0, len(ids))
	if err := r.db.WithContext(ctx).Raw(sql, ids).Scan(&rows).Error; err != nil {
		return nil, err
	}

	result := make(map[uint]time.Time, len(rows))
	for _, r := range rows {
		result[r.ServerID] = r.Time
	}
	return result, nil
}

// Query returns aggregated metrics for a server over a time range.
// interval is a PostgreSQL interval string (e.g. "1 minute", "5 minutes", "1 hour").
func (r *MetricRepo) Query(
	ctx context.Context,
	serverID uint,
	from, to time.Time,
	interval string,
) ([]AggregatedMetric, error) {
	// Rates come from adjacent samples (REQUIREMENTS §8.3): a counter reset, a
	// zero/negative time delta or a gap beyond the declared threshold yields an
	// unknown value (a gap), while a real zero increment is a real zero. The scan
	// starts one threshold early so the first bucket has a predecessor.
	// Gauges come from the per-sample rows (one bucket each) and rates from the
	// per-bucket split pairs, so a boundary-crossing counter interval is weighted
	// per bucket instead of being charged to the later one (N02).
	gauge := `
		SELECT
			to_timestamp(floor(EXTRACT(EPOCH FROM time) / EXTRACT(EPOCH FROM $5::interval))
			            * EXTRACT(EPOCH FROM $5::interval)) AS bucket,
			AVG(cpu_percent) AS cpu_percent,
			CASE WHEN SUM(CASE WHEN memory_total > 0 THEN 1 ELSE 0 END) > 0
				 THEN SUM(CASE WHEN memory_total > 0 THEN memory_used * 100.0 / memory_total ELSE 0 END)
				      / SUM(CASE WHEN memory_total > 0 THEN 1 ELSE 0 END)
				 ELSE NULL END AS memory_percent,
			CASE WHEN SUM(CASE WHEN disk_total > 0 THEN 1 ELSE 0 END) > 0
				 THEN SUM(CASE WHEN disk_total > 0 THEN disk_used * 100.0 / disk_total ELSE 0 END)
				      / SUM(CASE WHEN disk_total > 0 THEN 1 ELSE 0 END)
				 ELSE NULL END AS disk_percent,
			AVG(load_1) AS load_1,
			AVG(load_5) AS load_5,
			AVG(load_15) AS load_15,
			CASE WHEN SUM(CASE WHEN swap_total > 0 THEN 1 ELSE 0 END) > 0
				 THEN SUM(CASE WHEN swap_total > 0 THEN swap_used * 100.0 / swap_total ELSE 0 END)
				      / SUM(CASE WHEN swap_total > 0 THEN 1 ELSE 0 END)
				 ELSE NULL END AS swap_percent
		FROM samples
		WHERE time >= $6 AND time < $7
		GROUP BY bucket`
	sql := deltaSamplesCTE("$5::interval", "$6", "$7") + fmt.Sprintf(`,
	aggregate AS (
		SELECT COALESCE(g.bucket, r.bucket) AS bucket,
		       g.cpu_percent, g.memory_percent, g.disk_percent,
		       g.load_1, g.load_5, g.load_15, g.swap_percent,
		       r.net_recv_rate, r.net_sent_rate, r.disk_read_rate, r.disk_write_rate
		FROM ( %s ) g
		FULL OUTER JOIN ( %s ) r ON r.bucket = g.bucket
	)
	SELECT bucket, cpu_percent, memory_percent, disk_percent, load_1, load_5, load_15,
	       swap_percent, net_recv_rate, net_sent_rate, disk_read_rate, disk_write_rate
	FROM aggregate
	ORDER BY bucket ASC`, gauge, rateAggregateSelect())

	var results []AggregatedMetric
	err := r.db.WithContext(ctx).Raw(sql,
		serverID, from, to,
		r.gapThreshold().String(), interval, from, to).Scan(&results).Error
	if err != nil {
		return nil, err
	}
	return results, nil
}
