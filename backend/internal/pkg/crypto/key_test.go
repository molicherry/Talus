package crypto

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestNewMasterKey_Valid(t *testing.T) {
	// 64 hex chars = 32 bytes (minimum)
	mk, err := NewMasterKey("aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")
	if err != nil {
		t.Fatalf("NewMasterKey failed: %v", err)
	}
	if mk == nil {
		t.Fatal("expected non-nil MasterKey")
	}
}

func TestNewMasterKey_InvalidHex(t *testing.T) {
	_, err := NewMasterKey("not-hex!!!")
	if err == nil {
		t.Error("expected error for invalid hex input")
	}
}

func TestNewMasterKey_TooShort(t *testing.T) {
	// 63 hex chars = 31.5 bytes → decoded as 31 bytes, which is < 32
	_, err := NewMasterKey("aa")
	if err == nil {
		t.Error("expected error for key shorter than 32 bytes")
	}
}

func TestDeriveKey_Deterministic(t *testing.T) {
	hexKey := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	mk, err := NewMasterKey(hexKey)
	if err != nil {
		t.Fatal(err)
	}

	salt := []byte("test-salt-16byte")
	key1 := mk.DeriveKey(salt)
	key2 := mk.DeriveKey(salt)

	if !bytes.Equal(key1, key2) {
		t.Error("DeriveKey should be deterministic with same inputs")
	}

	if len(key1) != 32 {
		t.Errorf("DeriveKey output length: got %d, want 32", len(key1))
	}
}

func TestDeriveKey_DifferentSaltsGiveDifferentKeys(t *testing.T) {
	hexKey := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	mk, err := NewMasterKey(hexKey)
	if err != nil {
		t.Fatal(err)
	}

	salt1 := []byte("test-salt-16byte")
	salt2 := []byte("diff-salt-16byte")

	key1 := mk.DeriveKey(salt1)
	key2 := mk.DeriveKey(salt2)

	if bytes.Equal(key1, key2) {
		t.Error("different salts should produce different derived keys")
	}
}

func TestGenerateSalt_Length(t *testing.T) {
	salt, err := GenerateSalt()
	if err != nil {
		t.Fatalf("GenerateSalt failed: %v", err)
	}
	if len(salt) != SaltSize {
		t.Errorf("GenerateSalt length: got %d, want %d", len(salt), SaltSize)
	}
}

func TestGenerateSalt_Random(t *testing.T) {
	salt1, err := GenerateSalt()
	if err != nil {
		t.Fatal(err)
	}
	salt2, err := GenerateSalt()
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(salt1, salt2) {
		t.Error("consecutive GenerateSalt calls should produce different values")
	}
}

func TestDeriveKey_IntegrationWithEncrypt(t *testing.T) {
	// Full integration: master key → derive → encrypt → decrypt
	hexKey := hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	mk, err := NewMasterKey(hexKey)
	if err != nil {
		t.Fatal(err)
	}

	salt, err := GenerateSalt()
	if err != nil {
		t.Fatal(err)
	}

	derivedKey := mk.DeriveKey(salt)
	plaintext := []byte("ssh private key content here")

	encoded, err := Encrypt(plaintext, derivedKey)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	decoded, err := Decrypt(encoded, derivedKey)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if string(decoded) != string(plaintext) {
		t.Errorf("integration round-trip: got %q, want %q", string(decoded), string(plaintext))
	}
}
