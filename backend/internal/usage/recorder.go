package usage

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
)

var ErrRecorderUnavailable = errors.New("usage recorder unavailable")
var errNoWriteSlot = errors.New("usage write capacity unavailable")

type Store interface {
	Upsert(context.Context, *model.UsageLog) error
	RegisterInstance(context.Context, string) error
	Heartbeat(context.Context, string) error
	Reconcile(context.Context, string, []string) error
	Cleanup(context.Context) error
}
type AuditStore interface {
	Create(context.Context, *model.AuditEvent) error
}
type ownerUnknownStore interface {
	OwnerUnknown(context.Context, string) ([]string, error)
}
type reconciliationCounter interface{ ReconciledCount() uint64 }
type cleanupCounter interface{ CleanedCount() uint64 }

const (
	cleanupRoundTimeout  = 2 * time.Second
	cleanupRoundBatches  = 100
	cleanupRetryInterval = time.Second
)

type Options struct {
	DBMaxOpenConnections int
	WriteConcurrency     int
	WriteTimeout         time.Duration
	ActiveLimit          int
	RetryItems           int
	RetryBytes           int
	RetryLifetime        time.Duration
	AuditRetryItems      int
	AuditRetryBytes      int
	HeartbeatInterval    time.Duration
	LeaseDuration        time.Duration
	ReconcileInterval    time.Duration
	CleanupInterval      time.Duration
	IPLimit              int
	IPTTL                time.Duration
	AuthenticatedRate    float64
	AuthenticatedBurst   float64
	UnauthenticatedRate  float64
	UnauthenticatedBurst float64
	IPRate               float64
	IPBurst              float64
	OrdinaryRetention    time.Duration
	SensitiveRetention   time.Duration
}

func (o Options) defaults() Options {
	if o.DBMaxOpenConnections <= 0 {
		o.DBMaxOpenConnections = 32
	}
	if o.WriteConcurrency <= 0 {
		o.WriteConcurrency = 4
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 200 * time.Millisecond
	}
	if o.ActiveLimit <= 0 {
		o.ActiveLimit = 4096
	}
	if o.RetryItems <= 0 {
		o.RetryItems = 1024
	}
	if o.RetryBytes <= 0 {
		o.RetryBytes = 8 << 20
	}
	if o.RetryLifetime <= 0 {
		o.RetryLifetime = 2 * time.Minute
	}
	if o.AuditRetryItems <= 0 {
		o.AuditRetryItems = 256
	}
	if o.AuditRetryBytes <= 0 {
		o.AuditRetryBytes = 1 << 20
	}
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = 10 * time.Second
	}
	if o.LeaseDuration <= 0 {
		o.LeaseDuration = 60 * time.Second
	}
	if o.ReconcileInterval <= 0 {
		o.ReconcileInterval = 30 * time.Second
	}
	if o.CleanupInterval <= 0 {
		o.CleanupInterval = time.Minute
	}
	if o.IPLimit <= 0 {
		o.IPLimit = 4096
	}
	if o.IPTTL <= 0 {
		o.IPTTL = 10 * time.Minute
	}
	if o.AuthenticatedRate <= 0 {
		o.AuthenticatedRate = 50
	}
	if o.AuthenticatedBurst <= 0 {
		o.AuthenticatedBurst = 100
	}
	if o.UnauthenticatedRate <= 0 {
		o.UnauthenticatedRate = 10
	}
	if o.UnauthenticatedBurst <= 0 {
		o.UnauthenticatedBurst = 20
	}
	if o.IPRate <= 0 {
		o.IPRate = 5.0 / 60
	}
	if o.IPBurst <= 0 {
		o.IPBurst = 5
	}
	if o.OrdinaryRetention <= 0 {
		o.OrdinaryRetention = 30 * 24 * time.Hour
	}
	if o.SensitiveRetention <= 0 {
		o.SensitiveRetention = 90 * 24 * time.Hour
	}
	return o
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func (b *tokenBucket) allow(now time.Time, rate, burst float64) bool {
	if b.last.IsZero() {
		b.tokens = burst
	} else {
		b.tokens = min(burst, b.tokens+max(0, now.Sub(b.last).Seconds())*rate)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type ipBudget struct {
	bucket tokenBucket
	seen   time.Time
}
type pendingUsage struct {
	row             model.UsageLog
	born, next      time.Time
	bytes, attempts int
	inFlight        bool
	completion      bool
}
type pendingAudit struct {
	event           model.AuditEvent
	born, next      time.Time
	bytes, attempts int
	inFlight        bool
}

// Stats contains only low-cardinality counters and bounded queue gauges.
type Stats struct {
	CaptureSkipped    uint64 `json:"usage_capture_skipped_total"`
	WriteFailed       uint64 `json:"usage_write_failed_total"`
	FinalizationRetry uint64 `json:"usage_finalization_retry_total"`
	Reconciled        uint64 `json:"usage_reconciled_total"`
	AuditWriteFailed  uint64 `json:"audit_write_failed_total"`
	AuditLost         uint64 `json:"audit_lost_total"`
	FinalizationLost  uint64 `json:"usage_finalization_lost_total"`
	PhaseSkipped      uint64 `json:"usage_phase_skipped_total"`
	Active            int    `json:"active"`
	Pending           int    `json:"pending"`
	PendingBytes      int    `json:"pending_bytes"`
	AuditPending      int    `json:"audit_pending"`
	AuditPendingBytes int    `json:"audit_pending_bytes"`
	OldestPendingMS   int64  `json:"oldest_pending_ms"`
}

type Recorder struct {
	store                                                                          Store
	audit                                                                          AuditStore
	opts                                                                           Options
	mu                                                                             sync.Mutex
	owner                                                                          string
	leaseConfirmedUntil                                                            time.Time
	active                                                                         map[string]*Operation
	pending                                                                        map[string]*pendingUsage
	auditPending                                                                   map[string]*pendingAudit
	pendingBytes, auditBytes                                                       int
	ips                                                                            map[string]*ipBudget
	authBudget, unauthBudget                                                       tokenBucket
	stats                                                                          Stats
	lastWarning                                                                    map[string]time.Time
	phaseQueued                                                                    map[string]bool
	allSlots, writeSlots, ordinarySlots, heartbeatSlot, maintenanceSlot, auditSlot chan struct{}
	phases                                                                         chan string
	startOnce, closeOnce                                                           sync.Once
	cancel                                                                         context.CancelFunc
	wg                                                                             sync.WaitGroup
}

func NewRecorder(store Store, audit AuditStore, options Options) *Recorder {
	o := options.defaults()
	allCapacity := max(0, o.DBMaxOpenConnections/4)
	regular := min(o.WriteConcurrency, max(1, allCapacity-2))
	r := &Recorder{store: store, audit: audit, opts: o, owner: newUUID(), active: map[string]*Operation{}, pending: map[string]*pendingUsage{}, auditPending: map[string]*pendingAudit{}, ips: map[string]*ipBudget{}, lastWarning: map[string]time.Time{}, phaseQueued: map[string]bool{}, allSlots: make(chan struct{}, allCapacity), writeSlots: make(chan struct{}, regular), ordinarySlots: make(chan struct{}, max(1, regular-1)), heartbeatSlot: make(chan struct{}, 1), maintenanceSlot: make(chan struct{}, 1), auditSlot: make(chan struct{}, 1), phases: make(chan string, o.ActiveLimit)}
	return r
}
func (r *Recorder) NewOperation(action, route, requestID, method, client string) *Operation {
	op := NewOperation(action, route, requestID, method, client)
	op.recorder = r
	return op
}

// Start confirms a new owner before any long operation can create a running
// row. Initial registration failure never prevents the application starting.
func (r *Recorder) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.startOnce.Do(func() {
		runctx, cancel := context.WithCancel(ctx)
		r.cancel = cancel
		r.registerOwner()
		r.wg.Add(3)
		go r.retryLoop(runctx)
		go r.heartbeatLoop(runctx)
		go r.maintenanceLoop(runctx)
	})
}
func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		r.wg.Wait()
	})
}

// AdmitShort decides authenticated new-operation capture when identity is
// verified, before business work. This decision only consumes memory budgets;
// independent audit writes and the operation's business result are unaffected.
func (r *Recorder) AdmitShort(op *Operation) {
	if r == nil || op == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.finished || op.captureDecided || op.beginAttempted || op.row.AuthType == "unauthenticated" || longAction(op.row.Action) {
		return
	}
	op.captureDecided = true
	op.admitted = r.store != nil && r.admitLocked(op.row.AuthType, op.row.ClientAddress)
	if !op.admitted {
		r.stats.CaptureSkipped++
	}
}

func longAction(action string) bool {
	return action == "server.exec" || action == "service.relay" || action == "server.terminal"
}

func (r *Recorder) Begin(op *Operation) bool {
	if r == nil || op == nil {
		return false
	}
	op.mu.Lock()
	if op.finished {
		op.mu.Unlock()
		return false
	}
	if op.beginAttempted {
		accepted := op.admitted
		op.mu.Unlock()
		return accepted
	}
	op.beginAttempted = true
	op.mu.Unlock()
	r.mu.Lock()
	op.mu.Lock()
	// Finish may have won the operation lock while admission waited for the
	// recorder. Never add that terminal operation to the active table.
	if op.finished {
		op.mu.Unlock()
		r.mu.Unlock()
		return false
	}
	p := op.principal
	if p.AuthType == "unauthenticated" || !time.Now().Before(r.leaseConfirmedUntil) || r.store == nil || len(r.active) >= r.opts.ActiveLimit || (op.captureDecided && !op.admitted) || (!op.captureDecided && !r.admitLocked(p.AuthType, op.row.ClientAddress)) {
		r.stats.CaptureSkipped++
		op.mu.Unlock()
		r.mu.Unlock()
		return false
	}
	owner := r.owner
	op.admitted = true
	op.row.OwnerInstanceID = &owner
	op.row.StateSeq++
	row := op.snapshotLocked()
	op.mu.Unlock()
	r.active[row.OperationID] = op
	r.mu.Unlock()
	if err := r.writeUsage(row, false); err != nil {
		r.writeFailure(row, "start_write_failed", 0)
	}
	return true
}

// Finish must run after business connections have been released. The active
// entry remains tracked throughout the synchronous write and atomic transfer
// to the retry queue, including all in-flight retry attempts.
func (r *Recorder) Finish(op *Operation, status int) {
	if r == nil || op == nil {
		return
	}
	row, fresh := op.finish(status)
	if !fresh {
		return
	}
	r.mu.Lock()
	op.mu.Lock()
	begun, decided, admitted := op.beginAttempted, op.captureDecided, op.admitted
	op.mu.Unlock()
	if (begun || decided) && !admitted {
		r.mu.Unlock()
		return
	}
	if !begun && !decided {
		if r.store == nil || !r.admitLocked(row.AuthType, row.ClientAddress) {
			r.stats.CaptureSkipped++
			r.mu.Unlock()
			return
		}
		op.mu.Lock()
		op.captureDecided = true
		op.admitted = true
		op.mu.Unlock()
	}
	r.mu.Unlock()
	// A short operation's first INSERT is an ordinary new write. Only a
	// previously accepted long operation can use the reserved completion slot.
	completion := begun && admitted
	err := r.writeUsage(row, completion)
	if err != nil {
		r.writeFailure(row, "final_write_failed", 0)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil && !permanentOwnerError(err) && !r.snapshotExpired(row, time.Now()) {
		r.enqueueUsageLocked(row, completion)
	} else if err != nil {
		r.stats.FinalizationLost++
	}
	delete(r.active, row.OperationID)
}

func (r *Recorder) admitLocked(auth, ip string) bool {
	now := time.Now()
	if auth != "unauthenticated" {
		return r.authBudget.allow(now, r.opts.AuthenticatedRate, r.opts.AuthenticatedBurst)
	}
	if !r.unauthBudget.allow(now, r.opts.UnauthenticatedRate, r.opts.UnauthenticatedBurst) {
		return false
	}
	b := r.ips[ip]
	if b == nil {
		if len(r.ips) >= r.opts.IPLimit {
			return true
		}
		b = &ipBudget{}
		r.ips[ip] = b
	}
	b.seen = now
	return b.bucket.allow(now, r.opts.IPRate, r.opts.IPBurst)
}
func (r *Recorder) enqueueUsageLocked(row model.UsageLog, completion bool) {
	data, _ := json.Marshal(row)
	size := len(data)
	if len(r.pending) >= r.opts.RetryItems || r.pendingBytes+size > r.opts.RetryBytes {
		r.stats.FinalizationLost++
		return
	}
	now := time.Now()
	r.pending[row.OperationID] = &pendingUsage{row: row, born: now, next: now.Add(retryDelay(0)), bytes: size, completion: completion}
	r.pendingBytes += size
}

func (r *Recorder) WriteAudit(_ context.Context, event *model.AuditEvent) error {
	if r == nil || r.audit == nil || event == nil {
		return ErrRecorderUnavailable
	}
	safe := *event
	safe.OperationID = copyPtr(event.OperationID)
	if safe.OperationID == nil {
		id := newUUID()
		safe.OperationID = &id
		event.OperationID = &id
	}
	safe.Username = boundedText(safe.Username, 256)
	safe.Action = boundedText(safe.Action, 96)
	safe.ResourceType = boundedText(safe.ResourceType, 64)
	safe.IPAddress = boundedText(safe.IPAddress, 64)
	safe.Details = ""
	err := r.writeAudit(safe)
	if err == nil {
		return nil
	}
	r.mu.Lock()
	r.stats.AuditWriteFailed++
	r.warnLocked(*safe.OperationID, safe.Action, "audit_write_failed", 0)
	if _, exists := r.auditPending[*safe.OperationID]; !exists {
		data, _ := json.Marshal(safe)
		size := len(data)
		if len(r.auditPending) >= r.opts.AuditRetryItems || r.auditBytes+size > r.opts.AuditRetryBytes {
			r.stats.AuditLost++
		} else {
			now := time.Now()
			r.auditPending[*safe.OperationID] = &pendingAudit{event: safe, born: now, next: now.Add(retryDelay(0)), bytes: size}
			r.auditBytes += size
		}
	}
	r.mu.Unlock()
	return err
}

func take(ch chan struct{}) bool {
	select {
	case ch <- struct{}{}:
		return true
	default:
		return false
	}
}
func release(ch chan struct{}) { <-ch }
func (r *Recorder) writeUsage(row model.UsageLog, completion bool) error {
	if r.store == nil {
		return ErrRecorderUnavailable
	}
	if !completion {
		if !take(r.ordinarySlots) {
			return errNoWriteSlot
		}
		defer release(r.ordinarySlots)
	}
	if !take(r.writeSlots) {
		return errNoWriteSlot
	}
	defer release(r.writeSlots)
	if !take(r.allSlots) {
		return errNoWriteSlot
	}
	defer release(r.allSlots)
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.WriteTimeout)
	defer cancel()
	return r.store.Upsert(ctx, &row)
}
func (r *Recorder) writeAudit(event model.AuditEvent) error {
	if !take(r.auditSlot) {
		return errNoWriteSlot
	}
	defer release(r.auditSlot)
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.WriteTimeout)
	defer cancel()
	return r.audit.Create(ctx, &event)
}
func permanentOwnerError(err error) bool {
	return errors.Is(err, repository.ErrUsageOwnerMissing) || errors.Is(err, repository.ErrUsageOwnerRetired)
}
func (r *Recorder) snapshotExpired(row model.UsageLog, now time.Time) bool {
	if row.RetentionAt == nil {
		return false
	}
	retention := r.opts.OrdinaryRetention
	if sensitiveAction(row.Action) {
		retention = r.opts.SensitiveRetention
	}
	return now.Sub(*row.RetentionAt) >= retention
}
func sensitiveAction(action string) bool {
	switch action {
	case "credential.reveal", "api_key.reveal", "service.credentials", "server.host_key.trust", "server.delete", "credential.delete", "service.delete", "api_key.delete":
		return true
	}
	return false
}
func retryDelay(attempt int) time.Duration {
	delays := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 30 * time.Second}
	base := delays[min(attempt, len(delays)-1)]
	return time.Duration(float64(base) * (0.85 + rand.Float64()*0.3))
}
func (r *Recorder) writeFailure(row model.UsageLog, reason string, attempt int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stats.WriteFailed++
	r.warnLocked(row.OperationID, row.Action, reason, attempt)
}
func (r *Recorder) warnLocked(id, action, reason string, attempt int) {
	now := time.Now()
	if now.Sub(r.lastWarning[reason]) < time.Minute {
		return
	}
	r.lastWarning[reason] = now
	slog.Warn("operation history persistence degraded", "operation_id", id, "action", action, "reason", reason, "retry", attempt)
}

func (r *Recorder) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats
	s.Active = len(r.active)
	s.Pending = len(r.pending)
	s.PendingBytes = r.pendingBytes
	s.AuditPending = len(r.auditPending)
	s.AuditPendingBytes = r.auditBytes
	now := time.Now()
	for _, p := range r.pending {
		s.OldestPendingMS = max(s.OldestPendingMS, now.Sub(p.born).Milliseconds())
	}
	return s
}
func (r *Recorder) schedulePhase(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[id] == nil || r.phaseQueued[id] {
		return
	}
	select {
	case r.phases <- id:
		r.phaseQueued[id] = true
	default:
		r.stats.PhaseSkipped++
	}
}
func (r *Recorder) retryLoop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-r.phases:
			r.persistPhase(id)
		case <-ticker.C:
			r.retryPending()
			r.retryAudits()
		}
	}
}
func (r *Recorder) persistPhase(id string) {
	r.mu.Lock()
	delete(r.phaseQueued, id)
	op := r.active[id]
	r.mu.Unlock()
	if op == nil {
		return
	}
	op.mu.Lock()
	if op.finished {
		op.mu.Unlock()
		return
	}
	row := op.snapshotLocked()
	op.mu.Unlock()
	if err := r.writeUsage(row, false); err != nil {
		r.writeFailure(row, "phase_write_failed", 0)
	}
}
func (r *Recorder) retryPending() {
	for attempts := 0; attempts < 8; attempts++ {
		now := time.Now()
		r.mu.Lock()
		var id string
		var p *pendingUsage
		for key, item := range r.pending {
			if item.inFlight {
				continue
			}
			if now.Sub(item.born) >= r.opts.RetryLifetime || r.snapshotExpired(item.row, now) {
				delete(r.pending, key)
				r.pendingBytes -= item.bytes
				r.stats.FinalizationLost++
				continue
			}
			if !item.next.After(now) {
				id = key
				p = item
				p.inFlight = true
				break
			}
		}
		if p == nil {
			r.mu.Unlock()
			return
		}
		r.stats.FinalizationRetry++
		row := p.row
		r.mu.Unlock()
		err := r.writeUsage(row, p.completion)
		if err != nil {
			r.writeFailure(row, "final_retry_failed", p.attempts+1)
		}
		r.mu.Lock()
		if err == nil || permanentOwnerError(err) {
			delete(r.pending, id)
			r.pendingBytes -= p.bytes
			if err != nil {
				r.stats.FinalizationLost++
			}
		} else {
			p.attempts++
			p.next = time.Now().Add(retryDelay(p.attempts))
			p.inFlight = false
		}
		r.mu.Unlock()
	}
}
func (r *Recorder) retryAudits() {
	for attempts := 0; attempts < 4; attempts++ {
		now := time.Now()
		r.mu.Lock()
		var id string
		var p *pendingAudit
		for key, item := range r.auditPending {
			if item.inFlight {
				continue
			}
			if now.Sub(item.born) >= r.opts.RetryLifetime {
				delete(r.auditPending, key)
				r.auditBytes -= item.bytes
				r.stats.AuditLost++
				continue
			}
			if !item.next.After(now) {
				id = key
				p = item
				p.inFlight = true
				break
			}
		}
		if p == nil {
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		err := r.writeAudit(p.event)
		r.mu.Lock()
		if err == nil {
			delete(r.auditPending, id)
			r.auditBytes -= p.bytes
		} else {
			r.stats.AuditWriteFailed++
			p.attempts++
			r.warnLocked(id, p.event.Action, "audit_retry_failed", p.attempts)
			p.next = time.Now().Add(retryDelay(p.attempts))
			p.inFlight = false
		}
		r.mu.Unlock()
	}
}

func (r *Recorder) registerOwner() {
	if r.store == nil {
		return
	}
	r.mu.Lock()
	id := r.owner
	r.mu.Unlock()
	err := r.ownerWrite(func(ctx context.Context) error { return r.store.RegisterInstance(ctx, id) })
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil && id == r.owner {
		r.leaseConfirmedUntil = time.Now().Add(r.opts.LeaseDuration - r.opts.WriteTimeout)
	} else {
		r.warnLocked("", "", "instance_registration_failed", 0)
	}
}
func (r *Recorder) ownerWrite(fn func(context.Context) error) error {
	if !take(r.heartbeatSlot) {
		return errNoWriteSlot
	}
	defer release(r.heartbeatSlot)
	if !take(r.allSlots) {
		return errNoWriteSlot
	}
	defer release(r.allSlots)
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.WriteTimeout)
	defer cancel()
	return fn(ctx)
}
func (r *Recorder) heartbeatLoop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(r.opts.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.heartbeat()
		}
	}
}
func (r *Recorder) heartbeat() {
	if r.store == nil {
		return
	}
	r.mu.Lock()
	id := r.owner
	r.mu.Unlock()
	err := r.ownerWrite(func(ctx context.Context) error { return r.store.Heartbeat(ctx, id) })
	if permanentOwnerError(err) {
		r.mu.Lock()
		if id == r.owner {
			r.owner = newUUID()
			r.leaseConfirmedUntil = time.Time{}
		}
		r.mu.Unlock()
		r.registerOwner()
		return
	}
	if err != nil {
		r.mu.Lock()
		r.warnLocked("", "", "instance_heartbeat_failed", 0)
		r.mu.Unlock()
		return
	}
	r.mu.Lock()
	if id == r.owner {
		r.leaseConfirmedUntil = time.Now().Add(r.opts.LeaseDuration - r.opts.WriteTimeout)
	}
	r.mu.Unlock()
	r.restoreUnknown(id)
}
func (r *Recorder) restoreUnknown(owner string) {
	store, ok := r.store.(ownerUnknownStore)
	if !ok {
		return
	}
	var ids []string
	err := r.maintenanceWrite(func(ctx context.Context) error { var e error; ids, e = store.OwnerUnknown(ctx, owner); return e })
	if err != nil {
		return
	}
	for _, id := range ids {
		r.mu.Lock()
		op := r.active[id]
		r.mu.Unlock()
		if op == nil {
			continue
		}
		row, ok := op.refreshRunning()
		if !ok {
			continue
		}
		if err := r.writeUsage(row, false); err != nil {
			r.writeFailure(row, "active_restore_failed", 0)
		}
	}
}
func (r *Recorder) maintenanceWrite(fn func(context.Context) error) error {
	return r.maintenanceWriteContext(context.Background(), fn)
}
func (r *Recorder) maintenanceWriteContext(parent context.Context, fn func(context.Context) error) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if !take(r.maintenanceSlot) {
		return errNoWriteSlot
	}
	defer release(r.maintenanceSlot)
	if !take(r.allSlots) {
		return errNoWriteSlot
	}
	defer release(r.allSlots)
	ctx, cancel := context.WithTimeout(parent, r.opts.WriteTimeout)
	defer cancel()
	return fn(ctx)
}
func (r *Recorder) maintenanceLoop(ctx context.Context) {
	defer r.wg.Done()
	reconcile := time.NewTicker(r.opts.ReconcileInterval)
	cleanup := time.NewTimer(r.opts.CleanupInterval)
	defer reconcile.Stop()
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcile.C:
			r.reconcile()
		case <-cleanup.C:
			interval := r.opts.CleanupInterval
			if r.drainCleanup(ctx, reconcile.C) {
				interval = min(interval, cleanupRetryInterval)
			}
			cleanup.Reset(interval)
		}
	}
}

// Cleanup commits small batches. Keep draining while the store reports
// progress, but bound each round and yield to due reconciliation between
// batches. A deadline may follow already committed deletions, so it still
// needs a short follow-up when the counter advanced.
func (r *Recorder) drainCleanup(parent context.Context, reconcile <-chan time.Time) bool {
	if r.store == nil || parent.Err() != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(parent, cleanupRoundTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	counter, counted := r.store.(cleanupCounter)
	anyProgress := false
	for batch := 0; batch < cleanupRoundBatches; batch++ {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return parent.Err() == nil && anyProgress
		}
		select {
		case <-reconcile:
			r.reconcileContext(ctx)
		default:
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return parent.Err() == nil && anyProgress
		}
		var before uint64
		if counted {
			before = counter.CleanedCount()
		}
		err := r.maintenanceWriteContext(ctx, r.store.Cleanup)
		progress := counted && counter.CleanedCount() > before
		anyProgress = anyProgress || progress
		if parent.Err() != nil {
			return false
		}
		if err != nil {
			r.mu.Lock()
			r.warnLocked("", "", "usage_cleanup_failed", 0)
			r.mu.Unlock()
			return errors.Is(err, context.DeadlineExceeded) && (progress || (anyProgress && errors.Is(ctx.Err(), context.DeadlineExceeded)))
		}
		if !progress {
			return false
		}
	}
	return true
}

func (r *Recorder) reconcile() {
	r.reconcileContext(context.Background())
}
func (r *Recorder) reconcileContext(ctx context.Context) {
	if r.store == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for ip, b := range r.ips {
		if now.Sub(b.seen) >= r.opts.IPTTL {
			delete(r.ips, ip)
		}
	}
	tracked := make([]string, 0, len(r.active)+len(r.pending))
	for id := range r.active {
		tracked = append(tracked, id)
	}
	for id := range r.pending {
		tracked = append(tracked, id)
	}
	err := r.maintenanceWriteContext(ctx, func(ctx context.Context) error { return r.store.Reconcile(ctx, r.owner, tracked) })
	if err != nil {
		r.warnLocked("", "", "usage_reconcile_failed", 0)
	}
	if counter, ok := r.store.(reconciliationCounter); ok {
		r.stats.Reconciled = counter.ReconciledCount()
	}
}
