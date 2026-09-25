package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/vpsmanager/backend/internal/model"
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
	repo       *repository.ServerRepo
	metricRepo *repository.MetricRepo
	pool       ConnectionInvalidator
}

// NewServerService creates a ServerService with the given repositories.
func NewServerService(repo *repository.ServerRepo, metricRepo *repository.MetricRepo, pool ConnectionInvalidator) *ServerService {
	return &ServerService{repo: repo, metricRepo: metricRepo, pool: pool}
}

// statusThreshold is the cutoff for considering a server "online"
// based on the age of its most recent metric. 120s = 2× default monitor interval.
const statusThreshold = 120 * time.Second

// List returns all servers enriched with status and latest metrics.
func (s *ServerService) List(ctx context.Context) ([]model.Server, error) {
	servers, err := s.repo.FindAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	for i := range servers {
		decorateHostKey(&servers[i])
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

		if now.Sub(row.Time) <= statusThreshold {
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

	if now.Sub(row.Time) <= statusThreshold {
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
	for i := range servers {
		decorateHostKey(&servers[i])
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

		if now.Sub(row.Time) <= statusThreshold {
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
	if len(summaries) == 0 {
		return summaries, nil
	}
	for i := range summaries {
		summaries[i].HostKeyMismatch = summaries[i].HostKeyMismatchAt != nil
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
		if now.Sub(t) <= statusThreshold {
			summaries[i].Status = "online"
		} else {
			summaries[i].Status = "offline"
		}
	}
	return summaries, nil
}

// Create validates and inserts a new server.
func (s *ServerService) Create(ctx context.Context, server *model.Server) (*model.Server, error) {
	if server.Name == "" {
		return nil, fmt.Errorf("create server: name is required")
	}
	if server.Host == "" {
		return nil, fmt.Errorf("create server: host is required")
	}
	if server.Port <= 0 {
		server.Port = 22
	}

	if err := s.repo.Create(ctx, server); err != nil {
		return nil, fmt.Errorf("create server: %w", err)
	}
	server.Status = "unknown"
	return server, nil
}

// Update applies partial updates to an existing server.
// Only non-zero fields from input are applied.
// clearCredential distinguishes an explicit "credential_id": null (unbind)
// from an absent field (leave the binding unchanged).
func (s *ServerService) Update(ctx context.Context, id uint, input *model.Server, clearCredential bool) (*model.Server, error) {
	existing, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("server %d: %w", id, server.ErrNotFound)
		}
		return nil, fmt.Errorf("server %d: %w", id, err)
	}

	// A different host may present a different key; the old TOFU key would
	// reject it (or worse, pin the wrong host), so drop it and re-learn. The
	// pending-mismatch state belongs to the old host too — keeping it would
	// show the old host's alert for the new address and let "trust" pin the
	// old host's key.
	connectionChanged := (input.Host != "" && input.Host != existing.Host) ||
		(input.Port > 0 && input.Port != existing.Port)
	if connectionChanged {
		existing.HostKey = nil
		existing.HostKeySeen = nil
		existing.HostKeyMismatchAt = nil
	}

	if input.Name != "" {
		existing.Name = input.Name
	}
	if input.Host != "" {
		existing.Host = input.Host
	}
	if input.Port > 0 {
		existing.Port = input.Port
	}
	if input.Description != nil {
		existing.Description = input.Description
	}
	if input.Notes != nil {
		existing.Notes = input.Notes
	}
	existing.CredentialID = resolveCredentialBinding(existing.CredentialID, input.CredentialID, clearCredential)

	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, fmt.Errorf("server %d: %w", id, err)
	}
	// Drop any pooled connection dialed with the previous parameters. The
	// fingerprint check would also catch this on the next Get; invalidating
	// here closes it immediately instead of leaving it idle.
	s.invalidate(id)
	return s.Get(ctx, id)
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

// Delete soft-deletes a server by id.
func (s *ServerService) Delete(ctx context.Context, id uint) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("server %d: %w", id, err)
	}
	s.invalidate(id)
	return nil
}

func (s *ServerService) invalidate(serverID uint) {
	if s.pool != nil {
		s.pool.Invalidate(serverID)
	}
}

// TrustHostKey re-pins the host key a server last presented when it changed.
//
// fingerprint is the exact fingerprint the operator verified. It must still
// match the recorded pending key: the background monitor may have recorded a
// newer one since the UI rendered, and trusting that by accident would defeat
// the point of asking the user to verify. A stale request gets 409.
func (s *ServerService) TrustHostKey(ctx context.Context, id uint, fingerprint string) (*model.Server, error) {
	srv, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("server %d: %w", id, server.ErrNotFound)
		}
		return nil, fmt.Errorf("server %d: %w", id, err)
	}
	if srv.HostKeySeen == nil || srv.HostKeyMismatchAt == nil {
		return nil, server.NewAppError(http.StatusConflict, server.ReasonNoHostKeyMismatch)
	}
	if fingerprint == "" || fingerprint != sshpool.Fingerprint(*srv.HostKeySeen) {
		return nil, server.NewAppError(http.StatusConflict, server.ReasonHostKeyChanged)
	}
	// Conditional on the pending key still being the verified one, so a monitor
	// write between the read above and this update cannot slip a different key in.
	if updated, err := s.repo.TrustHostKey(ctx, id, *srv.HostKeySeen); err != nil {
		return nil, fmt.Errorf("server %d: trust host key: %w", id, err)
	} else if !updated {
		return nil, server.NewAppError(http.StatusConflict, server.ReasonHostKeyChanged)
	}
	s.invalidate(id)
	return s.Get(ctx, id)
}

// decorateHostKey fills the transient host-key fields a client needs to show a
// pending host-key change: the flag, and both fingerprints for comparison.
func decorateHostKey(srv *model.Server) {
	srv.HostKeyMismatch = srv.HostKeyMismatchAt != nil
	if srv.HostKey != nil {
		fp := sshpool.Fingerprint(*srv.HostKey)
		srv.HostKeyFingerprint = &fp
	}
	if srv.HostKeySeen != nil {
		fp := sshpool.Fingerprint(*srv.HostKeySeen)
		srv.HostKeySeenFingerprint = &fp
	}
}
