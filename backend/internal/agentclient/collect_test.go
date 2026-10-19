package agentclient

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCollectorPartialFailureAndBoundedStuckWorker(t *testing.T) {
	var disks atomic.Int32
	var cpu atomic.Int32
	release := make(chan struct{})
	defer close(release)
	c := &Collector{BootID: "boot", Run: func(ctx context.Context, name string) (sectionResult, error) {
		switch name {
		case "disk":
			disks.Add(1)
			<-release
			return sectionResult{}, errors.New("stuck filesystem")
		case "cpu":
			n := float64(cpu.Add(1))
			return sectionResult{TotalCPU: 100 * n, IdleCPU: 50 * n}, nil
		case "memory":
			r := sectionResult{}
			r.Sample.MemoryUsed = i64(20)
			r.Sample.MemoryTotal = i64(100)
			return r, nil
		case "load":
			return sectionResult{}, errors.New("unsupported load")
		default:
			return sectionResult{}, nil
		}
	}}
	start := time.Now()
	s, e := c.Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if time.Since(start) > 3*time.Second || s.MemoryUsed == nil || *s.MemoryUsed != 20 || s.DiskUsed != nil || s.Errors["disk"] != "timeout" {
		t.Fatalf("partial sample: %+v", s)
	}
	start = time.Now()
	s, e = c.Collect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if time.Since(start) > time.Second || disks.Load() != 1 {
		t.Fatal("stuck worker accumulated or blocked next sample")
	}
	if s.CPU == nil || *s.CPU != 50 || s.Errors["load"] != "read_failed" {
		t.Fatalf("CPU continuity: %+v", s)
	}
}
