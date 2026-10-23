package agentclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestWindowsBootIdentityAndUptime(t *testing.T) {
	a, err := bootID()
	if err != nil || a == "" {
		t.Fatalf("boot identity: %q %v", a, err)
	}
	time.Sleep(1100 * time.Millisecond)
	b, err := bootID()
	if err != nil || a != b {
		t.Fatalf("boot identity changed across agent starts: %q %q %v", a, b, err)
	}
	u, err := systemUptime()
	if err != nil || u == 0 {
		t.Fatalf("uptime: %d %v", u, err)
	}
}

func TestWindowsLoadIsNotFabricated(t *testing.T) {
	r, err := CollectSection("load")
	if err != nil || r.Sample.Load1 != nil || r.Sample.Load5 != nil || r.Sample.Load15 != nil || len(r.Sample.Errors) != 0 {
		t.Fatalf("load: %+v %v", r, err)
	}
}

func TestWindowsPagingFileCapacity(t *testing.T) {
	s, err := CollectSwapMemory()
	if err != nil {
		t.Fatal(err)
	}
	if s.Total > 1<<60 || s.Used > s.Total || s.Free > s.Total || s.UsedPercent < 0 || s.UsedPercent > 100 {
		t.Fatalf("invalid paging-file capacity: %+v", s)
	}
}

func TestWindowsServiceStopAndFailure(t *testing.T) {
	for _, stop := range []svc.Cmd{svc.Stop, svc.Shutdown} {
		requests := make(chan svc.ChangeRequest, 2)
		changes := make(chan svc.Status, 8)
		done := make(chan uint32, 1)
		h := &windowsService{run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}
		go func() { _, code := h.Execute(nil, requests, changes); done <- code }()
		if (<-changes).State != svc.StartPending || (<-changes).State != svc.Running {
			t.Fatal("invalid start states")
		}
		requests <- svc.ChangeRequest{Cmd: svc.Interrogate}
		if (<-changes).State != svc.Running {
			t.Fatal("interrogate changed state")
		}
		requests <- svc.ChangeRequest{Cmd: stop}
		if (<-changes).State != svc.StopPending {
			t.Fatal("missing stop pending")
		}
		select {
		case code := <-done:
			if code != 0 {
				t.Fatalf("exit %d", code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("SCM stop hung")
		}
	}
	h := &windowsService{run: func(context.Context) error { return errors.New("startup failed") }}
	serviceSpecific, code := h.Execute(nil, make(chan svc.ChangeRequest), make(chan svc.Status, 8))
	if !serviceSpecific || code == 0 || h.err == nil {
		t.Fatal("SCM was not informed of startup failure")
	}
}
