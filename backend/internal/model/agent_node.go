package model

import "time"

// AgentNode credentials are distinct from administrator/API credentials. Secret
// fields are never serialized and are encrypted with a purpose-scoped key.
type AgentNode struct {
	ServerID      uint       `gorm:"primaryKey" json:"-"`
	InstallID     string     `gorm:"not null;size:32" json:"-"`
	TokenHash     string     `gorm:"uniqueIndex;size:64;not null" json:"-"`
	TokenCipher   string     `gorm:"not null" json:"-"`
	Salt          []byte     `gorm:"not null" json:"-"`
	Target        string     `gorm:"not null" json:"-"`
	InstalledHash string     `json:"-"`
	ConfigHash    string     `json:"-"`
	Deployment    string     `gorm:"not null;default:pending" json:"-"`
	DeployError   string     `json:"-"`
	LastAttempt   *time.Time `json:"-"`
	LastContact   *time.Time `json:"-"`
	LastSample    *time.Time `json:"-"`
	LastPersisted *time.Time `json:"-"`
	Version       string     `json:"-"`
}

type AgentReceipt struct {
	ServerID   uint      `gorm:"primaryKey"`
	SampleID   string    `gorm:"primaryKey;size:32"`
	ReceivedAt time.Time `gorm:"not null;index"`
}

// MonitoringStatus deliberately separates reachability, collector validity and
// storage freshness. Replayed history cannot make a disconnected agent online.
type MonitoringStatus struct {
	Connection      string            `json:"connection"`
	Collection      string            `json:"collection"`
	Data            string            `json:"data"`
	Deployment      string            `json:"deployment"`
	DeploymentError string            `json:"deployment_error,omitempty"`
	LastContact     *time.Time        `json:"last_contact_at,omitempty"`
	LastSample      *time.Time        `json:"last_sample_at,omitempty"`
	LastPersisted   *time.Time        `json:"last_persisted_at,omitempty"`
	Errors          map[string]string `json:"errors,omitempty"`
	Pending         int               `json:"pending_samples"`
	Dropped         uint64            `json:"dropped_samples"`
	Version         string            `json:"version,omitempty"`
}
