package agentclient

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
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

func TestCollectorReplacesStuckWorkerWithoutUnboundedGrowth(t *testing.T) {
	// Exercise the production grace period using virtual time, independent
	// of runner speed and goroutine scheduling latency.
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		first, second := make(chan struct{}), make(chan struct{})
		defer close(second)
		var releaseFirst sync.Once
		defer releaseFirst.Do(func() { close(first) })
		c := &Collector{Run: func(ctx context.Context, name string) (sectionResult, error) {
			switch calls.Add(1) {
			case 1:
				<-first
			case 2:
				<-second
			default:
				return sectionResult{TotalCPU: 42}, nil
			}
			return sectionResult{}, nil
		}}
		run := func() (sectionResult, error) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			return c.runBounded(ctx, "cpu")
		}
		for i := 0; i < 2; i++ {
			if _, err := run(); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stuck worker: %v", err)
			}
			time.Sleep(11 * time.Second)
		}
		for i := 0; i < 3; i++ {
			if _, err := run(); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("unbounded replacement: %v", err)
			}
		}
		if calls.Load() != 2 {
			t.Fatalf("live worker limit: %d", calls.Load())
		}
		releaseFirst.Do(func() { close(first) })
		c.workersMu.Lock()
		oldDone := c.workers["cpu"][0].done
		c.workersMu.Unlock()
		select {
		case <-oldDone:
		case <-time.After(time.Second):
			t.Fatal("old worker did not exit")
		}
		r, err := run()
		if err != nil || r.TotalCPU != 42 {
			t.Fatalf("failed to recover with one stuck worker: %+v %v", r, err)
		}
		// The late completion of the old worker must not erase the remaining one.
		c.workersMu.Lock()
		live := len(c.workers["cpu"])
		c.workersMu.Unlock()
		if live != 2 {
			t.Fatalf("lost tracked worker: %d", live)
		}
	})
}

func TestDiskCollectionSkipsUnavailableWindowsVolumes(t *testing.T) {
	parts := []disk.PartitionStat{{Device: "C:", Mountpoint: "C:", Fstype: "NTFS"}, {Device: "D:", Mountpoint: "D:/", Fstype: "NTFS"}, {Device: "recovery", Mountpoint: "Volume{hidden}", Fstype: "NTFS"}}
	usage := func(path string) (*disk.UsageStat, error) {
		if path == "Volume{hidden}" {
			t.Error("queried a volume without a drive letter")
		}
		if path != "C:/" {
			return nil, errors.New("access denied")
		}
		return &disk.UsageStat{Total: 100, Used: 20}, nil
	}
	used, total, err := collectDiskUsage(parts, usage, true)
	if err != nil || used != 20 || total != 100 {
		t.Fatalf("partial readable disks: %d/%d %v", used, total, err)
	}
	if _, _, err = collectDiskUsage(parts[1:], usage, true); err == nil {
		t.Fatal("no readable disks must remain an error")
	}
	// Linux does not normalize C: to C:/; use a genuinely readable first
	// filesystem so this checks partial failure rather than all disks failing.
	parts[0].Mountpoint = "C:/"
	if _, _, err = collectDiskUsage(parts[:2], usage, false); err == nil {
		t.Fatal("Linux partial disk failure was hidden")
	}
}
