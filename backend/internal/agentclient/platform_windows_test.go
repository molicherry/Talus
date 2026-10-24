package agentclient

import (
	"context"
	"errors"
	"io"
	"net"
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

// Use real TCP loopback traffic and the same native counters used by the agent.
// This catches accidental inclusion of the Windows software loopback adapter.
func TestWindowsNetworkExcludesLoopbackTraffic(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	before, err := CollectSection("network")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	const traffic = 32 << 20
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			done <- e
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		_, e = io.CopyN(io.Discard, conn, traffic)
		done <- e
	}()
	conn, err := net.DialTimeout("tcp4", listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	block := make([]byte, 1<<20)
	for i := 0; i < 32; i++ {
		if _, err = conn.Write(block); err != nil {
			t.Fatal(err)
		}
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	after, err := CollectSection("network")
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range []int64{*after.Sample.NetRecv - *before.Sample.NetRecv, *after.Sample.NetSent - *before.Sample.NetSent} {
		if delta < 0 || delta >= traffic/2 {
			t.Fatalf("loopback traffic leaked into host counters: %d bytes", delta)
		}
	}
}
