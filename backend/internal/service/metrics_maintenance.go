package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/vpsmanager/backend/internal/repository"
)

// MetricsMaintenance runs the periodic metrics storage jobs of REQ-08 §8.3.1:
// an hourly 6h-rollup refresh (first run backfills the retention window) and a
// daily retention pass. A failed refresh or an incomplete aggregation pauses the
// corresponding work and is reported, never silently ignored.
type MetricsMaintenance struct {
	repo         *repository.MetricRepo
	logger       *slog.Logger
	refreshEvery time.Duration
	pruneEvery   time.Duration
	// jobBudget bounds a single cycle so a huge backfill cannot hold shutdown or
	// the connection pool forever.
	jobBudget time.Duration
	// now is injectable for tests.
	now func() time.Time
	// retentionEnabled/retentionDryRun implement the documented rollout: report a
	// plan first, delete only when the operator enabled it.
	retentionEnabled bool
	retentionDryRun  bool
}

// NewMetricsMaintenance creates the maintenance jobs with their documented
// cadence: refresh hourly, prune daily.
func NewMetricsMaintenance(repo *repository.MetricRepo) *MetricsMaintenance {
	return &MetricsMaintenance{
		repo:            repo,
		logger:          slog.Default().With("component", "metrics-maintenance"),
		refreshEvery:    time.Hour,
		pruneEvery:      24 * time.Hour,
		jobBudget:       10 * time.Minute,
		now:             func() time.Time { return time.Now().UTC() },
		retentionDryRun: true,
	}
}

// SetRetention applies the rollout switches from the configuration.
func (m *MetricsMaintenance) SetRetention(enabled, dryRun bool) {
	m.retentionEnabled = enabled
	m.retentionDryRun = dryRun
}

// Run drives the jobs until ctx is cancelled. It returns after joining any
// in-flight cycle, so the caller may close storage afterwards.
func (m *MetricsMaintenance) Run(ctx context.Context) {
	if m == nil || m.repo == nil {
		return
	}
	refreshTicker := time.NewTicker(m.refreshEvery)
	defer refreshTicker.Stop()
	pruneTicker := time.NewTicker(m.pruneEvery)
	defer pruneTicker.Stop()

	m.logger.Info("metrics maintenance started", "refresh", m.refreshEvery, "prune", m.pruneEvery)
	// A first cycle after startup backfills the whole retention window; later
	// cycles only recompute the recent window.
	m.Refresh(ctx, repository.RollupBackfillWindow)

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("metrics maintenance stopped")
			return
		case <-refreshTicker.C:
			m.Refresh(ctx, repository.RollupRefreshWindow)
		case <-pruneTicker.C:
			m.Prune(ctx)
		}
	}
}

// Refresh materializes closed buckets and reports the outcome.
func (m *MetricsMaintenance) Refresh(ctx context.Context, window time.Duration) {
	if ctx.Err() != nil {
		return
	}
	jobCtx, cancel := context.WithTimeout(ctx, m.jobBudget)
	defer cancel()
	result, err := m.repo.RefreshRollups(jobCtx, m.now(), window)
	if err != nil {
		// A failed cycle leaves the checkpoint untouched, so the next cycle
		// recomputes the same range.
		m.logger.Error("metrics rollup refresh failed", "error", err, "window", window)
		return
	}
	m.logger.Info("metrics rollup refreshed",
		"servers", result.Servers, "buckets", result.Buckets, "through", result.Checkpoint)
}

// Prune plans and applies retention. It always plans first (the dry run) and
// pauses for servers whose aggregation is not confirmed.
// StorageReport records the on-disk footprint of the metrics storage so capacity
// planning is based on measurements rather than struct sizes.
func (m *MetricsMaintenance) StorageReport(ctx context.Context) {
	if m == nil || m.repo == nil || ctx.Err() != nil {
		return
	}
	report, err := m.repo.StorageReport(ctx)
	if err != nil {
		m.logger.Error("metrics storage report failed", "error", err)
		return
	}
	m.logger.Info("metrics storage report",
		"raw_rows", report.RawRows, "raw_total_bytes", report.RawTotalBytes,
		"raw_table_bytes", report.RawTableBytes, "raw_index_bytes", report.RawIndexBytes,
		"raw_chunk_bytes", report.RawChunkBytes, "rollup_rows", report.RollupRows,
		"rollup_total_bytes", report.RollupTotalBytes,
		"oldest_raw", report.OldestRaw, "newest_raw", report.NewestRaw,
		"projected_35d_bytes", report.ProjectedRetentionBytes)
}

func (m *MetricsMaintenance) Prune(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	jobCtx, cancel := context.WithTimeout(ctx, m.jobBudget)
	defer cancel()
	now := m.now()
	plan, err := m.repo.PlanPrune(jobCtx, now, repository.MetricsRetention)
	if err != nil {
		m.logger.Error("metrics retention planning failed", "error", err)
		return
	}
	if len(plan.Pending) > 0 {
		m.logger.Warn("metrics aggregation incomplete, retention paused for those servers",
			"pending_servers", len(plan.Pending), "cutoff", plan.Cutoff)
	}
	switch retentionDecision(m.retentionEnabled, m.retentionDryRun) {
	case retentionDisabled:
		// Disabled: the cycle still reports exactly what it would delete, which is
		// the read-only verification step of the rollout.
		m.logger.Info("metrics retention disabled, dry run only",
			"cutoff", plan.Cutoff, "raw_rows", plan.RawRows, "rollup_rows", plan.RollupRows,
			"pending_servers", len(plan.Pending))
		return
	case retentionDryRunOnly:
		m.logger.Info("metrics retention dry run",
			"cutoff", plan.Cutoff, "raw_rows", plan.RawRows, "rollup_rows", plan.RollupRows,
			"pending_servers", len(plan.Pending))
		return
	}
	result, err := m.repo.PruneMetrics(jobCtx, plan)
	if err != nil {
		m.logger.Error("metrics retention failed", "error", err, "cutoff", plan.Cutoff)
		return
	}
	m.logger.Info("metrics retention applied",
		"raw_rows", result.RawRows, "rollup_rows", result.RollupRows,
		"chunks_dropped", result.ChunksDropped, "paused", result.Paused)
}

// retentionDecision is the rollout state machine: disabled reports a plan only,
// a dry run reports without deleting, and only an enabled non-dry-run applies.
type retentionDecisionKind int

const (
	retentionDisabled retentionDecisionKind = iota
	retentionDryRunOnly
	retentionApply
)

func retentionDecision(enabled, dryRun bool) retentionDecisionKind {
	switch {
	case !enabled:
		return retentionDisabled
	case dryRun:
		return retentionDryRunOnly
	default:
		return retentionApply
	}
}
