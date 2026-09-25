package sshpool

import (
	"errors"
	"strings"
	"testing"
)

func TestFingerprint(t *testing.T) {
	// Known vector: SHA256("abc").
	got := Fingerprint([]byte("abc"))
	want := "SHA256:ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0"
	if got != want {
		t.Fatalf("Fingerprint = %q, want %q", got, want)
	}
	if Fingerprint([]byte("abd")) == got {
		t.Fatal("different keys produced the same fingerprint")
	}
	if !strings.HasPrefix(got, "SHA256:") {
		t.Fatalf("fingerprint %q is missing the SHA256: prefix", got)
	}
}

func TestHostKeyMismatchErrorCarriesPresentedKey(t *testing.T) {
	cause := errors.New("handshake failed")
	presented := []byte("new-key-blob")
	err := &HostKeyMismatchError{Host: "example.test", Presented: presented, Cause: cause}

	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatal("errors.Is(err, ErrHostKeyMismatch) = false")
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause is not reachable")
	}
	var got *HostKeyMismatchError
	if !errors.As(err, &got) {
		t.Fatal("errors.As did not extract the mismatch error")
	}
	if got.Host != "example.test" || string(got.Presented) != "new-key-blob" {
		t.Fatalf("unexpected payload: host=%q presented=%q", got.Host, got.Presented)
	}
}
