package model

// Server represents a managed VPS or bare-metal host.
type Server struct {
	BaseModel
	Name         string  `gorm:"uniqueIndex;size:128;not null" json:"name"`
	Host         string  `gorm:"size:256;not null" json:"host"`
	Port         int     `gorm:"not null;default:22" json:"port"`
	Description  *string `gorm:"type:text" json:"description,omitempty"`
	OwnerID      uint    `gorm:"not null;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"owner_id"`
	CredentialID *uint   `gorm:"index;constraint:OnUpdate:CASCADE,OnDelete:SET NULL" json:"credential_id,omitempty"`
	HostKey      *[]byte `gorm:"type:bytea" json:"-"`

	// Transient fields — populated by service layer, never persisted.
	Status        string         `gorm:"-" json:"status,omitempty"`
	LastSeen      *string        `gorm:"-" json:"last_seen,omitempty"`
	LatestMetrics *LatestMetrics `gorm:"-" json:"latest_metrics,omitempty"`
	OS            *string        `gorm:"-" json:"os,omitempty"`
	CPUModel      *string        `gorm:"-" json:"cpu_model,omitempty"`
	UptimeSeconds *int64         `gorm:"-" json:"uptime_seconds,omitempty"`

	Credential *SSHCredential `gorm:"foreignKey:CredentialID" json:"credential,omitempty"`
}

// LatestMetrics holds the most recent snapshot of key metrics for a server.
type LatestMetrics struct {
	CPUPercent    *float64 `json:"cpu_percent,omitempty"`
	MemoryPercent *float64 `json:"memory_percent,omitempty"`
	DiskPercent   *float64 `json:"disk_percent,omitempty"`
}
