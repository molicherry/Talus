package model

// TrustedAgentArtifact records only artifacts verified from a Hub's bundled
// manifest. Keeping previous releases lets compatible agents serve metrics
// during an upgrade without trusting a remote binary's self-reported version.
type TrustedAgentArtifact struct {
	Platform        string `gorm:"primaryKey;size:32"`
	SHA256          string `gorm:"primaryKey;size:64"`
	BuildVersion    string `gorm:"not null;size:128"`
	ProtocolVersion int    `gorm:"not null"`
}
