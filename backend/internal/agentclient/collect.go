package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	stdnet "net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"
	"github.com/vpsmanager/backend/internal/agentwire"
)

type sectionResult struct {
	Sample   agentwire.Sample `json:"sample"`
	TotalCPU float64          `json:"total_cpu,omitempty"`
	IdleCPU  float64          `json:"idle_cpu,omitempty"`
}

// CollectSection runs in an isolated local child. A blocked filesystem syscall
// cannot strand the long-lived sampler or accumulate background goroutines.
func CollectSection(name string) (sectionResult, error) {
	r := sectionResult{}
	s := &r.Sample
	switch name {
	case "cpu":
		v, e := cpu.Times(false)
		if e != nil {
			return r, e
		}
		if len(v) == 0 {
			return r, errors.New("no cpu counters")
		}
		t := v[0]
		r.TotalCPU = t.User + t.Nice + t.System + t.Idle + t.Iowait + t.Irq + t.Softirq + t.Steal
		r.IdleCPU = t.Idle + t.Iowait
	case "memory":
		v, e := mem.VirtualMemory()
		if e != nil {
			return r, e
		}
		s.MemoryUsed = i64(v.Used)
		s.MemoryTotal = i64(v.Total)
		w, e := CollectSwapMemory()
		if e == nil {
			s.SwapUsed = i64(w.Used)
			s.SwapTotal = i64(w.Total)
		} else {
			s.Errors = map[string]string{"swap": "read_failed"}
		}
	case "load":
		// Windows has no equivalent Linux load average. Do not publish the
		// library's process-local approximation from a short-lived worker.
		if runtime.GOOS == "windows" {
			return r, nil
		}
		v, e := load.Avg()
		if e != nil {
			return r, e
		}
		s.Load1 = &v.Load1
		s.Load5 = &v.Load5
		s.Load15 = &v.Load15
	case "disk":
		p, e := disk.Partitions(false)
		if e != nil && (runtime.GOOS != "windows" || len(p) == 0) {
			return r, e
		}
		used, total, e := collectDiskUsage(p, disk.Usage, runtime.GOOS == "windows")
		if e != nil {
			return r, e
		}
		s.DiskUsed = i64(used)
		s.DiskTotal = i64(total)
	case "network":
		v, e := psnet.IOCounters(true)
		if e != nil {
			return r, e
		}
		var recv, sent uint64
		loopback := map[string]bool{}
		if runtime.GOOS == "windows" {
			// gopsutil v4 uses net.Interfaces().Name for its Windows counters,
			// including GetIfEntry2; it does not use the adapter Description.
			interfaces, err := stdnet.Interfaces()
			if err != nil {
				return r, err
			}
			for _, iface := range interfaces {
				if iface.Flags&stdnet.FlagLoopback != 0 {
					loopback[iface.Name] = true
				}
			}
		}
		for _, n := range v {
			if loopback[n.Name] || n.Name == "lo" || strings.HasPrefix(n.Name, "veth") || strings.HasPrefix(n.Name, "docker") || strings.HasPrefix(n.Name, "br-") {
				continue
			}
			recv += n.BytesRecv
			sent += n.BytesSent
		}
		s.NetRecv = i64(recv)
		s.NetSent = i64(sent)
	case "disk_io":
		v, e := disk.IOCounters()
		if e != nil {
			return r, e
		}
		var read, write uint64
		for name, d := range v {
			if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "dm-") || strings.HasPrefix(name, "zram") {
				continue
			}
			read += d.ReadBytes
			write += d.WriteBytes
		}
		s.DiskRead = i64(read)
		s.DiskWrite = i64(write)
	case "host":
		v, e := host.Info()
		if e != nil {
			return r, e
		}
		o := v.OS + " " + v.Platform + " " + v.PlatformVersion
		k := v.KernelVersion + " (" + runtime.GOARCH + ")"
		s.OS = &o
		s.Kernel = &k
		s.Uptime = i64(v.Uptime)
		if c, e := cpu.Info(); e == nil && len(c) > 0 {
			s.CPUModel = &c[0].ModelName
		}
	default:
		return r, errors.New("unknown collection section")
	}
	return r, nil
}

func i64(v uint64) *int64 { n := int64(min(v, uint64(math.MaxInt64))); return &n }
func physicalFS(fs string) bool {
	switch fs {
	case "ext2", "ext3", "ext4", "xfs", "btrfs", "zfs", "f2fs", "jfs", "reiserfs", "bcachefs", "simfs", "ntfs", "refs", "fat", "fat32", "vfat", "exfat":
		return true
	}
	return false
}

// Windows recovery volumes and removable drives can be inaccessible. Sum only
// readable drive-letter roots there; Linux retains its all-filesystems contract.
func collectDiskUsage(partitions []disk.PartitionStat, usage func(string) (*disk.UsageStat, error), windows bool) (uint64, uint64, error) {
	seen := map[string]bool{}
	var used, total uint64
	var failed bool
	for _, p := range partitions {
		if seen[p.Device] || (p.Mountpoint != "/" && !physicalFS(strings.ToLower(p.Fstype))) {
			continue
		}
		if windows && !windowsDriveRoot(p.Mountpoint) {
			continue
		}
		seen[p.Device] = true
		mountpoint := p.Mountpoint
		if windows && len(mountpoint) == 2 {
			mountpoint += "/" // gopsutil can return C:; query its root, not its cwd.
		}
		v, err := usage(mountpoint)
		if err != nil {
			failed = true
			continue
		}
		used += v.Used
		total += v.Total
	}
	if total == 0 || (failed && !windows) {
		return 0, 0, errors.New("filesystem unavailable")
	}
	return used, total, nil
}

func windowsDriveRoot(p string) bool {
	return (len(p) == 2 || len(p) == 3 && (p[2] == '/' || p[2] == '\\')) && p[1] == ':' &&
		((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
}

type Collector struct {
	Executable string
	BootID     string
	previous   *sectionResult
	metadata   agentwire.Sample
	metadataAt time.Time
	Run        func(context.Context, string) (sectionResult, error)
	workersMu  sync.Mutex
	workers    map[string][]*collectionWorker
}

// At most one abandoned worker and one replacement may be alive per section.
// A grace period lets CommandContext reap killed children before a takeover.
// Keeping the abandoned worker tracked prevents repeated timeouts leaking an
// unbounded number of processes when the kernel cannot finish a syscall.
type collectionWorker struct {
	done       chan struct{}
	result     sectionResult
	err        error
	retryAfter time.Time // protected by workersMu
}

func (c *Collector) runBounded(ctx context.Context, name string) (sectionResult, error) {
	if err := ctx.Err(); err != nil {
		return sectionResult{}, err
	}
	c.workersMu.Lock()
	if c.workers == nil {
		c.workers = map[string][]*collectionWorker{}
	}
	live := make([]*collectionWorker, 0, 2)
	for _, w := range c.workers[name] {
		select {
		case <-w.done:
		default:
			live = append(live, w)
		}
	}
	c.workers[name] = live
	if len(live) > 0 {
		last := live[len(live)-1]
		if len(live) >= 2 || last.retryAfter.IsZero() || time.Now().Before(last.retryAfter) {
			c.workersMu.Unlock()
			return sectionResult{}, context.DeadlineExceeded
		}
	}
	childCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	w := &collectionWorker{done: make(chan struct{})}
	c.workers[name] = append(live, w)
	c.workersMu.Unlock()
	go func() {
		w.result, w.err = c.run(childCtx, name)
		close(w.done)
	}()
	defer cancel()
	select {
	case <-w.done:
		return w.result, w.err
	case <-childCtx.Done():
		c.workersMu.Lock()
		w.retryAfter = time.Now().Add(10 * time.Second)
		c.workersMu.Unlock()
		return sectionResult{}, childCtx.Err()
	}
}

func NewCollector() (*Collector, error) {
	exe, e := os.Executable()
	if e != nil {
		return nil, e
	}
	boot, e := bootID()
	if e != nil {
		return nil, e
	}
	return &Collector{Executable: exe, BootID: boot}, nil
}

func (c *Collector) run(ctx context.Context, name string) (sectionResult, error) {
	if c.Run != nil {
		return c.Run(ctx, name)
	}
	cmd := exec.CommandContext(ctx, c.Executable, "--agent-collect", name)
	cmd.WaitDelay = time.Second
	// Each section has bounded output; children only emit one compact object.
	var b limitedBuffer
	b.limit = 32 << 10
	cmd.Stdout = &b
	cmd.Stderr = nil
	err := cmd.Run()
	var r sectionResult
	if err == nil {
		err = json.Unmarshal(b.data, &r)
	}
	return r, err
}

type limitedBuffer struct {
	data  []byte
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > b.limit {
		return 0, errors.New("collector output limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (c *Collector) Collect(ctx context.Context) (agentwire.Sample, error) {
	id, e := agentwire.NewID()
	if e != nil {
		return agentwire.Sample{}, e
	}
	s := agentwire.Sample{ID: id, BootID: c.BootID, Time: time.Now().UTC(), Errors: map[string]string{}}
	names := []string{"cpu", "memory", "load", "disk", "network", "disk_io"}
	if time.Since(c.metadataAt) > 10*time.Minute {
		names = append(names, "host")
	}
	type result struct {
		name string
		r    sectionResult
		err  error
	}
	ch := make(chan result, len(names))
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sectionCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			r, e := c.runBounded(sectionCtx, name)
			ch <- result{name, r, e}
		}(name)
	}
	wg.Wait()
	close(ch)
	for v := range ch {
		if v.err != nil {
			reason := "read_failed"
			if errors.Is(v.err, context.DeadlineExceeded) || strings.Contains(v.err.Error(), "signal: killed") {
				reason = "timeout"
			}
			s.Errors[v.name] = reason
			continue
		}
		r := v.r.Sample
		for k, e := range r.Errors {
			s.Errors[k] = e
		}
		switch v.name {
		case "cpu":
			if c.previous != nil {
				total := v.r.TotalCPU - c.previous.TotalCPU
				idle := v.r.IdleCPU - c.previous.IdleCPU
				if total > 0 && idle >= 0 && idle <= total {
					p := 100 * (total - idle) / total
					s.CPU = &p
				} else {
					s.Errors["cpu"] = "counter_reset"
				}
			} else {
				s.Errors["cpu"] = "warming_up"
			}
			vCopy := v.r
			c.previous = &vCopy
		case "memory":
			s.MemoryUsed = r.MemoryUsed
			s.MemoryTotal = r.MemoryTotal
			s.SwapUsed = r.SwapUsed
			s.SwapTotal = r.SwapTotal
		case "load":
			s.Load1 = r.Load1
			s.Load5 = r.Load5
			s.Load15 = r.Load15
		case "disk":
			s.DiskUsed = r.DiskUsed
			s.DiskTotal = r.DiskTotal
		case "network":
			s.NetRecv = r.NetRecv
			s.NetSent = r.NetSent
		case "disk_io":
			s.DiskRead = r.DiskRead
			s.DiskWrite = r.DiskWrite
		case "host":
			c.metadata = r
			c.metadataAt = time.Now()
		}
	}
	s.OS = c.metadata.OS
	s.Kernel = c.metadata.Kernel
	s.CPUModel = c.metadata.CPUModel
	if uptime, e := systemUptime(); e == nil {
		s.Uptime = i64(uptime)
	}
	if len(s.Errors) == 0 {
		s.Errors = nil
	}
	return s, nil
}
