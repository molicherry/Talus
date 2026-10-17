package crypto

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

const (
	VersionLegacy  = 1
	VersionCurrent = 2
	PurposeSSH     = "ssh"
	PurposeService = "service"
	PurposeSOCKS5  = "socks5"
	PurposeAPIKey  = "api-key"
)

// RecordCipher is scoped to one version, salt and purpose. New records always
// use VersionCurrent; the old KDF is retained only for upgrade/restore reads.
type RecordCipher struct {
	key     []byte
	version int
	purpose string
}

// ForRecord derives a key from a cryptographically random master key. An empty
// SSH salt denotes the historical fixed-salt format, not an HKDF fallback.
func (mk *MasterKey) ForRecord(version int, salt []byte, purpose string) (*RecordCipher, error) {
	switch purpose {
	case PurposeSSH, PurposeService, PurposeSOCKS5, PurposeAPIKey:
	default:
		return nil, fmt.Errorf("unsupported encryption purpose")
	}
	var key []byte
	switch version {
	case VersionLegacy:
		if purpose == PurposeSSH && len(salt) == 0 {
			key = mk.LegacyDeriveKey()
		} else {
			key = mk.DeriveKey(salt)
		}
	case VersionCurrent:
		if len(salt) != SaltSize {
			return nil, fmt.Errorf("invalid v2 encryption salt length")
		}
		var err error
		key, err = hkdf.Key(sha256.New, mk.raw, salt, "talus/secret/v2/"+purpose, 32)
		if err != nil {
			return nil, fmt.Errorf("derive v2 encryption key: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported encryption version: %d", version)
	}
	return &RecordCipher{key: key, version: version, purpose: purpose}, nil
}

func (c *RecordCipher) aad(field string) []byte {
	if c.version == VersionLegacy {
		return nil
	}
	// JSON string-array encoding is unambiguous even for arbitrary service keys.
	aad, _ := json.Marshal([]string{"talus/secret/v2", c.purpose, field})
	return aad
}

func (c *RecordCipher) Encrypt(field string, plaintext []byte) (string, error) {
	return encryptAAD(plaintext, c.key, c.aad(field))
}

func (c *RecordCipher) Decrypt(field, encoded string) ([]byte, error) {
	return decryptAAD(encoded, c.key, c.aad(field))
}
