package crypto

import (
	"encoding/base64"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	plaintext := []byte("this is a secret credential value")

	encoded, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	decoded, err := Decrypt(encoded, key)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if string(decoded) != string(plaintext) {
		t.Errorf("round-trip mismatch: got %q, want %q", string(decoded), string(plaintext))
	}
}

func TestEncryptProducesDifferentOutputEachTime(t *testing.T) {
	key := make([]byte, 32)
	plaintext := []byte("same plaintext")

	enc1, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatal(err)
	}
	enc2, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatal(err)
	}

	if enc1 == enc2 {
		t.Error("expected different ciphertexts due to random nonce, got identical outputs")
	}
}

func TestDecryptInvalidBase64(t *testing.T) {
	key := make([]byte, 32)
	_, err := Decrypt("!!!not-valid-base64!!!", key)
	if err == nil {
		t.Error("expected error for invalid base64 input")
	}
}

func TestDecryptWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	for i := range key1 {
		key1[i] = 0xAA
		key2[i] = 0xBB
	}

	encoded, err := Encrypt([]byte("secret"), key1)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Decrypt(encoded, key2)
	if err == nil {
		t.Error("expected decryption failure with wrong key")
	}
}

func TestDecryptShortCiphertext(t *testing.T) {
	key := make([]byte, 32)
	// A valid AES-256-GCM ciphertext must be at least nonceSize (12) + tag (16) = 28 bytes
	// Encode a single byte as base64 — way too short
	short := base64.StdEncoding.EncodeToString([]byte{0x00})
	_, err := Decrypt(short, key)
	if err == nil {
		t.Error("expected error for ciphertext shorter than nonce size")
	}
}

func TestDecryptCorruptedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	encoded, err := Encrypt([]byte("secret"), key)
	if err != nil {
		t.Fatal(err)
	}

	// Flip a byte in the middle of the base64 string
	b := []byte(encoded)
	b[len(b)/2] ^= 0xFF
	corrupted := string(b)

	_, err = Decrypt(corrupted, key)
	if err == nil {
		t.Error("expected decryption failure with corrupted ciphertext")
	}
}
