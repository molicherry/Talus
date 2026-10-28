package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

func requireMetricChunkCatalogue(t *testing.T, db *gorm.DB) {
	t.Helper()
	var exists bool
	if err := db.Raw(`SELECT to_regclass('timescaledb_information.chunks') IS NOT NULL`).Scan(&exists).Error; err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Skip("TimescaleDB extension required for chunk catalogue regression")
	}
}

func seedCompletedExpiredMetric(t *testing.T, db *gorm.DB) (*MetricRepo, PrunePlan) {
	t.Helper()
	if err := db.AutoMigrate(&model.Metric{}); err != nil {
		t.Fatal(err)
	}
	if err := PrepareMetricsRollupSchema(db); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 1, 2, 0, 10, 0, 0, time.UTC)
	if err := db.Create(&model.Metric{ServerID: 9, Time: old, CPUPercent: f64(10)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO metrics_rollup_checkpoints (server_id, completed_through) VALUES (9, ?)`, old.Add(RollupBucket)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO metrics_rollup_6h (server_id, bucket_start, samples) VALUES (9, ?, 1)`, BucketStart(old)).Error; err != nil {
		t.Fatal(err)
	}
	repo := NewMetricRepo(db)
	plan, err := repo.PlanPrune(context.Background(), old.Add(40*24*time.Hour), MetricsRetention)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RawRows != 1 || plan.RollupRows != 1 || len(plan.Pending) != 0 {
		t.Fatalf("invalid completed-history fixture: %+v", plan)
	}
	return repo, plan
}

// A chunk belonging to another schema's metrics table is not part of the
// current retention operation. In particular, an ordinary metrics table must
// still prune by row even while another instance has expired hypertable chunks.
func TestPruneMetricsScopesChunksToMetricsSchema(t *testing.T) {
	db := newTestDBWithExtensions(t)
	requireMetricChunkCatalogue(t, db)
	repo, plan := seedCompletedExpiredMetric(t, db)
	other := newTestDBWithExtensions(t)
	if err := other.AutoMigrate(&model.Metric{}); err != nil {
		t.Fatal(err)
	}
	if err := other.Exec(`SELECT create_hypertable('metrics', 'time', chunk_time_interval => interval '1 day', migrate_data => true)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := other.Create(&model.Metric{ServerID: 99, Time: plan.Cutoff.Add(-48 * time.Hour), CPUPercent: f64(20)}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := repo.PruneMetrics(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.RawRows != 1 || result.RollupRows != 1 || result.ChunksDropped != 0 {
		t.Fatalf("plain-table retention consumed another schema's chunks: %+v", result)
	}
	var remaining int64
	if err := other.Model(&model.Metric{}).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("another schema's raw history changed: remaining=%d", remaining)
	}
}

// Once a chunk catalogue exists, an enumeration failure must stop retention.
// Treating it as absence would otherwise allow raw/rollup deletes to commit
// after the physical-reclamation safety checks were never completed.
func TestPruneMetricsFailsClosedOnChunkCatalogueError(t *testing.T) {
	db := newTestDBWithExtensions(t)
	requireMetricChunkCatalogue(t, db)
	repo, plan := seedCompletedExpiredMetric(t, db)
	catalogueErr := errors.New("fixture chunk catalogue denied")
	if err := db.Callback().Row().Before("gorm:row").Register("test:retention_chunk_catalogue_error", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "FROM timescaledb_information.chunks") {
			_ = tx.AddError(catalogueErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	result, err := repo.PruneMetrics(context.Background(), plan)
	if !errors.Is(err, catalogueErr) || result.RawRows != 0 || result.RollupRows != 0 || result.ChunksDropped != 0 {
		t.Fatalf("catalogue failure was lost or retention was applied: result=%+v error=%v", result, err)
	}
	var raw, rollup int64
	if err := db.Model(&model.Metric{}).Count(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("metrics_rollup_6h").Count(&rollup).Error; err != nil {
		t.Fatal(err)
	}
	if raw != 1 || rollup != 1 {
		t.Fatalf("catalogue failure did not preserve history: raw=%d rollup=%d", raw, rollup)
	}
}
