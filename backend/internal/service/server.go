package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	"gorm.io/gorm"
)

// ServerService provides business logic for server management.
type ServerService struct {
	repo       *repository.ServerRepo
	metricRepo *repository.MetricRepo
}

// NewServerService creates a ServerService with the given repositories.
func NewServerService(repo *repository.ServerRepo, metricRepo *repository.MetricRepo) *ServerService {
	return &ServerService{repo: repo, metricRepo: metricRepo}
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

// ListFiltered returns servers scoped to the given serverIDs. When serverIDs is empty,
// it returns all servers (full access).
func (s *ServerService) ListFiltered(ctx context.Context, serverIDs []uint) ([]model.Server, error) {
	if len(serverIDs) == 0 {
		return s.List(ctx)
	}

	servers, err := s.repo.FindByIDs(ctx, serverIDs)
	if err != nil {
		return nil, fmt.Errorf("list filtered servers: %w", err)
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
func (s *ServerService) Update(ctx context.Context, id uint, input *model.Server) (*model.Server, error) {
	existing, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("server %d: %w", id, server.ErrNotFound)
		}
		return nil, fmt.Errorf("server %d: %w", id, err)
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
	if input.CredentialID != nil {
		existing.CredentialID = input.CredentialID
	}

	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, fmt.Errorf("server %d: %w", id, err)
	}
	return s.Get(ctx, id)
}

// Delete soft-deletes a server by id.
func (s *ServerService) Delete(ctx context.Context, id uint) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("server %d: %w", id, err)
	}
	return nil
}
