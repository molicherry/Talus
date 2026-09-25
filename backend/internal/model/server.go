package model

import "time"

// Server represents a managed VPS or bare-metal host.
type Server struct {
	BaseModel
	Name         string  `gorm:"uniqueIndex;size:128;not null" json:"name"`
	Host         string  `gorm:"size:256;not null" json:"host"`
	Port         int     `gorm:"not null;default:22" json:"port"`
	Description  *string `gorm:"type:text" json:"description,omitempty"`
	Notes        *string `gorm:"type:text" json:"notes,omitempty"`
	OwnerID      uint    `gorm:"not null;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"owner_id"`
	CredentialID *uint   `gorm:"index;constraint:OnUpdate:CASCADE,OnDelete:SET NULL" json:"credential_id,omitempty"`
	HostKey      *[]byte `gorm:"type:bytea" json:"-"`
	// HostKeySeen is the host key the server presented when it did not match the
	// pinned HostKey. Persisted so the operator can see the new fingerprint and
	// decide whether to trust it, instead of the connection just failing silently.
	HostKeySeen *[]byte `gorm:"type:bytea" json:"-"`
	// HostKeyMismatchAt is set when the last mismatch was detected, cleared once
	// the operator trusts the key or a connection succeeds.
	HostKeyMismatchAt *time.Time `gorm:"index" json:"host_key_mismatch_at,omitempty"`

	// Transient fields — populated by service layer, never persisted.
	Status        string         `gorm:"-" json:"status,omitempty"`
	LastSeen      *string        `gorm:"-" json:"last_seen,omitempty"`
	LatestMetrics *LatestMetrics `gorm:"-" json:"latest_metrics,omitempty"`
	OS            *string        `gorm:"-" json:"os,omitempty"`
	CPUModel      *string        `gorm:"-" json:"cpu_model,omitempty"`
	UptimeSeconds *int64         `gorm:"-" json:"uptime_seconds,omitempty"`
	// HostKeyMismatch marks a pending host-key change awaiting the operator.
	HostKeyMismatch        bool    `gorm:"-" json:"host_key_mismatch"`
	HostKeyFingerprint     *string `gorm:"-" json:"host_key_fingerprint,omitempty"`
	HostKeySeenFingerprint *string `gorm:"-" json:"host_key_seen_fingerprint,omitempty"`

	Credential *SSHCredential `gorm:"foreignKey:CredentialID" json:"credential,omitempty"`
}

// ServerSummary is a lightweight server representation for list endpoints.
// Field set is the contract for GET /api/v1/servers/summary — keep in sync
// with the frontend ServerSummarySchema.
type ServerSummary struct {
	ID           uint    `json:"id"`
	Name         string  `json:"name"`
	Description  *string `json:"description,omitempty"`
	Host         string  `json:"host"`
	CredentialID *uint   `json:"credential_id"`
	Status       string  `gorm:"-" json:"status"`
	// HostKeyMismatchAt is read from the column; the service turns it into the
	// HostKeyMismatch flag so the list can flag a server whose key changed.
	HostKeyMismatchAt *time.Time `json:"-"`
	HostKeyMismatch   bool       `gorm:"-" json:"host_key_mismatch"`
}

// LatestMetrics holds the most recent snapshot of key metrics for a server.
type LatestMetrics struct {
	CPUPercent    *float64 `json:"cpu_percent,omitempty"`
	MemoryPercent *float64 `json:"memory_percent,omitempty"`
	DiskPercent   *float64 `json:"disk_percent,omitempty"`
}
