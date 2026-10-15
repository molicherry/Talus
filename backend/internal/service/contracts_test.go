package service

import (
	"errors"
	"testing"
)

func TestStageErrorUnwrap(t *testing.T) {
	inner := errors.New("boom")
	err := &StageError{Stage: StageTransfer, Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("StageError must unwrap to its cause")
	}
	if got := err.Error(); got != "transfer: boom" {
		t.Fatalf("Error() = %q, want %q", got, "transfer: boom")
	}
}

func TestSessionStatesAreDistinct(t *testing.T) {
	seen := map[SessionState]bool{}
	for _, s := range []SessionState{SessionPreparing, SessionReady, SessionRevoked} {
		if s == "" {
			t.Fatal("session state must not be empty")
		}
		if seen[s] {
			t.Fatalf("duplicate session state %q", s)
		}
		seen[s] = true
	}
}

func TestStageClassesAreNonEmpty(t *testing.T) {
	for _, s := range []StageClass{
		StageQuota, StageDial, StageHostKey, StageAuth, StageSession,
		StageTransfer, StageRemoteExit, StageTimeout, StageCanceled,
		StageProtocol, StageIntegrity, StagePermission,
	} {
		if s == "" {
			t.Fatal("stage class must not be empty")
		}
	}
}
