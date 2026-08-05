package model

// Service represents an external service that Talus can proxy requests to.
// Credentials are encrypted with AES-256-GCM using per-service random salt,
// matching the SSH credential encryption scheme.
type Service struct {
	BaseModel
	ServerID             *uint             `gorm:"index;constraint:OnUpdate:CASCADE,OnDelete:SET NULL" json:"server_id,omitempty"`
	Name                 string            `gorm:"uniqueIndex;size:128;not null" json:"name"`
	DisplayName          string            `gorm:"size:128" json:"display_name"`
	BaseURL              string            `gorm:"size:512;not null" json:"base_url"`
	EncryptedCredentials map[string]string `gorm:"type:jsonb;serializer:json" json:"-"`
	CredentialHints      map[string]string `gorm:"type:jsonb;serializer:json" json:"credential_hints,omitempty"`
	Description          *string           `gorm:"type:text" json:"description,omitempty"`
	UsageGuide           *string           `gorm:"type:text" json:"usage_guide,omitempty"`
	Salt                 []byte            `gorm:"type:bytea;not null" json:"-"`

	// UsageGuideExcerpt is a non-persisted summary of UsageGuide (first ~200 runes),
	// populated by the service layer for list responses so AI agents can see a
	// directory without the full guide text.
	UsageGuideExcerpt string `gorm:"-" json:"usage_guide_excerpt,omitempty"`
}

func (Service) TableName() string {
	return "services"
}
