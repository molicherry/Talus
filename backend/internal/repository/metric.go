package repository

import (
	"context"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

// MetricRepo provides database access for time-series metrics.
type MetricRepo struct {
	db *gorm.DB
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
	Time          time.Time `gorm:"column:bucket" json:"time"`
	CPUPercent    *float64  `gorm:"column:cpu_percent" json:"cpu_percent"`
	MemoryPercent *float64  `gorm:"column:memory_percent" json:"memory_percent"`
	DiskPercent   *float64  `gorm:"column:disk_percent" json:"disk_percent"`
	Load1         *float64  `gorm:"column:load_1" json:"load_1"`
	Load5         *float64  `gorm:"column:load_5" json:"load_5"`
	Load15        *float64  `gorm:"column:load_15" json:"load_15"`
	SwapPercent   *float64  `gorm:"column:swap_percent" json:"swap_percent"`
	NetRecvRate   *float64  `gorm:"column:net_recv_rate" json:"net_recv_rate"`
	NetSentRate   *float64  `gorm:"column:net_sent_rate" json:"net_sent_rate"`
	DiskReadRate  *float64  `gorm:"column:disk_read_rate" json:"disk_read_rate"`
	DiskWriteRate *float64  `gorm:"column:disk_write_rate" json:"disk_write_rate"`
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

// Query returns aggregated metrics for a server over a time range.
// interval is a PostgreSQL interval string (e.g. "1 minute", "5 minutes", "1 hour").
func (r *MetricRepo) Query(
	ctx context.Context,
	serverID uint,
	from, to time.Time,
	interval string,
) ([]AggregatedMetric, error) {
	sql := `
		SELECT
			time_bucket($1::interval, time) AS bucket,
			AVG(cpu_percent) AS cpu_percent,
			CASE WHEN AVG(memory_total) > 0
				 THEN AVG(memory_used) * 100.0 / AVG(memory_total)
				 ELSE NULL END AS memory_percent,
			CASE WHEN AVG(disk_total) > 0
				 THEN AVG(disk_used) * 100.0 / AVG(disk_total)
				 ELSE NULL END AS disk_percent,
			AVG(load_1) AS load_1,
			AVG(load_5) AS load_5,
			AVG(load_15) AS load_15,
			CASE WHEN AVG(swap_total) > 0
				 THEN AVG(swap_used) * 100.0 / AVG(swap_total)
				 ELSE NULL END AS swap_percent,
			CASE WHEN COUNT(*) > 1 AND MAX(net_bytes_recv) IS NOT NULL
				 THEN (MAX(net_bytes_recv) - MIN(net_bytes_recv))::float8
				      / EXTRACT(EPOCH FROM (MAX(time) - MIN(time)))
				 ELSE NULL END AS net_recv_rate,
			CASE WHEN COUNT(*) > 1 AND MAX(net_bytes_sent) IS NOT NULL
				 THEN (MAX(net_bytes_sent) - MIN(net_bytes_sent))::float8
				      / EXTRACT(EPOCH FROM (MAX(time) - MIN(time)))
				 ELSE NULL END AS net_sent_rate,
			CASE WHEN COUNT(*) > 1 AND MAX(disk_read_bytes) IS NOT NULL
				 THEN (MAX(disk_read_bytes) - MIN(disk_read_bytes))::float8
				      / EXTRACT(EPOCH FROM (MAX(time) - MIN(time)))
				 ELSE NULL END AS disk_read_rate,
			CASE WHEN COUNT(*) > 1 AND MAX(disk_write_bytes) IS NOT NULL
				 THEN (MAX(disk_write_bytes) - MIN(disk_write_bytes))::float8
				      / EXTRACT(EPOCH FROM (MAX(time) - MIN(time)))
				 ELSE NULL END AS disk_write_rate
		FROM metrics
		WHERE server_id = $2 AND time >= $3 AND time <= $4
		GROUP BY bucket
		ORDER BY bucket ASC
	`

	var results []AggregatedMetric
	err := r.db.WithContext(ctx).Raw(sql, interval, serverID, from, to).Scan(&results).Error
	if err != nil {
		return nil, err
	}
	return results, nil
}
