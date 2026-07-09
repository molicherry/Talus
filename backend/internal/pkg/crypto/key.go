package crypto

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/argon2"
)

const (
	// legacySalt is the old hardcoded salt used before per-credential salts.
	// Only used for migrating existing credentials to the new scheme.
	legacySalt = "efda83ecbe626ad00c2e"

	// SaltSize is the size of random salt generated per credential.
	SaltSize = 16
)

// argon2Params are the Argon2id key derivation parameters.
var argon2Params = struct {
	time    uint32
	memory  uint32
	threads uint8
	keyLen  uint32
}{
	time:    1,
	memory:  64 * 1024,
	threads: 4,
	keyLen:  32,
}

// MasterKey holds the raw hex-decoded master key bytes.
// Derived AES-256 keys are computed on demand via DeriveKey or LegacyDeriveKey.
type MasterKey struct {
	raw []byte
}

// NewMasterKey decodes a hex-encoded master key and returns a MasterKey.
// hexKey must be at least 64 hex characters (32 bytes).
func NewMasterKey(hexKey string) (*MasterKey, error) {
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("invalid master key: %w", err)
	}
	if len(raw) < 32 {
		return nil, fmt.Errorf("master key too short: need 32+ bytes, got %d", len(raw))
	}
	return &MasterKey{raw: raw}, nil
}

// DeriveKey derives a 32-byte AES-256 key from the master key using the given salt.
// Each credential should use a unique random salt (see GenerateSalt).
func (mk *MasterKey) DeriveKey(salt []byte) []byte {
	return argon2.IDKey(mk.raw, salt, argon2Params.time, argon2Params.memory, argon2Params.threads, argon2Params.keyLen)
}

// LegacyDeriveKey derives a key using the old hardcoded salt.
// Only used for migrating pre-existing credentials to the per-credential scheme.
func (mk *MasterKey) LegacyDeriveKey() []byte {
	return argon2.IDKey(mk.raw, []byte(legacySalt), argon2Params.time, argon2Params.memory, argon2Params.threads, argon2Params.keyLen)
}

// GenerateSalt returns a cryptographically random salt for use with DeriveKey.
func GenerateSalt() ([]byte, error) {
	salt := make([]byte, SaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	return salt, nil
}
