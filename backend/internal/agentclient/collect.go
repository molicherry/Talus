package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
		w, e := mem.SwapMemory()
		if e == nil {
			s.SwapUsed = i64(w.Used)
			s.SwapTotal = i64(w.Total)
		} else {
			s.Errors = map[string]string{"swap": "read_failed"}
		}
	case "load":
		v, e := load.Avg()
		if e != nil {
			return r, e
		}
		s.Load1 = &v.Load1
		s.Load5 = &v.Load5
		s.Load15 = &v.Load15
	case "disk":
		p, e := disk.Partitions(false)
		if e != nil {
			return r, e
		}
		seen := map[string]bool{}
		var used, total uint64
		var failed bool
		for _, p := range p {
			if seen[p.Device] || (p.Mountpoint != "/" && !physicalFS(p.Fstype)) {
				continue
			}
			seen[p.Device] = true
			v, e := disk.Usage(p.Mountpoint)
			if e != nil {
				failed = true
				continue
			}
			used += v.Used
			total += v.Total
		}
		if total == 0 || failed {
			return r, errors.New("filesystem unavailable")
		}
		s.DiskUsed = i64(used)
		s.DiskTotal = i64(total)
	case "network":
		v, e := psnet.IOCounters(true)
		if e != nil {
			return r, e
		}
		var recv, sent uint64
		for _, n := range v {
			if n.Name == "lo" || strings.HasPrefix(n.Name, "veth") || strings.HasPrefix(n.Name, "docker") || strings.HasPrefix(n.Name, "br-") {
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
	case "ext2", "ext3", "ext4", "xfs", "btrfs", "zfs", "f2fs", "jfs", "reiserfs", "bcachefs", "simfs", "ntfs", "vfat", "exfat":
		return true
	}
	return false
}

type Collector struct {
	Executable string
	BootID     string
	previous   *sectionResult
	metadata   agentwire.Sample
	metadataAt time.Time
	Run        func(context.Context, string) (sectionResult, error)
	workersMu  sync.Mutex
	workers    map[string]chan collectionResult
}

type collectionResult struct {
	result sectionResult
	err    error
}

func (c *Collector) runBounded(ctx context.Context, name string) (sectionResult, error) {
	c.workersMu.Lock()
	if c.workers == nil {
		c.workers = map[string]chan collectionResult{}
	}
	if previous := c.workers[name]; previous != nil {
		select {
		case <-previous:
			delete(c.workers, name)
		default:
			c.workersMu.Unlock()
			return sectionResult{}, context.DeadlineExceeded
		}
	}
	childCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	done := make(chan collectionResult, 1)
	c.workers[name] = done
	c.workersMu.Unlock()
	go func() { r, e := c.run(childCtx, name); done <- collectionResult{r, e} }()
	defer cancel()
	select {
	case result := <-done:
		c.workersMu.Lock()
		delete(c.workers, name)
		c.workersMu.Unlock()
		return result.result, result.err
	case <-childCtx.Done():
		return sectionResult{}, childCtx.Err()
	}
}

func NewCollector() (*Collector, error) {
	exe, e := os.Executable()
	if e != nil {
		return nil, e
	}
	b, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return nil, e
	}
	return &Collector{Executable: exe, BootID: strings.TrimSpace(string(b))}, nil
}

func (c *Collector) run(ctx context.Context, name string) (sectionResult, error) {
	if c.Run != nil {
		return c.Run(ctx, name)
	}
	cmd := exec.CommandContext(ctx, c.Executable, "--agent-collect", name)
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
	// /proc/uptime does not touch filesystems or execute external commands.
	if b, e := os.ReadFile("/proc/uptime"); e == nil {
		var uptime float64
		if _, e = fmt.Sscan(string(b), &uptime); e == nil && uptime >= 0 {
			s.Uptime = i64(uint64(uptime))
		}
	}
	if len(s.Errors) == 0 {
		s.Errors = nil
	}
	return s, nil
}
