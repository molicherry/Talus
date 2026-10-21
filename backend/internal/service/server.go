package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	"gorm.io/gorm"
)

// ConnectionInvalidator drops cached SSH connections for a server. Implemented
// by *sshpool.Pool; a nil value is tolerated (the pool is optional in tests).
type ConnectionInvalidator interface {
	Invalidate(serverID uint)
}

// ServerService provides business logic for server management.
type ServerService struct {
	// freshness is the derived online/expired cutoff (0 = default).
	freshness time.Duration
	// topology notifier receives create/delete so the monitor refreshes
	// the managed node list instead of waiting for its next interval.
	notifier   topologyNotifier
	repo       *repository.ServerRepo
	metricRepo *repository.MetricRepo
	pool       ConnectionInvalidator
	masterKey  *crypto.MasterKey
	monitoring interface {
		Statuses(context.Context, []uint) map[uint]*model.MonitoringStatus
	}
	// deletion phase budgets; zero uses the package defaults.
	deletePrepareBudget time.Duration
	deleteCommitBudget  time.Duration
	deleteCleanupBudget time.Duration
}

// NewServerService creates a ServerService with the given repositories.
func NewServerService(repo *repository.ServerRepo, metricRepo *repository.MetricRepo, pool ConnectionInvalidator) *ServerService {
	return &ServerService{repo: repo, metricRepo: metricRepo, pool: pool}
}

// SetMasterKey enables encryption of per-server SOCKS5 credentials.
func (s *ServerService) SetMasterKey(key *crypto.MasterKey) { s.masterKey = key }

func (s *ServerService) SetMonitoringProvider(p interface {
	Statuses(context.Context, []uint) map[uint]*model.MonitoringStatus
}) {
	s.monitoring = p
}

func monitoringStatus(v *model.MonitoringStatus) string {
	if v == nil {
		return "unknown"
	}
	switch v.Connection {
	case "online":
		return "online"
	case "unreachable":
		return "offline"
	case "reconnecting":
		return "checking"
	}
	return "unknown"
}

func (s *ServerService) decorateMonitoring(ctx context.Context, servers []model.Server) {
	if s.monitoring == nil {
		return
	}
	ids := make([]uint, len(servers))
	for i := range servers {
		ids[i] = servers[i].ID
	}
	states := s.monitoring.Statuses(ctx, ids)
	for i := range servers {
		v := states[servers[i].ID]
		servers[i].Monitoring = v
		servers[i].Status = monitoringStatus(v)
		servers[i].LastSeen = nil
		if v != nil && v.LastContact != nil {
			seen := v.LastContact.UTC().Format(time.RFC3339)
			servers[i].LastSeen = &seen
		}
	}
}

// defaultFreshnessThreshold keeps the historical behaviour (2× the default
// monitor interval) when no derived threshold is configured.
const defaultFreshnessThreshold = 120 * time.Second

// freshnessThreshold reports the configured online/expired cutoff. It is derived
// from the monitor interval, the tolerated rounds and the allowed latency
// instead of a fixed 120 seconds.
func (s *ServerService) freshnessThreshold() time.Duration {
	if s.freshness > 0 {
		return s.freshness
	}
	return defaultFreshnessThreshold
}

// List returns all servers enriched with status and latest metrics.
func (s *ServerService) List(ctx context.Context) ([]model.Server, error) {
	servers, err := s.repo.FindAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	defer s.decorateMonitoring(ctx, servers)
	for i := range servers {
		decorateHostKey(&servers[i])
	}
	if err := s.decorateConnectionModes(ctx, servers); err != nil {
		return nil, err
	}

	if len(servers) == 0 {
		return servers, nil
	}

	// Collect server IDs for batch metric lookup.
	ids := make([]uint, len(servers))
	for i, srv := range servers {
		ids[i] = srv.ID
	}

	latest, err := s.metricRepo.FindLatestByServerIDs(ctx, ids)
	if err != nil {
		slog.Error("failed to fetch latest metrics, showing servers as unknown",
			"error", err,
		)
		for i := range servers {
			servers[i].Status = "unknown"
		}
		return servers, nil
	}

	now := time.Now().UTC()
	for i := range servers {
		row, ok := latest[servers[i].ID]
		if !ok {
			servers[i].Status = "unknown"
			continue
		}

		seen := row.Time.Format(time.RFC3339)
		servers[i].LastSeen = &seen

		if now.Sub(row.Time) <= s.freshnessThreshold() {
			servers[i].Status = "online"
		} else {
			servers[i].Status = "offline"
		}

		servers[i].LatestMetrics = &model.LatestMetrics{
			CPUPercent:    row.CPUPercent,
			MemoryPercent: row.MemoryPercent,
			DiskPercent:   row.DiskPercent,
		}
		servers[i].OS = row.OS
		servers[i].CPUModel = row.CPUModel
		servers[i].UptimeSeconds = row.UptimeSeconds
	}

	return servers, nil
}

// Get returns a single server by id, enriched with status and latest metrics.
func (s *ServerService) Get(ctx context.Context, id uint) (*model.Server, error) {
	srv, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("server %d: %w", id, server.ErrNotFound)
		}
		return nil, fmt.Errorf("server %d: %w", id, err)
	}
	decorateHostKey(srv)
	defer func() {
		if s.monitoring != nil {
			rows := []model.Server{*srv}
			s.decorateMonitoring(ctx, rows)
			*srv = rows[0]
		}
	}()
	if err := s.decorateConnection(ctx, srv, true); err != nil {
		return nil, err
	}

	latest, err := s.metricRepo.FindLatestByServerIDs(ctx, []uint{id})
	if err != nil {
		slog.Error("failed to fetch latest metrics for server, showing as unknown",
			"server_id", id,
			"error", err,
		)
		srv.Status = "unknown"
		return srv, nil
	}

	row, ok := latest[id]
	if !ok {
		srv.Status = "unknown"
		return srv, nil
	}

	now := time.Now().UTC()
	seen := row.Time.Format(time.RFC3339)
	srv.LastSeen = &seen

	if now.Sub(row.Time) <= s.freshnessThreshold() {
		srv.Status = "online"
	} else {
		srv.Status = "offline"
	}

	srv.LatestMetrics = &model.LatestMetrics{
		CPUPercent:    row.CPUPercent,
		MemoryPercent: row.MemoryPercent,
		DiskPercent:   row.DiskPercent,
	}
	srv.OS = row.OS
	srv.CPUModel = row.CPUModel
	srv.UptimeSeconds = row.UptimeSeconds

	return srv, nil
}

// ListFiltered returns servers scoped to the given serverIDs. When serverIDs
// is empty, it returns all servers (full access).
func (s *ServerService) ListFiltered(ctx context.Context, serverIDs []uint) ([]model.Server, error) {
	if len(serverIDs) == 0 {
		return s.List(ctx)
	}

	servers, err := s.repo.FindByIDs(ctx, serverIDs)
	if err != nil {
		return nil, fmt.Errorf("list filtered servers: %w", err)
	}
	defer s.decorateMonitoring(ctx, servers)
	for i := range servers {
		decorateHostKey(&servers[i])
	}
	if err := s.decorateConnectionModes(ctx, servers); err != nil {
		return nil, err
	}

	if len(servers) == 0 {
		return servers, nil
	}

	ids := make([]uint, len(servers))
	for i, srv := range servers {
		ids[i] = srv.ID
	}

	latest, err := s.metricRepo.FindLatestByServerIDs(ctx, ids)
	if err != nil {
		slog.Error("failed to fetch latest metrics, showing servers as unknown",
			"error", err,
		)
		for i := range servers {
			servers[i].Status = "unknown"
		}
		return servers, nil
	}

	now := time.Now().UTC()
	for i := range servers {
		row, ok := latest[servers[i].ID]
		if !ok {
			servers[i].Status = "unknown"
			continue
		}

		seen := row.Time.Format(time.RFC3339)
		servers[i].LastSeen = &seen

		if now.Sub(row.Time) <= s.freshnessThreshold() {
			servers[i].Status = "online"
		} else {
			servers[i].Status = "offline"
		}

		servers[i].LatestMetrics = &model.LatestMetrics{
			CPUPercent:    row.CPUPercent,
			MemoryPercent: row.MemoryPercent,
			DiskPercent:   row.DiskPercent,
		}
		servers[i].OS = row.OS
		servers[i].CPUModel = row.CPUModel
		servers[i].UptimeSeconds = row.UptimeSeconds
	}

	return servers, nil
}

// ListSummaries returns all servers as lightweight summaries (id, name,
// description, host, credential_id, status).
func (s *ServerService) ListSummaries(ctx context.Context) ([]model.ServerSummary, error) {
	summaries, err := s.repo.FindAllSummaries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list server summaries: %w", err)
	}
	return s.applyStatus(ctx, summaries)
}

// ListSummariesFiltered returns summaries scoped to the given serverIDs.
func (s *ServerService) ListSummariesFiltered(ctx context.Context, serverIDs []uint) ([]model.ServerSummary, error) {
	if len(serverIDs) == 0 {
		return s.ListSummaries(ctx)
	}

	summaries, err := s.repo.FindByIDSummaries(ctx, serverIDs)
	if err != nil {
		return nil, fmt.Errorf("list filtered server summaries: %w", err)
	}
	return s.applyStatus(ctx, summaries)
}

// applyStatus computes the online/offline/unknown status for summaries from
// the timestamp of each server's most recent metric, without transferring the
// full metric payload.
func (s *ServerService) applyStatus(ctx context.Context, summaries []model.ServerSummary) ([]model.ServerSummary, error) {
	defer func() {
		if s.monitoring != nil {
			ids := make([]uint, len(summaries))
			for i := range summaries {
				ids[i] = summaries[i].ID
			}
			states := s.monitoring.Statuses(ctx, ids)
			for i := range summaries {
				v := states[summaries[i].ID]
				summaries[i].Monitoring = v
				summaries[i].Status = monitoringStatus(v)
			}
		}
	}()
	if len(summaries) == 0 {
		return summaries, nil
	}
	for i := range summaries {
		summaries[i].HostKeyUnverified = summaries[i].HostKey == nil || len(*summaries[i].HostKey) == 0
		summaries[i].HostKeyMismatch = !summaries[i].HostKeyUnverified && summaries[i].HostKeyMismatchAt != nil
	}
	idsForModes := make([]uint, len(summaries))
	for i := range summaries {
		idsForModes[i] = summaries[i].ID
	}
	modes, err := s.repo.FindConnectionModes(ctx, idsForModes)
	if err != nil {
		return nil, err
	}
	for i := range summaries {
		mode := modes[summaries[i].ID]
		if mode == "" {
			mode = model.ConnectionDirect
		}
		summaries[i].Connection = &model.ConnectionConfig{Mode: mode}
	}

	ids := make([]uint, len(summaries))
	for i, srv := range summaries {
		ids[i] = srv.ID
	}

	latest, err := s.metricRepo.FindLatestTimesByServerIDs(ctx, ids)
	if err != nil {
		slog.Error("failed to fetch latest metric times, showing servers as unknown",
			"error", err,
		)
		for i := range summaries {
			summaries[i].Status = "unknown"
		}
		return summaries, nil
	}

	now := time.Now().UTC()
	for i := range summaries {
		t, ok := latest[summaries[i].ID]
		if !ok {
			summaries[i].Status = "unknown"
			continue
		}
		if now.Sub(t) <= s.freshnessThreshold() {
			summaries[i].Status = "online"
		} else {
			summaries[i].Status = "offline"
		}
	}
	return summaries, nil
}

// SetFreshnessThreshold configures the online/expired cutoff derived from the
// monitor interval, tolerated rounds and allowed latency.
func (s *ServerService) SetFreshnessThreshold(d time.Duration) {
	s.freshness = d
}

// topologyNotifier is implemented by the monitor scheduler.
type topologyNotifier interface{ NotifyTopologyChange() }

// preparedAgentRemoval holds the installation fence and a pinned SSH lease.
// Remove may only run after the server deletion commits; Close also handles
// the rejected-delete path without changing the remote agent or its credential.
type preparedAgentRemoval interface {
	Remove(context.Context) error
	Close()
	// Owned reports whether this attempt owns the fence and the pinned
	// connection. A request that only joined an in-flight deletion must not
	// invalidate the pool beneath that deletion's uninstall.
	Owned() bool
}

type agentRemover interface {
	PrepareAgentRemoval(context.Context, uint) preparedAgentRemoval
}

// SetTopologyNotifier wires the monitor so node create/delete is observed
// immediately. It is optional: without it the monitor still converges on its
// next interval.
func (s *ServerService) SetTopologyNotifier(n topologyNotifier) {
	s.notifier = n
}

func (s *ServerService) notifyTopology() {
	if s.notifier != nil {
		s.notifier.NotifyTopologyChange()
	}
}

// Create validates and inserts a server and its route in one transaction.
func (s *ServerService) Create(ctx context.Context, server *model.Server, connection ...ConnectionMutation) (*model.Server, error) {
	created, err := s.createServer(ctx, server, firstConnectionMutation(connection))
	if err != nil {
		return nil, err
	}
	s.notifyTopology()
	return created, nil
}

// Update applies partial updates to an existing server.
// Only non-zero fields from input are applied.
// clearCredential distinguishes an explicit "credential_id": null (unbind)
// from an absent field (leave the binding unchanged).
func (s *ServerService) Update(ctx context.Context, id uint, input *model.Server, clearCredential bool, connection ...ConnectionMutation) (*model.Server, error) {
	v, err := s.updateServer(ctx, id, input, clearCredential, firstConnectionMutation(connection))
	if err == nil {
		s.notifyTopology()
	}
	return v, err
}

// resolveCredentialBinding applies a partial credential-binding update: an
// explicit clear wins, then an explicit value, otherwise the current binding is
// kept. It exists so "absent" and "null" cannot collapse into one case.
func resolveCredentialBinding(current, input *uint, clear bool) *uint {
	if clear {
		return nil
	}
	if input != nil {
		return input
	}
	return current
}

// Budgets for the three phases of a deletion that may touch the remote agent.
// They add up to ServerDeleteWriteBudget in the middleware; each phase gets its
// own budget so a slow SSH prepare cannot starve the uninstall that follows it.
const (
	agentRemovalPrepareBudget = 20 * time.Second
	serverDeleteBudget        = 10 * time.Second
	agentRemovalCleanupBudget = 20 * time.Second
)

// SetDeletionBudgets overrides the three deletion phase budgets. Zero keeps the
// package default; tests shorten them to exercise concurrent deletions without
// waiting out the production budgets.
func (s *ServerService) SetDeletionBudgets(prepare, commit, cleanup time.Duration) {
	if prepare > 0 {
		s.deletePrepareBudget = prepare
	}
	if commit > 0 {
		s.deleteCommitBudget = commit
	}
	if cleanup > 0 {
		s.deleteCleanupBudget = cleanup
	}
}

func deletionPhaseBudget(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

// Delete commits the server deletion before best-effort agent cleanup. Prepare
// pins the live route first; pool invalidation waits until that cleanup ends.
//
// A concurrent deletion must not commit without owning the removal fence. If
// the owner already committed, the repeated deletion is an idempotent success;
// otherwise it returns a conflict so a failed owner cannot leave a server
// deleted without anyone responsible for uninstalling its agent.
func (s *ServerService) Delete(ctx context.Context, id uint) error {
	prepareCtx, cancel := context.WithTimeout(ctx, deletionPhaseBudget(s.deletePrepareBudget, agentRemovalPrepareBudget))
	defer cancel()
	var removal preparedAgentRemoval
	// ownsCleanup starts true for the no-monitor path; a prepared removal
	// reports whether this request actually owns the remote cleanup.
	ownsCleanup := true
	deleted := false
	defer func() {
		if removal != nil {
			removal.Close()
		}
		if deleted && ownsCleanup {
			s.invalidate(id)
		}
	}()
	if remover, ok := s.monitoring.(agentRemover); ok {
		removal = remover.PrepareAgentRemoval(prepareCtx, id)
		if removal != nil {
			ownsCleanup = removal.Owned()
		}
	}
	deleteCtx, cancelDelete := context.WithTimeout(ctx, deletionPhaseBudget(s.deleteCommitBudget, serverDeleteBudget))
	if !ownsCleanup {
		_, err := s.repo.FindByID(deleteCtx, id)
		cancelDelete()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return server.ErrConflict
	}
	err := s.deleteServer(deleteCtx, id)
	cancelDelete()
	if err != nil {
		return err
	}
	deleted = true
	if p, ok := s.monitoring.(interface{ Disconnect(uint) }); ok {
		p.Disconnect(id)
	}
	if removal != nil && ownsCleanup {
		// A committed deletion must finish cleanup even if the browser leaves.
		// Cleanup gets its own budget: prepare may have consumed all of its own.
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), deletionPhaseBudget(s.deleteCleanupBudget, agentRemovalCleanupBudget))
		if err := removal.Remove(cleanupCtx); err != nil {
			// SSH error strings can carry credential material; log only the class.
			slog.Warn("resident agent removal failed", "server_id", id, "reason", deployFailureReason(err))
		}
		cancelCleanup()
	}
	s.notifyTopology()
	return nil
}

func (s *ServerService) invalidate(serverID uint) {
	if s.pool != nil {
		s.pool.Invalidate(serverID)
	}
}

// TrustHostKey pins a first-use or changed host key after explicit verification.
//
// fingerprint is the exact fingerprint the operator verified. It must still
// match the recorded pending key: the background monitor may have recorded a
// newer one since the UI rendered, and trusting that by accident would defeat
// the point of asking the user to verify. A stale request gets 409.
func (s *ServerService) TrustHostKey(ctx context.Context, id uint, fingerprint string, revision ...uint64) (*model.Server, error) {
	return s.trustHostKey(ctx, id, fingerprint, revision...)
}

// decorateHostKey fills the transient host-key fields a client needs to show a
// first-use or changed host key: trust state and fingerprints for comparison.
func decorateHostKey(srv *model.Server) {
	srv.HostKeyUnverified = !hasTrustedHostKey(srv)
	srv.HostKeyMismatch = !srv.HostKeyUnverified && srv.HostKeyMismatchAt != nil
	srv.HostKeyFingerprint, srv.HostKeySeenFingerprint = nil, nil
	if !srv.HostKeyUnverified {
		fp := sshpool.Fingerprint(*srv.HostKey)
		srv.HostKeyFingerprint = &fp
	}
	if srv.HostKeySeen != nil {
		fp := sshpool.Fingerprint(*srv.HostKeySeen)
		srv.HostKeySeenFingerprint = &fp
	}
}
