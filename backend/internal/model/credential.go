package model

// SSHCredential stores encrypted SSH authentication credentials.
// Encrypted fields are NEVER serialized to JSON.
// ServerID is deprecated — servers now reference credentials via servers.credential_id.
type SSHCredential struct {
	BaseModel
	Name                string  `gorm:"size:128" json:"name,omitempty"`
	ServerID            *uint   `gorm:"index;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"server_id,omitempty"`
	AuthType            string  `gorm:"size:16;not null" json:"auth_type"`
	Username            string  `gorm:"size:64;not null" json:"username"`
	EncryptedPassword   *string `gorm:"type:text" json:"-"`
	EncryptedPrivateKey *string `gorm:"type:text" json:"-"`
	KeyFingerprint      *string `gorm:"size:128" json:"key_fingerprint,omitempty"`
	Salt                []byte  `gorm:"type:bytea" json:"-"`
}

// TableName overrides the default table name.
func (SSHCredential) TableName() string {
	return "ssh_credentials"
}
