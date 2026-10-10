package service

import (
	"bytes"
	"sync"
)

// cappedWriter retains at most streamMax bytes, and never more than the shared
// totalMax across all writers. Beyond the cap it keeps accepting (and counting)
// input so the remote command is never blocked by output backpressure; the
// excess is discarded rather than buffered (REQUIREMENTS §7.1.2).
type cappedWriter struct {
	mu        *sync.Mutex
	total     *int64
	totalMax  int64
	streamMax int64
	buf       bytes.Buffer
	retained  int64
	discarded int64
}

func newCappedWriter(mu *sync.Mutex, total *int64, totalMax, streamMax int64) *cappedWriter {
	return &cappedWriter{mu: mu, total: total, totalMax: totalMax, streamMax: streamMax}
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	allow := w.streamMax - w.retained
	if room := w.totalMax - *w.total; room < allow {
		allow = room
	}
	if allow < 0 {
		allow = 0
	}
	n := int64(len(p))
	take := n
	if take > allow {
		take = allow
	}
	if take > 0 {
		_, _ = w.buf.Write(p[:take])
		w.retained += take
		*w.total += take
	}
	if n > take {
		w.discarded += n - take
	}
	return len(p), nil
}

func (w *cappedWriter) String() string { return w.buf.String() }

// execResult assembles an ExecResult and its truncation metadata. Truncation is
// only reported when bytes were actually discarded.
func execResult(stdout, stderr *cappedWriter, exitCode int) *ExecResult {
	return &ExecResult{
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		ExitCode:        exitCode,
		OutputTruncated: stdout.discarded > 0 || stderr.discarded > 0,
		StdoutTruncated: stdout.discarded > 0,
		StderrTruncated: stderr.discarded > 0,
		StdoutRetained:  stdout.retained,
		StderrRetained:  stderr.retained,
		StdoutDiscarded: stdout.discarded,
		StderrDiscarded: stderr.discarded,
	}
}
