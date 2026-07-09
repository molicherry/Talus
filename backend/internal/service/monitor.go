package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vpsmanager/backend/internal/repository"
)

const agentPath = "/tmp/vpsmanager-agent"
const localAgentPath = "/usr/local/bin/vpsmanager-agent"

// agentOutput mirrors the JSON output of the monitoring agent binary.
type agentOutput struct {
	Timestamp  string       `json:"timestamp"`
	Hostname   string       `json:"hostname"`
	CPU        agentCPU      `json:"cpu"`
	Memory     agentMemory   `json:"memory"`
	Swap       *agentSwap    `json:"swap"`
	Disks      []agentDisk   `json:"disks"`
	DiskIO     *agentDiskIO  `json:"disk_io"`
	NetIO      *agentNetIO   `json:"net_io"`
	OS         agentOS       `json:"os"`
	UptimeSecs uint64        `json:"uptime_seconds"`
}

type agentCPU struct {
	Percent float64 `json:"percent"`
	Model   string  `json:"model"`
	Load1   float64 `json:"load_1"`
	Load5   float64 `json:"load_5"`
	Load15  float64 `json:"load_15"`
}

type agentMemory struct {
	UsedBytes  uint64 `json:"used_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

type agentSwap struct {
	UsedBytes  uint64 `json:"used_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

type agentDisk struct {
	UsedBytes  uint64 `json:"used_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

type agentDiskIO struct {
	ReadBytes  uint64 `json:"read_bytes"`
	WriteBytes uint64 `json:"write_bytes"`
}

type agentNetIO struct {
	BytesRecv uint64 `json:"bytes_recv"`
	BytesSent uint64 `json:"bytes_sent"`
}

type agentOS struct {
	OS         string `json:"os"`
	Platform   string `json:"platform"`
	KernelArch string `json:"kernel_arch"`
	KernelVer  string `json:"kernel_version"`
}

// MonitorService periodically collects system metrics from all managed servers
// via the SSH agent binary and persists them to the database.
type MonitorService struct {
	sshSvc     *SSHService
	metricRepo *repository.MetricRepo
	serverRepo *repository.ServerRepo
	interval   time.Duration
	logger     *slog.Logger
}

// NewMonitorService creates a MonitorService with the given dependencies.
func NewMonitorService(
	sshSvc *SSHService,
	metricRepo *repository.MetricRepo,
	serverRepo *repository.ServerRepo,
	interval time.Duration,
) *MonitorService {
	return &MonitorService{
		sshSvc:     sshSvc,
		metricRepo: metricRepo,
		serverRepo: serverRepo,
		interval:   interval,
		logger:     slog.Default().With("component", "monitor"),
	}
}

// Start begins the monitoring loop. It blocks until ctx is cancelled.
func (s *MonitorService) Start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.logger.Info("monitor service started", "interval", s.interval)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("monitor service stopped")
			return
		case <-ticker.C:
			s.collect(ctx)
		}
	}
}

func (s *MonitorService) collect(ctx context.Context) {
	servers, err := s.serverRepo.FindAll(ctx)
	if err != nil {
		s.logger.Error("failed to fetch servers for monitoring", "error", err)
		return
	}

	if len(servers) == 0 {
		return
	}

	var wg sync.WaitGroup
	var collected int
	var mu sync.Mutex

	for _, srv := range servers {
		wg.Add(1)
		go func(serverID uint) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					s.logger.Error("monitor goroutine panicked", "server_id", serverID, "panic", r)
				}
			}()
			if err := s.collectOne(ctx, serverID); err != nil {
				s.logger.Error("failed to collect metrics", "server_id", serverID, "error", err)
				return
			}
			mu.Lock()
			collected++
			mu.Unlock()
		}(srv.ID)
	}

	wg.Wait()
	s.logger.Info("collected metrics", "servers", collected, "total", len(servers))
}

func (s *MonitorService) collectOne(ctx context.Context, serverID uint) error {
	result, err := s.sshSvc.Exec(ctx, serverID, agentPath+" --format json", 30*time.Second)
	if err != nil {
		return err
	}

	if result.ExitCode != 0 {
		// Agent binary not found — auto-upload and retry once.
		if strings.Contains(result.Stderr, "not found") || strings.Contains(result.Stderr, "No such file") {
			s.logger.Info("agent binary not found, uploading...",
				"server_id", serverID,
				"stderr", strings.TrimSpace(result.Stderr),
			)

			// Verify local binary exists.
			if _, statErr := os.Stat(localAgentPath); statErr != nil {
				return fmt.Errorf("local agent binary missing at %s: %w", localAgentPath, statErr)
			}

			if uploadErr := s.sshSvc.CopyFile(ctx, serverID, localAgentPath, agentPath); uploadErr != nil {
				return fmt.Errorf("failed to upload agent binary: %w", uploadErr)
			}

			s.logger.Info("agent binary uploaded, retrying exec", "server_id", serverID)

			// Retry execution after upload.
			var retryErr error
			result, retryErr = s.sshSvc.Exec(ctx, serverID, agentPath+" --format json", 30*time.Second)
			if retryErr != nil {
				return retryErr
			}

			if result.ExitCode != 0 {
				return fmt.Errorf("agent still failing after upload (exit %d): %s",
					result.ExitCode, strings.TrimSpace(result.Stderr))
			}
		} else {
			return fmt.Errorf("agent exited with code %d: %s",
				result.ExitCode, strings.TrimSpace(result.Stderr))
		}
	}

	var out agentOutput
	if err := json.Unmarshal([]byte(result.Stdout), &out); err != nil {
		return fmt.Errorf("failed to parse agent JSON: %w", err)
	}

	var cpuPercent *float64
	if out.CPU.Percent != 0 || out.Timestamp != "" {
		v := out.CPU.Percent
		cpuPercent = &v
	}

	var memUsed, memTotal *int64
	if out.Memory.TotalBytes > 0 {
		u := int64(out.Memory.UsedBytes)
		t := int64(out.Memory.TotalBytes)
		memUsed = &u
		memTotal = &t
	}

	var diskUsed, diskTotal *int64
	if len(out.Disks) > 0 {
		var du, dt uint64
		for _, d := range out.Disks {
			du += d.UsedBytes
			dt += d.TotalBytes
		}
		if dt > 0 {
			u := int64(du)
			t := int64(dt)
			diskUsed = &u
			diskTotal = &t
		}
	}

	var osInfo *string
	if out.OS.OS != "" {
		v := out.OS.OS + " " + out.OS.Platform
		osInfo = &v
	}

	var kernelVer *string
	if out.OS.KernelVer != "" {
		v := out.OS.KernelVer + " (" + out.OS.KernelArch + ")"
		kernelVer = &v
	}

	var cpuModel *string
	if out.CPU.Model != "" {
		v := out.CPU.Model
		cpuModel = &v
	}

	var uptime *int64
	if out.UptimeSecs > 0 {
		v := int64(out.UptimeSecs)
		uptime = &v
	}

	var load1, load5, load15 *float64
	if out.CPU.Load1 != 0 || out.CPU.Load5 != 0 || out.CPU.Load15 != 0 || cpuPercent != nil {
		l1 := out.CPU.Load1
		l5 := out.CPU.Load5
		l15 := out.CPU.Load15
		load1 = &l1
		load5 = &l5
		load15 = &l15
	}

	var swapUsed, swapTotal *int64
	if out.Swap != nil && out.Swap.TotalBytes > 0 {
		u := int64(out.Swap.UsedBytes)
		t := int64(out.Swap.TotalBytes)
		swapUsed = &u
		swapTotal = &t
	}

	var netRecv, netSent *int64
	if out.NetIO != nil {
		r := int64(out.NetIO.BytesRecv)
		s := int64(out.NetIO.BytesSent)
		netRecv = &r
		netSent = &s
	}

	var diskRead, diskWrite *int64
	if out.DiskIO != nil {
		r := int64(out.DiskIO.ReadBytes)
		w := int64(out.DiskIO.WriteBytes)
		diskRead = &r
		diskWrite = &w
	}

	return s.metricRepo.InsertFull(ctx, &repository.MetricFull{
		ServerID:       serverID,
		CPUPercent:     cpuPercent,
		MemoryUsed:     memUsed,
		MemoryTotal:    memTotal,
		DiskUsed:       diskUsed,
		DiskTotal:      diskTotal,
		Load1:          load1,
		Load5:          load5,
		Load15:         load15,
		SwapUsed:       swapUsed,
		SwapTotal:      swapTotal,
		NetBytesRecv:   netRecv,
		NetBytesSent:   netSent,
		DiskReadBytes:  diskRead,
		DiskWriteBytes: diskWrite,
		OS:             osInfo,
		KernelVersion:  kernelVer,
		CPUModel:       cpuModel,
		UptimeSeconds:  uptime,
	})
}
