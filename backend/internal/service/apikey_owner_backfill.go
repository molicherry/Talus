package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/vpsmanager/backend/internal/repository"
)

type legacyAPIKeyOwnerBinder interface {
	BindUnownedToDefaultAdmin(context.Context) (int64, error)
}

// LegacyAPIKeyOwnerBackfill retries optional assignment without putting key
// updates on the server startup or initial-administrator transaction path.
type LegacyAPIKeyOwnerBackfill struct {
	repo       legacyAPIKeyOwnerBinder
	wake       chan struct{}
	interval   time.Duration
	timeout    time.Duration
	maxBatches int
}

func NewLegacyAPIKeyOwnerBackfill(repo legacyAPIKeyOwnerBinder) *LegacyAPIKeyOwnerBackfill {
	return &LegacyAPIKeyOwnerBackfill{
		repo: repo, wake: make(chan struct{}, 1),
		interval: time.Minute, timeout: 2 * time.Second, maxBatches: 8,
	}
}

// Trigger coalesces notifications and never waits for the database.
func (b *LegacyAPIKeyOwnerBackfill) Trigger() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *LegacyAPIKeyOwnerBackfill) Run(ctx context.Context) {
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	b.Trigger()
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.wake:
			b.runBatch(ctx)
		case <-ticker.C:
			b.runBatch(ctx)
		}
	}
}

func (b *LegacyAPIKeyOwnerBackfill) runBatch(ctx context.Context) {
	for i := 0; i < b.maxBatches && ctx.Err() == nil; i++ {
		batchCtx, cancel := context.WithTimeout(ctx, b.timeout)
		changed, err := b.repo.BindUnownedToDefaultAdmin(batchCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("legacy API key owner assignment deferred", "reason", "owner_backfill_unavailable")
			}
			return
		}
		if changed > 0 {
			slog.Info("legacy API key owners assigned", "keys", changed)
		}
		if changed < repository.APIKeyOwnerBackfillBatchSize {
			return
		}
	}
}
