package service

import (
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
)

func TestDecorateHostKey(t *testing.T) {
	pinned := []byte("pinned-key")
	seen := []byte("presented-key")
	at := time.Now()

	pending := &model.Server{HostKey: &pinned, HostKeySeen: &seen, HostKeyMismatchAt: &at}
	decorateHostKey(pending)
	if !pending.HostKeyMismatch {
		t.Fatal("a recorded mismatch should set host_key_mismatch")
	}
	if pending.HostKeyFingerprint == nil || pending.HostKeySeenFingerprint == nil {
		t.Fatal("both fingerprints should be populated for comparison")
	}
	if *pending.HostKeyFingerprint == *pending.HostKeySeenFingerprint {
		t.Fatal("different keys produced the same fingerprint")
	}

	clean := &model.Server{HostKey: &pinned}
	decorateHostKey(clean)
	if clean.HostKeyMismatch {
		t.Fatal("no mismatch timestamp must not set host_key_mismatch")
	}
	if clean.HostKeySeenFingerprint != nil {
		t.Fatal("no presented key should leave host_key_seen_fingerprint empty")
	}
	if clean.HostKeyFingerprint == nil {
		t.Fatal("the pinned key fingerprint should still be shown")
	}

	empty := &model.Server{}
	decorateHostKey(empty)
	if empty.HostKeyMismatch || empty.HostKeyFingerprint != nil || empty.HostKeySeenFingerprint != nil {
		t.Fatal("an unbound host key should decorate to zero values")
	}
}
