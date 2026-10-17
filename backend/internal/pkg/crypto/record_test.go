package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestRecordHKDFKnownAnswer(t *testing.T) {
	master, salt := make([]byte, 32), make([]byte, 16)
	for i := range master {
		master[i] = byte(i)
	}
	for i := range salt {
		salt[i] = byte(i)
	}
	mk, err := NewMasterKey(hex.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	c, err := mk.ForRecord(VersionCurrent, salt, PurposeSSH)
	if err != nil {
		t.Fatal(err)
	}
	// Independent HKDF-Extract/Expand HMAC-SHA256 reference calculation with
	// info="talus/secret/v2/ssh" and a single 32-byte expansion block.
	if got := hex.EncodeToString(c.key); got != "71da7d73665bc78fee2dbcd817b15f1950233fdec372ef926aadbbe26c923494" {
		t.Fatalf("HKDF key = %s", got)
	}
}

func TestRecordAuthenticationAndVersionIsolation(t *testing.T) {
	mk, _ := NewMasterKey(strings.Repeat("ab", 32))
	salt := bytes.Repeat([]byte{7}, SaltSize)
	c, err := mk.ForRecord(VersionCurrent, salt, PurposeService)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := c.Encrypt("token", []byte("synthetic-secret"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := c.Decrypt("token", encoded)
	if err != nil || string(plain) != "synthetic-secret" {
		t.Fatalf("roundtrip: %v", err)
	}
	second, _ := c.Encrypt("token", plain)
	if second == encoded {
		t.Fatal("encryption reused nonce")
	}
	if _, err := c.Decrypt("other-token", encoded); err == nil {
		t.Fatal("swapped fields were accepted")
	}
	other, _ := mk.ForRecord(VersionCurrent, salt, PurposeSSH)
	if _, err := other.Decrypt("token", encoded); err == nil {
		t.Fatal("swapped purpose was accepted")
	}
	wrong, _ := NewMasterKey(strings.Repeat("cd", 32))
	wrongCipher, _ := wrong.ForRecord(VersionCurrent, salt, PurposeService)
	if _, err := wrongCipher.Decrypt("token", encoded); err == nil {
		t.Fatal("wrong master key was accepted")
	}
	changedSalt := append([]byte(nil), salt...)
	changedSalt[0]++
	other, _ = mk.ForRecord(VersionCurrent, changedSalt, PurposeService)
	if _, err := other.Decrypt("token", encoded); err == nil {
		t.Fatal("wrong salt was accepted")
	}
	data, _ := base64.StdEncoding.DecodeString(encoded)
	data[len(data)-1] ^= 1
	if _, err := c.Decrypt("token", base64.StdEncoding.EncodeToString(data)); err == nil {
		t.Fatal("tampering accepted")
	}
	for _, version := range []int{0, -1, 3, 255} {
		if _, err := mk.ForRecord(version, salt, PurposeService); err == nil {
			t.Fatalf("accepted version %d", version)
		}
	}
	if _, err := mk.ForRecord(VersionCurrent, nil, PurposeService); err == nil {
		t.Fatal("v2 accepted empty salt")
	}
	if _, err := mk.ForRecord(VersionCurrent, salt, "unknown"); err == nil {
		t.Fatal("accepted unknown purpose")
	}
	legacy, _ := mk.ForRecord(VersionLegacy, salt, PurposeService)
	if _, err := legacy.Decrypt("token", encoded); err == nil {
		t.Fatal("v2 could be downgraded")
	}
}

func TestRecordReadsLegacyAndFixedSaltSSH(t *testing.T) {
	mk, _ := NewMasterKey(strings.Repeat("ab", 32))
	for _, salt := range [][]byte{nil, bytes.Repeat([]byte{3}, SaltSize)} {
		var key []byte
		if len(salt) == 0 {
			key = mk.LegacyDeriveKey()
		} else {
			key = mk.DeriveKey(salt)
		}
		encoded, err := Encrypt([]byte("old-password"), key)
		if err != nil {
			t.Fatal(err)
		}
		legacy, err := mk.ForRecord(VersionLegacy, salt, PurposeSSH)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := legacy.Decrypt("password", encoded)
		if err != nil || string(plain) != "old-password" {
			t.Fatalf("legacy salt length %d: %v", len(salt), err)
		}
		if len(salt) > 0 {
			current, _ := mk.ForRecord(VersionCurrent, salt, PurposeSSH)
			if _, err := current.Decrypt("password", encoded); err == nil {
				t.Fatal("v2 fell back to old decryption")
			}
		}
	}
}

func BenchmarkRecordHKDF(b *testing.B) {
	mk, _ := NewMasterKey(strings.Repeat("ab", 32))
	salt := bytes.Repeat([]byte{1}, SaltSize)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := mk.ForRecord(VersionCurrent, salt, PurposeService); err != nil {
			b.Fatal(err)
		}
	}
}
