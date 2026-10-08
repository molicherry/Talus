package model

import (
	"time"

	"gorm.io/gorm"
)

// AllScopeGatedScopes is the list of all scope-gated scopes used as default for new API keys.
var AllScopeGatedScopes = []string{
	"servers:read",
	"servers:exec",
	"servers:terminal",
	"metrics:read",
	"credentials:read",
	"services:read",
}

type APIKey struct {
	ID                  uint      `gorm:"primaryKey;index:idx_api_key_legacy_owner,where:owner_binding_version = 0 AND (user_id = 0 OR user_id IS NULL)" json:"id"`
	UserID              uint      `gorm:"default:0;index" json:"-"`
	OwnerBindingVersion uint8     `gorm:"type:smallint;not null;default:1" json:"-"`
	Name                string    `gorm:"size:128;not null" json:"name"`
	KeyHash             string    `gorm:"uniqueIndex;not null" json:"-"`
	KeyPrefix           string    `gorm:"size:16;not null" json:"key_prefix"`
	Scopes              []string  `gorm:"type:jsonb;serializer:json;default:'[\"servers:read\",\"servers:exec\",\"servers:terminal\",\"metrics:read\",\"credentials:read\"]'" json:"scopes"`
	ServerIDs           []uint    `gorm:"type:jsonb;serializer:json" json:"server_ids,omitempty"`
	EncryptedRawKey     string    `gorm:"type:text" json:"-"`
	Salt                []byte    `gorm:"type:bytea" json:"-"`
	CreatedAt           time.Time `gorm:"not null" json:"created_at"`
}

func (APIKey) TableName() string { return "api_keys" }

// All application-created keys use the current ownership version, including
// callers without an HTTP identity. Only pre-upgrade rows qualify as legacy.
func (k *APIKey) BeforeCreate(_ *gorm.DB) error {
	k.OwnerBindingVersion = 1
	return nil
}
