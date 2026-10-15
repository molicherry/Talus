package handler

import "testing"

func TestExecHandlerOptionsCannotRaiseHardTimeoutCap(t *testing.T) {
	h := NewExecHandler(nil, ExecOptions{DefaultTimeoutSeconds: 600, MaxTimeoutSeconds: 900})
	if h.defaultSecs != 300 || h.maxSecs != 300 {
		t.Fatalf("timeout caps = %d/%d, want 300/300", h.defaultSecs, h.maxSecs)
	}
	reduced := NewExecHandler(nil, ExecOptions{DefaultTimeoutSeconds: 10, MaxTimeoutSeconds: 20})
	if reduced.defaultSecs != 10 || reduced.maxSecs != 20 {
		t.Fatalf("reduced caps = %d/%d", reduced.defaultSecs, reduced.maxSecs)
	}
}
