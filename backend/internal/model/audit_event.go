package model

// AuditEvent records a security-sensitive action for audit trail purposes.
type AuditEvent struct {
	BaseModel
	UserID       uint   `gorm:"index" json:"user_id"`
	Username     string `json:"username"`
	Action       string `gorm:"index" json:"action"`        // e.g. "credential.reveal"
	ResourceType string `gorm:"index" json:"resource_type"` // e.g. "credential"
	ResourceID   uint   `gorm:"index" json:"resource_id"`
	IPAddress    string `json:"ip_address"`
	Details      string `json:"details"`
}
