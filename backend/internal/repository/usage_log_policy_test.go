package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vpsmanager/backend/internal/model"
)

func TestUsageDatabaseErrorClassesAreFixedAndRetryableOnlyWhenTransient(t *testing.T) {
	for _, tc := range []struct {
		code string
		want error
	}{{"22003", ErrUsageInvalid}, {"23514", ErrUsageInvalid}, {"23505", ErrUsageInvalid}, {"42501", ErrUsagePermanent}, {"42P01", ErrUsagePermanent}, {"28P01", ErrUsagePermanent}, {"0A000", ErrUsagePermanent}, {"40001", ErrUsageStorage}, {"40P01", ErrUsageStorage}, {"53300", ErrUsageStorage}, {"55P03", ErrUsageStorage}, {"08006", ErrUsageStorage}, {"57P01", ErrUsageStorage}} {
		t.Run(tc.code, func(t *testing.T) {
			err := usageErr(fmt.Errorf("driver wrapper: %w", &pgconn.PgError{Code: tc.code, Message: "SECRET SQL argument", Detail: "SECRET private credential"}))
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "SECRET") || err.Error() != tc.want.Error() {
				t.Fatalf("unsafe or misclassified SQLSTATE %s: %v", tc.code, err)
			}
		})
	}
}

func TestUsageSQLRetentionSharesDefaultsAndFutureDeletionClassification(t *testing.T) {
	db, repo := usageTestDB(t, UsageLogRepoConfig{OrdinaryRetention: time.Hour, SensitiveRetention: 3 * time.Hour})
	policy := (model.UsagePolicy{OrdinaryRetention: time.Hour, SensitiveRetention: 3 * time.Hour}).WithDefaults()
	for i, action := range []string{"future_resource.delete", "credential.reveal", "api_key.reveal", "service.credentials", "server.host_key.trust", "server.update", "future_resource.delete.extra"} {
		if got := repo.retention(action); got != policy.Retention(action) {
			t.Fatalf("repository Go retention for %s diverged: %s", action, got)
		}
		row := usageFinish(usageRecord(1000+i, nil), "succeeded")
		row.Action = action
		old := time.Now().Add(-2 * time.Hour)
		row.RetentionAt, row.FinishedAt = &old, &old
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rows []model.UsageLog
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("SQL classification disagrees with shared policy: %+v", rows)
	}
	for _, row := range rows {
		if !model.UsageActionSensitive(row.Action) {
			t.Fatalf("ordinary action was retained by SQL: %s", row.Action)
		}
	}
	zero := NewUsageLogRepo(db, UsageLogRepoConfig{LeaseDuration: -time.Second, OrdinaryRetention: 0, SensitiveRetention: -time.Hour})
	defaults := (model.UsagePolicy{}).WithDefaults()
	if zero.config.LeaseDuration != defaults.LeaseDuration || zero.config.OrdinaryRetention != defaults.OrdinaryRetention || zero.config.SensitiveRetention != defaults.SensitiveRetention {
		t.Fatal("repository nonpositive retention or lease defaults diverged")
	}
}

func TestUsageCleanupMustReportBeforeDeletingAndBoundsDimensions(t *testing.T) {
	db, repo := usageTestDB(t, UsageLogRepoConfig{OrdinaryRetention: time.Hour, SensitiveRetention: time.Hour})
	for i, tc := range []struct{ source, action, outcome string }{{"operation", "server.update", "succeeded"}, {"operation", "server.update", "succeeded"}, {"SECRET source", "SECRET command", "failed"}, {"audit_legacy", "credential.reveal", "unknown"}} {
		row := usageFinish(usageRecord(1100+i, nil), tc.outcome)
		row.Source, row.Action = tc.source, tc.action
		old := time.Now().Add(-2 * time.Hour)
		row.RetentionAt, row.FinishedAt = &old, &old
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	groups, err := repo.CleanupReport(context.Background())
	if err != nil || len(groups) != 2 || groups[0].Source != "operation" || groups[0].Action != "server.update" || groups[0].Count != 2 || groups[1].Source != "other" || groups[1].Action != "other" || groups[1].Outcome != "failed" || groups[1].Count != 1 {
		t.Fatalf("cleanup report included raw dimensions or incomplete legacy: %+v error=%v", groups, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := repo.Cleanup(ctx); !errors.Is(err, context.Canceled) || repo.cleanupReported.Load() {
		t.Fatalf("failed report enabled deletion: %v", err)
	}
	var count int64
	if err := db.Model(&model.UsageLog{}).Count(&count).Error; err != nil || count != 4 {
		t.Fatal("cleanup deleted rows before a successful report", count, err)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	if err := repo.PrepareCleanup(context.Background()); err != nil || !repo.cleanupReported.Load() {
		t.Fatalf("successful eligibility report did not enable cleanup: %v", err)
	}
	if strings.Contains(logs.String(), "SECRET") || !strings.Contains(logs.String(), `"eligible":3`) || !strings.Contains(logs.String(), `"action":"server.update"`) {
		t.Fatalf("unsafe or incomplete cleanup report: %s", logs.String())
	}
	if err := repo.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLog{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("prepared cleanup did not delete eligible rows or deleted unsealed legacy", count, err)
	}
}

func TestUsageBackfillCompletionAndCleanupReportCommitTogether(t *testing.T) {
	db, repo := usageTestDB(t, UsageLogRepoConfig{OrdinaryRetention: time.Hour, SensitiveRetention: time.Hour})
	event := model.AuditEvent{BaseModel: model.BaseModel{CreatedAt: time.Now().Add(-2 * time.Hour)}, Action: "credential.reveal", Username: "SECRET display name", Details: "SECRET free text"}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	// The migration inserts summaries before sealing. If eligibility counting
	// fails, the seal must roll back so the cleaner cannot delete those rows.
	if err := db.Exec("DROP TABLE usage_log_instances").Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.Backfill(context.Background()); !errors.Is(err, ErrUsagePermanent) {
		t.Fatalf("failed completion report error=%v", err)
	}
	status, err := repo.BackfillStatus(context.Background())
	if err != nil || status.CompletedAt != nil || status.Imported != 1 {
		t.Fatalf("failed report sealed migration or lost checkpoint: %+v error=%v", status, err)
	}
	if err := db.AutoMigrate(&model.UsageLogInstance{}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	if err := repo.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = repo.BackfillStatus(context.Background())
	if err != nil || status.CompletedAt == nil || status.Imported != 1 || status.FinalBound != uint64(event.ID) {
		t.Fatalf("successful completion metrics are incorrect: %+v error=%v", status, err)
	}
	if strings.Contains(logs.String(), "SECRET") || !strings.Contains(logs.String(), `"scope":"legacy_backfill"`) || !strings.Contains(logs.String(), `"eligible":1`) || !strings.Contains(logs.String(), `"source":"audit_legacy"`) {
		t.Fatalf("legacy pre-cleanup report was unsafe or missing: %s", logs.String())
	}
	if err := repo.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := repo.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.UsageLog{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("completed migration resurrected cleaned summaries", count, err)
	}
}
