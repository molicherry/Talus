package service

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestCappedWriterBoundaries(t *testing.T) {
	total := int64(0)
	var mu sync.Mutex
	w := newCappedWriter(&mu, &total, 10, 6)

	// Exactly the per-stream limit: retained, not truncated.
	if n, err := w.Write([]byte("aaa")); err != nil || n != 3 {
		t.Fatalf("write = (%d, %v)", n, err)
	}
	if n, err := w.Write([]byte("bbb")); err != nil || n != 3 {
		t.Fatalf("write = (%d, %v)", n, err)
	}
	if w.retained != 6 || w.discarded != 0 {
		t.Fatalf("retained=%d discarded=%d, want 6/0", w.retained, w.discarded)
	}

	// Past the per-stream limit: still accepts, discards the excess.
	if n, err := w.Write([]byte("cccc")); err != nil || n != 4 {
		t.Fatalf("write past limit = (%d, %v)", n, err)
	}
	if w.retained != 6 || w.discarded != 4 {
		t.Fatalf("retained=%d discarded=%d, want 6/4", w.retained, w.discarded)
	}
	if w.String() != "aaabbb" {
		t.Fatalf("retained content = %q", w.String())
	}
}

// TestExecOutputTruncationMetadata pins the per-stream cap, the shared total cap
// and the exit-code/truncation metadata (REQUIREMENTS §7.1.2).
func TestExecOutputTruncationMetadata(t *testing.T) {
	address, hostKey := startExecSSHServer(t, func(_ string, ch ssh.Channel, _ <-chan struct{}) {
		_, _ = ch.Write(bytes.Repeat([]byte("o"), 700))          // stdout: per-stream capped at 600
		_, _ = ch.Stderr().Write(bytes.Repeat([]byte("e"), 500)) // stderr: only 400 left of the 1000 total
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{3}))
		_ = ch.Close()
	})
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	svc.SetOutputLimits(1000, 600)

	result, err := svc.Exec(context.Background(), 1, "emit", 5*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if result.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", result.ExitCode)
	}
	// stdout (700) and stderr (500) are copied concurrently, so which stream is
	// cut first is not deterministic. Assert the invariants instead: the shared
	// total cap holds, each stream stays within its own cap, and exactly the
	// excess is discarded.
	if len(result.Stdout) > 600 || len(result.Stderr) > 600 {
		t.Fatalf("a stream exceeded its per-stream cap: stdout=%d stderr=%d", len(result.Stdout), len(result.Stderr))
	}
	if result.StdoutRetained+result.StderrRetained != 1000 {
		t.Fatalf("retained total = %d, want 1000", result.StdoutRetained+result.StderrRetained)
	}
	if result.StdoutDiscarded+result.StderrDiscarded != 200 {
		t.Fatalf("discarded total = %d, want 200", result.StdoutDiscarded+result.StderrDiscarded)
	}
	if !result.OutputTruncated || (!result.StdoutTruncated && !result.StderrTruncated) {
		t.Fatalf("truncation flags = %+v", result)
	}
}

// TestExecExactLimitIsNotTruncated pins that reaching the limit exactly (then
// EOF) is not reported as truncation.
func TestExecExactLimitIsNotTruncated(t *testing.T) {
	address, hostKey := startExecSSHServer(t, func(_ string, ch ssh.Channel, _ <-chan struct{}) {
		_, _ = ch.Write(bytes.Repeat([]byte("o"), 600))
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
	})
	pool := newTestPool(t)
	seedPool(t, pool, 1, dialTestSSH(t, address, hostKey))
	svc := NewSSHService(pool, staticServerSource{}, nil, time.Second, time.Second)
	svc.SetOutputLimits(1000, 600)

	result, err := svc.Exec(context.Background(), 1, "emit", 5*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(result.Stdout) != 600 || result.StdoutDiscarded != 0 {
		t.Fatalf("stdout len=%d discarded=%d, want 600/0", len(result.Stdout), result.StdoutDiscarded)
	}
	if result.OutputTruncated || result.StdoutTruncated || result.StderrTruncated {
		t.Fatalf("an exact-limit stream must not be truncated: %+v", result)
	}
}
