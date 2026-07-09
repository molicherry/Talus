package model

import "time"

// Metric represents a single row of system metrics collected from a server.
// It has no BaseModel — TimescaleDB hypertables do not work well with
// auto-increment primary keys.
type Metric struct {
	Time        time.Time `gorm:"not null" json:"time"`
	ServerID    uint      `gorm:"not null" json:"server_id"`
	CPUPercent  *float64  `json:"cpu_percent,omitempty"`
	MemoryUsed  *int64    `json:"memory_used,omitempty"`
	MemoryTotal *int64    `json:"memory_total,omitempty"`
	DiskUsed    *int64    `json:"disk_used,omitempty"`
	DiskTotal   *int64    `json:"disk_total,omitempty"`

	Load1  *float64 `gorm:"column:load_1" json:"load_1,omitempty"`
	Load5  *float64 `gorm:"column:load_5" json:"load_5,omitempty"`
	Load15 *float64 `gorm:"column:load_15" json:"load_15,omitempty"`

	SwapUsed  *int64 `gorm:"column:swap_used" json:"swap_used,omitempty"`
	SwapTotal *int64 `gorm:"column:swap_total" json:"swap_total,omitempty"`

	NetBytesRecv *int64 `gorm:"column:net_bytes_recv" json:"net_bytes_recv,omitempty"`
	NetBytesSent *int64 `gorm:"column:net_bytes_sent" json:"net_bytes_sent,omitempty"`

	DiskReadBytes  *int64 `gorm:"column:disk_read_bytes" json:"disk_read_bytes,omitempty"`
	DiskWriteBytes *int64 `gorm:"column:disk_write_bytes" json:"disk_write_bytes,omitempty"`

	OS            *string `gorm:"column:os" json:"os,omitempty"`
	KernelVersion *string `gorm:"column:kernel_version" json:"kernel_version,omitempty"`
	CPUModel      *string `gorm:"column:cpu_model" json:"cpu_model,omitempty"`
	UptimeSeconds *int64  `gorm:"column:uptime_seconds" json:"uptime_seconds,omitempty"`
}

// TableName overrides the default table name to "metrics".
func (Metric) TableName() string { return "metrics" }
