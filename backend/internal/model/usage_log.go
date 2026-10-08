package model

import (
	"encoding/json"
	"time"
)

// UsageLog is a best-effort operation summary. It deliberately has no soft
// deletion or foreign-key associations: historical snapshots survive resources.
type UsageLog struct {
	ID                   uint64          `gorm:"primaryKey;index:idx_usage_started,priority:2;index:idx_usage_action,priority:3;index:idx_usage_resource,priority:4;index:idx_usage_key,priority:3;index:idx_usage_user,priority:3;index:idx_usage_server,priority:3;index:idx_usage_running_owner,priority:2,where:outcome = 'running'" json:"id,string"`
	OperationID          string          `gorm:"type:uuid;not null;uniqueIndex" json:"operation_id"`
	LegacyAuditEventID   *uint           `gorm:"uniqueIndex" json:"legacy_audit_event_id,omitempty,string"`
	StartedAt            time.Time       `gorm:"not null;index:idx_usage_started,priority:1;index:idx_usage_action,priority:2;index:idx_usage_resource,priority:3;index:idx_usage_key,priority:2;index:idx_usage_user,priority:2;index:idx_usage_server,priority:2" json:"started_at"`
	FinishedAt           *time.Time      `json:"finished_at,omitempty"`
	DurationMS           *int64          `json:"duration_ms,omitempty"`
	RecordedAt           time.Time       `gorm:"not null;autoCreateTime" json:"recorded_at"`
	ReconciledAt         *time.Time      `json:"reconciled_at,omitempty"`
	RetentionAt          *time.Time      `gorm:"index" json:"retention_at,omitempty"`
	Outcome              string          `gorm:"size:32;not null" json:"outcome"`
	Phase                string          `gorm:"size:32;not null" json:"phase"`
	StateSeq             uint64          `gorm:"not null" json:"state_seq"`
	RecoveryReason       string          `gorm:"size:64" json:"recovery_reason,omitempty"`
	Action               string          `gorm:"size:96;not null;index:idx_usage_action,priority:1" json:"action"`
	ResourceType         string          `gorm:"size:64;index:idx_usage_resource,priority:1" json:"resource_type"`
	ResourceID           *uint           `gorm:"index:idx_usage_resource,priority:2" json:"resource_id,omitempty,string"`
	ResourceNameSnapshot string          `gorm:"size:256" json:"resource_name_snapshot,omitempty"`
	ServerID             *uint           `gorm:"index:idx_usage_server,priority:1" json:"server_id,omitempty,string"`
	AuthType             string          `gorm:"size:32;not null" json:"auth_type"`
	UserID               *uint           `gorm:"index:idx_usage_user,priority:1" json:"user_id,omitempty,string"`
	UsernameSnapshot     string          `gorm:"size:256" json:"username_snapshot,omitempty"`
	APIKeyID             *uint           `gorm:"index:idx_usage_key,priority:1" json:"api_key_id,omitempty,string"`
	APIKeyNameSnapshot   string          `gorm:"size:256" json:"api_key_name_snapshot,omitempty"`
	APIKeyPrefixSnapshot string          `gorm:"size:32" json:"api_key_prefix_snapshot,omitempty"`
	RequestID            string          `gorm:"size:128;index" json:"request_id,omitempty"`
	Method               string          `gorm:"size:16" json:"method,omitempty"`
	RoutePattern         string          `gorm:"size:256" json:"route_pattern,omitempty"`
	HTTPStatus           *int            `json:"http_status,omitempty"`
	ClientAddress        string          `gorm:"size:64" json:"client_address,omitempty"`
	Source               string          `gorm:"size:32;not null" json:"source"`
	ExitCode             *int            `json:"exit_code,omitempty"`
	UpstreamStatus       *int            `json:"upstream_status,omitempty"`
	ErrorReason          string          `gorm:"size:64" json:"error_reason,omitempty"`
	Metadata             json.RawMessage `gorm:"type:jsonb;serializer:json;not null;default:'{}'" json:"metadata"`
	OwnerInstanceID      *string         `gorm:"type:uuid;index;index:idx_usage_running_owner,priority:1,where:outcome = 'running'" json:"-"`
}

type UsageLogInstance struct {
	InstanceID string     `gorm:"type:uuid;primaryKey" json:"instance_id"`
	LastSeenAt time.Time  `gorm:"not null" json:"last_seen_at"`
	LeaseUntil time.Time  `gorm:"not null;index" json:"lease_until"`
	RetiredAt  *time.Time `json:"retired_at,omitempty"`
}

type UsageLogBackfillState struct {
	ID          string `gorm:"size:96;primaryKey"`
	Checkpoint  uint64 `gorm:"not null"`
	FinalBound  uint64 `gorm:"not null"`
	Imported    uint64 `gorm:"not null"`
	CompletedAt *time.Time
}
