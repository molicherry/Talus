package model

import "time"

// AllScopeGatedScopes is the list of all scope-gated scopes used as default for new API keys.
var AllScopeGatedScopes = []string{
	"servers:read",
	"servers:exec",
	"servers:terminal",
	"metrics:read",
	"credentials:read",
}

type APIKey struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	UserID          uint      `gorm:"not null;index" json:"-"`
	Name            string    `gorm:"size:128;not null" json:"name"`
	KeyHash         string    `gorm:"uniqueIndex;not null" json:"-"`
	KeyPrefix       string    `gorm:"size:16;not null" json:"key_prefix"`
	Scopes          []string  `gorm:"type:jsonb;serializer:json;default:'[\"servers:read\",\"servers:exec\",\"servers:terminal\",\"metrics:read\",\"credentials:read\"]'" json:"scopes"`
	ServerIDs       []uint    `gorm:"type:jsonb;serializer:json" json:"server_ids,omitempty"`
	EncryptedRawKey string    `gorm:"type:text" json:"-"`
	Salt            []byte    `gorm:"type:bytea" json:"-"`
	CreatedAt       time.Time `gorm:"not null" json:"created_at"`
}

func (APIKey) TableName() string { return "api_keys" }
