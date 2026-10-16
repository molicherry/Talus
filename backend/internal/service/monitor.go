package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/telemetry"
)

// agentOutput mirrors the JSON output of the monitoring agent binary, including
// the version contract the Hub verifies before storing any metric.
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
	// taskBudget is the per-collection deadline starting at admission.
	taskBudget time.Duration
	// deployer owns probing, uploading and protocol verification.
	deployer *AgentDeploymentService
	// taskFn replaces the collection step in tests; production uses collectOne.
	taskFn func(ctx context.Context, serverID uint) error
	// wake asks the scheduler to re-read the managed node list immediately.
	wake chan struct{}
	// telemetry records the bounded operation/stage/lag vocabulary.
	telemetry *monitorTelemetry
	// leaseSettleBudget bounds one settlement wait in tests. It is zero in
	// production, where the wait consumes the lease's own absolute deadline - the
	// single 3s budget defined by the SSH resource layer (N03).
	leaseSettleBudget time.Duration
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
		telemetry:  newMonitorTelemetry(nil, nil),
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
			startedAt := s.clock.Now()
			// The 30s budget starts at admission and covers the whole collection:
			// quota wait, probe, deployment, execution, parsing and the write.
			taskCtx, cancel := context.WithTimeout(ctx, s.taskBudget)
			mu.Lock()
			cancels[id] = cancel
			mu.Unlock()
			due, queuedAt := sched.admissionTiming(id)
			collectionID := s.telemetry.nextCollectionID(id)
			taskCtx = WithCollectionID(taskCtx, collectionID)
			s.telemetry.workers("monitor", 1)
			wg.Add(1)
			go func(id uint) {
				defer wg.Done()
				defer cancel()
				outcome, attemptErr := s.runTask(taskCtx, id)
				finishedAt := s.clock.Now()
				s.telemetry.workers("monitor", -1)
				s.reportCompletion(id, sched.complete(id, outcome, finishedAt))
				s.emitCollection(collectionID, id, due, queuedAt, startedAt, finishedAt, outcome, attemptErr)
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
		scheduled := s.clock.Now()
		if sched.hasAdmissible(scheduled) {
			delay = 0
		} else if wait := sched.earliestDue(scheduled); !wait.IsZero() {
			if until := wait.Sub(scheduled); until > 0 && until < s.interval {
				delay = until
			}
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
// runTask performs one collection and returns the attempt's own result. The error
// travels with the attempt (never through shared state), so concurrent collections
// cannot borrow each other's reason class (F08).
func (s *MonitorService) runTask(ctx context.Context, serverID uint) (outcome monitorOutcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("monitor goroutine panicked", "server_id", serverID, "reason_class", telReasonOther)
			outcome, err = outcomeFailure, errors.New("monitor collection panicked")
		}
	}()
	err = s.collectTask(ctx, serverID)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		s.logger.Error("metrics collection exceeded its deadline", "server_id", serverID)
		return outcomeFailure, err
	case ctx.Err() != nil:
		// Application shutdown, node deletion or generation invalidation.
		return outcomeCancelled, err
	case errors.Is(err, sshpool.ErrStaleGeneration):
		// The target changed under the lease: cancelled, never a failure.
		s.logger.Info("monitor collection cancelled by a target change", "server_id", serverID)
		return outcomeCancelled, err
	case err != nil:
		attrs := append([]slog.Attr{slog.Uint64("server_id", uint64(serverID))}, remoteFailureLogAttrs(err)...)
		s.logger.LogAttrs(ctx, slog.LevelError, "failed to collect metrics", attrs...)
		return outcomeFailure, err
	}
	return outcomeSuccess, nil
}

// SetTelemetry wires the shared telemetry registry so the background paths record
// the frozen vocabulary instead of only free-form logs.
func (s *MonitorService) SetTelemetry(registry *telemetry.Registry) {
	s.telemetry = newMonitorTelemetry(registry, s.logger)
	if s.deployer != nil {
		s.deployer.SetTelemetry(registry, s.logger)
	}
}

// SetAgentDeployer wires the agent deployment service that owns probing, upload
// and protocol verification.
func (s *MonitorService) SetAgentDeployer(deployer *AgentDeploymentService) {
	s.deployer = deployer
	s.deployer.SetTelemetry(s.telemetry.registry, s.logger)
}

// collectTask is the injectable collection step.
func (s *MonitorService) collectTask(ctx context.Context, serverID uint) error {
	if s.taskFn != nil {
		return s.taskFn(ctx, serverID)
	}
	return s.collectOne(ctx, serverID)
}

// emitCollection writes the final event of one collection attempt: the frozen
// outcome/reason pair, the schedule lag and the capacity wait. Success is an
// event too, so a healthy cycle is observable.
func (s *MonitorService) emitCollection(collectionID string, serverID uint, due, queuedAt, startedAt, finishedAt time.Time, outcome monitorOutcome, err error) {
	outcomeLabel, reasonLabel := collectionOutcome(outcome, err)
	lag := time.Duration(0)
	if !due.IsZero() && startedAt.After(due) {
		lag = startedAt.Sub(due)
	}
	wait := time.Duration(0)
	if !queuedAt.IsZero() && startedAt.After(queuedAt) {
		wait = startedAt.Sub(queuedAt)
	}
	s.telemetry.collection(collectionEvent{
		CollectionID: collectionID,
		ServerID:     serverID,
		Outcome:      outcomeLabel,
		ReasonClass:  reasonLabel,
		Duration:     finishedAt.Sub(startedAt),
		ScheduleLag:  lag,
		QueueWait:    wait,
	})
}

// reportTick records deferred admissions (missed or late samples) and escalates
// once per streak. Node identity stays in the
// log for correlation and is never a metric label.
func (s *MonitorService) reportTick(tick monitorTick) {
	// A deferred admission is a production outcome, not just a log line: count the
	// slots that were really missed with the frozen vocabulary (skipped/quota).
	for i := 0; i < tick.Deferred; i++ {
		s.telemetry.skippedCollection(telReasonQuota)
	}
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
// finished collection.
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
	// collection is pinned to a single target generation.
	lease, err := s.sshSvc.AcquireLease(ctx, serverID, LeaseConsumerAgent)
	if err != nil {
		return err
	}
	// The SSH layer owns the gauge: it starts when the pool slot is taken and ends
	// when the slot is really back, so a foreground timeout that returns while the
	// owner still holds the slot stays visible (N03).
	defer lease.Release()

	out, err := s.deployer.Collect(ctx, lease)
	if err != nil && isConnectionError(err) {
		lease.Discard()
	} else {
		lease.Release()
	}
	// Settlement is what actually returns the quota and closes a broken
	// transport. Waiting here keeps shutdown honest: the scheduler must not
	// report the task finished (and start its backoff) while a transport is still
	// closing. The wait is bounded by the frozen 3s local cleanup budget.
	if !s.awaitLeaseSettlement(lease) {
		s.logger.Warn("monitor lease settlement exceeded its cleanup budget", "server_id", serverID)
	}
	if err != nil {
		return err
	}
	return s.metricRepo.InsertFull(ctx, s.metricFull(serverID, out))
}

// awaitLeaseSettlement waits for the lease to report that its resources were
// reclaimed. The wait is not abandoned when the task context is cancelled:
// shutdown, node deletion and a collection deadline all cancel that context, and
// the resources are still being closed (F05).
//
// The wait consumes the lease's own immutable settlement deadline, which the SSH
// layer anchored when it began reclaiming the slot. A nested wait must not start a
// fresh budget: the resource layer's teardown plus this wait have to stay inside
// the single documented 3s bound (N03), and a lease that already spent it returns
// pending immediately instead of buying another 3 seconds.
func (s *MonitorService) awaitLeaseSettlement(lease Lease) bool {
	select {
	case <-lease.Done():
		return true
	default:
	}
	if s.leaseSettleBudget > 0 {
		// Tests may pin a shorter budget; the deadline still wins when it is
		// already inside that budget.
		timer := time.NewTimer(s.leaseSettleBudget)
		defer timer.Stop()
		select {
		case <-lease.Done():
			return true
		case <-timer.C:
			return false
		}
	}
	timer := time.NewTimer(max(time.Until(lease.SettlementDeadline()), 0))
	defer timer.Stop()
	select {
	case <-lease.Done():
		return true
	case <-timer.C:
		return false
	}
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
