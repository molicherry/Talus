package model

const (
	ConnectionDirect  = "direct"
	ConnectionSSHJump = "ssh_jump"
	ConnectionSOCKS5  = "socks5"
	SOCKSAuthNone     = "none"
	SOCKSAuthPassword = "username_password"
)

// ServerConnectionConfig is the private, one-to-one SSH routing configuration.
// An absent row means direct. Secrets never belong to ordinary server JSON.
type ServerConnectionConfig struct {
	ServerID          uint    `gorm:"primaryKey" json:"-"`
	Server            *Server `gorm:"foreignKey:ServerID;constraint:OnDelete:CASCADE" json:"-"`
	Mode              string  `gorm:"size:16;not null;check:server_connection_mode,(mode = 'ssh_jump' AND jump_server_id IS NOT NULL AND socks_host = '' AND socks_port = 0 AND socks_auth_type = '' AND socks_username = '' AND encrypted_password IS NULL AND salt IS NULL) OR (mode = 'socks5' AND jump_server_id IS NULL AND socks_host <> '' AND socks_port BETWEEN 1 AND 65535 AND ((socks_auth_type = 'none' AND socks_username = '' AND encrypted_password IS NULL AND salt IS NULL) OR (socks_auth_type = 'username_password' AND socks_username <> '' AND encrypted_password IS NOT NULL AND salt IS NOT NULL)))" json:"-"`
	JumpServerID      *uint   `gorm:"index" json:"-"`
	JumpServer        *Server `gorm:"foreignKey:JumpServerID;constraint:OnDelete:RESTRICT" json:"-"`
	SocksHost         string  `gorm:"size:256;not null;default:''" json:"-"`
	SocksPort         int     `gorm:"not null;default:0" json:"-"`
	SocksAuthType     string  `gorm:"size:32;not null;default:''" json:"-"`
	SocksUsername     string  `gorm:"size:255;not null;default:''" json:"-"`
	EncryptedPassword *string `gorm:"type:text" json:"-"`
	Salt              []byte  `gorm:"type:bytea" json:"-"`
}

// ConnectionConfig is a read-only public projection. A summary contains Mode
// only; a JWT administrator's detail may contain all non-secret fields.
type ConnectionConfig struct {
	Mode         string `json:"mode"`
	JumpServerID *uint  `json:"jump_server_id,omitempty"`
	Host         string `json:"host,omitempty"`
	Port         int    `json:"port,omitempty"`
	AuthType     string `json:"auth_type,omitempty"`
	Username     string `json:"username,omitempty"`
	HasPassword  *bool  `json:"has_password,omitempty"`
}
