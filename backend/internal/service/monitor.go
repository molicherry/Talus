package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
)

// agentOutput mirrors the JSON output of the monitoring agent binary, including
// the version contract the Hub verifies before storing any metric (§8.2).
type agentOutput struct {
	AgentVersion    string       `json:"agent_version"`
	ProtocolVersion int          `json:"protocol_version"`
	Timestamp       string       `json:"timestamp"`
	Hostname        string       `json:"hostname"`
	CPU             agentCPU     `json:"cpu"`
	Memory          agentMemory  `json:"memory"`
	Swap            *agentSwap   `json:"swap"`
	Disks           []agentDisk  `json:"disks"`
	DiskIO          *agentDiskIO `json:"disk_io"`
	NetIO           *agentNetIO  `json:"net_io"`
	OS              agentOS      `json:"os"`
	UptimeSecs      uint64       `json:"uptime_seconds"`
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

// monitorServerSource lists the managed nodes the scheduler must collect from.
type monitorServerSource interface {
	FindAll(ctx context.Context) ([]model.Server, error)
}

// MonitorService periodically collects system metrics from all managed servers
// via the SSH agent binary and persists them to the database.
type MonitorService struct {
	sshSvc     *SSHService
	metricRepo *repository.MetricRepo
	serverRepo monitorServerSource
	interval   time.Duration
	logger     *slog.Logger
	clock      monitorClock
	rng        monitorRand
	// taskBudget is the per-collection deadline starting at admission (§8.1.1).
	taskBudget time.Duration
	// deployer owns probing, uploading and protocol verification.
	deployer *AgentDeploymentService
	// taskFn replaces the collection step in tests; production uses collectOne.
	taskFn func(ctx context.Context, serverID uint) error
	// wake asks the scheduler to re-read the managed node list immediately.
	wake chan struct{}
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
		clock:      realMonitorClock{},
		rng:        newSeededMonitorRand(time.Now().UnixNano()),
		taskBudget: monitorTaskBudget,
		wake:       make(chan struct{}, 1),
	}
}

// monitorDiscoveryRetry bounds how long a failed node listing waits before the
// next discovery attempt.
const monitorDiscoveryRetry = 5 * time.Second

// Start runs the per-node scheduler until ctx is cancelled. It stops scheduling,
// cancels in-flight collections and joins them before returning, so the caller
// can close connection and storage dependencies afterwards.
func (s *MonitorService) Start(ctx context.Context) {
	sched := newMonitorScheduler(s.interval, s.clock, s.rng)
	notify := make(chan struct{}, 1)

	var wg sync.WaitGroup
	var mu sync.Mutex
	cancels := map[uint]context.CancelFunc{}

	s.logger.Info("monitor service started", "interval", s.interval)

	for ctx.Err() == nil {
		servers, err := s.serverRepo.FindAll(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			s.logger.Error("failed to fetch servers for monitoring", "error", err)
			select {
			case <-ctx.Done():
			case <-s.clock.After(monitorDiscoveryRetry):
			}
			continue
		}

		live := make([]uint, 0, len(servers))
		for _, srv := range servers {
			live = append(live, srv.ID)
		}
		now := s.clock.Now()
		for _, id := range sched.sync(live, now) {
			// A deleted node's task is cancelled, never counted as a failure.
			mu.Lock()
			cancel := cancels[id]
			delete(cancels, id)
			mu.Unlock()
			if cancel != nil {
				cancel()
			}
		}

		tick := sched.plan(now)
		for _, id := range tick.Admitted {
			// The 30s budget starts at admission and covers the whole collection:
			// quota wait, probe, deployment, execution, parsing and the write.
			taskCtx, cancel := context.WithTimeout(ctx, s.taskBudget)
			mu.Lock()
			cancels[id] = cancel
			mu.Unlock()
			wg.Add(1)
			go func(id uint) {
				defer wg.Done()
				defer cancel()
				outcome := s.runTask(taskCtx, id)
				s.reportCompletion(id, sched.complete(id, outcome, s.clock.Now()))
				mu.Lock()
				delete(cancels, id)
				mu.Unlock()
				select {
				case notify <- struct{}{}:
				default:
				}
			}(id)
		}
		s.reportTick(tick)

		// Wake at the next due slot, but never later than one interval so newly
		// added nodes are discovered promptly.
		delay := s.interval
		if wait := sched.earliestDue(s.clock.Now()); wait.IsZero() {
			delay = 0
		} else if until := wait.Sub(now); until <= 0 {
			delay = 0
		} else if until < s.interval {
			delay = until
		}
		select {
		case <-ctx.Done():
		case <-notify:
		case <-s.wake:
		case <-s.clock.After(delay):
		}
	}

	mu.Lock()
	for _, cancel := range cancels {
		cancel()
	}
	mu.Unlock()
	wg.Wait()
	s.logger.Info("monitor service stopped")
}

// NotifyTopologyChange refreshes the managed node list immediately, so a deleted
// node's task is cancelled and a new node is collected without waiting for the
// next interval. It never blocks.
func (s *MonitorService) NotifyTopologyChange() {
	if s.wake == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// runTask performs one collection and classifies its outcome, so only real
// failures advance the node's backoff.
func (s *MonitorService) runTask(ctx context.Context, serverID uint) (outcome monitorOutcome) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("monitor goroutine panicked", "server_id", serverID, "panic", r)
			outcome = outcomeFailure
		}
	}()
	err := s.collectTask(ctx, serverID)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		s.logger.Error("metrics collection exceeded its deadline", "server_id", serverID)
		return outcomeFailure
	case ctx.Err() != nil:
		// Application shutdown, node deletion or generation invalidation.
		return outcomeCancelled
	case err != nil:
		s.logger.Error("failed to collect metrics", "server_id", serverID, "error", err)
		return outcomeFailure
	}
	return outcomeSuccess
}

// SetAgentDeployer wires the agent deployment service that owns probing, upload
// and protocol verification.
func (s *MonitorService) SetAgentDeployer(deployer *AgentDeploymentService) {
	s.deployer = deployer
}

// collectTask is the injectable collection step.
func (s *MonitorService) collectTask(ctx context.Context, serverID uint) error {
	if s.taskFn != nil {
		return s.taskFn(ctx, serverID)
	}
	return s.collectOne(ctx, serverID)
}

// reportTick records deferred admissions (missed or late samples) and escalates
// once per streak, as required by REQUIREMENTS §12.3. Node identity stays in the
// log for correlation and is never a metric label.
func (s *MonitorService) reportTick(tick monitorTick) {
	if len(tick.Queued) > 0 || len(tick.Skipped) > 0 {
		s.logger.Info("monitor admission deferred",
			"action", "monitor.collect", "outcome", "deferred",
			"admitted", len(tick.Admitted), "queued", len(tick.Queued), "skipped", len(tick.Skipped))
	}
	for _, id := range tick.WarnDeferred {
		s.logger.Warn("monitor collection repeatedly deferred",
			"action", "monitor.collect", "outcome", "deferred", "reason_class", "capacity",
			"server_id", id, "consecutive", monitorWarnAfter)
	}
	for _, id := range tick.RecoveredDefer {
		s.logger.Info("monitor collection resumed",
			"action", "monitor.collect", "outcome", "recovered", "server_id", id)
	}
}

// reportCompletion emits exactly-once escalation and recovery events for a
// finished collection (§12.3).
func (s *MonitorService) reportCompletion(serverID uint, result monitorCompletion) {
	if result.WarnDegraded {
		s.logger.Warn("monitor collection repeatedly failing",
			"action", "monitor.collect", "outcome", "failure", "reason_class", "collection_failed",
			"server_id", serverID, "consecutive", result.Failures)
	}
	if result.Recovered {
		s.logger.Info("monitor collection recovered",
			"action", "monitor.collect", "outcome", "success", "server_id", serverID)
	}
}

func (s *MonitorService) collectOne(ctx context.Context, serverID uint) error {
	if s.deployer == nil {
		return errors.New("monitor: agent deployment service is not configured")
	}
	// One lease spans the probe, the upload and the execution so the whole
	// collection is pinned to a single target generation (§8.2 step 3).
	lease, err := s.sshSvc.AcquireLease(ctx, serverID)
	if err != nil {
		return err
	}
	defer lease.Release()

	out, err := s.deployer.Collect(ctx, lease)
	if err != nil {
		if isConnectionError(err) {
			lease.Discard()
		}
		return err
	}
	return s.metricRepo.InsertFull(ctx, s.metricFull(serverID, out))
}

// metricFull maps the agent payload onto the stored metric row.
func (s *MonitorService) metricFull(serverID uint, out *agentOutput) *repository.MetricFull {
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

	return &repository.MetricFull{
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
	}
}
