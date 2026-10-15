package model

// User represents an application user with an auto-created first-admin pattern.
type User struct {
	BaseModel
	Username     string  `gorm:"uniqueIndex;size:64;not null" json:"username"`
	PasswordHash string  `gorm:"size:256;not null" json:"-"` // bcrypt hash, never serialized
	Role         string  `gorm:"size:32;not null;default:operator" json:"role"`
	APIKeyHash   *string `gorm:"size:128;unique" json:"-"` // nullable, reserved for API key auth
	// TokenVersion is the JWT session version. It is bumped atomically with a
	// password change; a JWT is only accepted while its `tv` claim matches it.
	TokenVersion int64 `gorm:"not null;default:0" json:"-"`
}
