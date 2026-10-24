package repository

import (
	"context"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
)

func TestSettingsAuditRetentionFloorAndSingleRetentionWindow(t *testing.T) {
	day := 24 * time.Hour
	db, repo := usageTestDB(t, UsageLogRepoConfig{OrdinaryRetention: 30 * day, SensitiveRetention: day})
	ctx := context.Background()
	fresh := usageFinish(usageRecord(6000, nil), "succeeded")
	if err := repo.Upsert(ctx, &fresh); err != nil {
		t.Fatal(err)
	}
	var persisted model.UsageLog
	if err := db.Where("operation_id = ?", fresh.OperationID).First(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.RetentionAt == nil || !persisted.RetentionAt.Equal(*persisted.FinishedAt) {
		t.Fatal("retention_at must be the completion anchor, not an expiry")
	}
	for i, tc := range []struct {
		action string
		age    time.Duration
	}{
		{"server.update", 31 * day}, {"credential.reveal", 2 * day},
		{"settings.update", 89 * day}, {"settings.update", 91 * day},
	} {
		row := usageFinish(usageRecord(6001+i, nil), "succeeded")
		old := time.Now().Add(-tc.age)
		row.Action, row.StartedAt, row.FinishedAt, row.RetentionAt = tc.action, old, &old, &old
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if got := repo.retention("settings.update"); got != 90*day {
		t.Fatalf("Go policy floor = %v", got)
	}
	if err := repo.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []model.UsageLog
	if err := db.Order("operation_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].OperationID != fresh.OperationID || rows[1].OperationID != usageUUID(6003) {
		t.Fatalf("SQL retention floor/window mismatch: %+v", rows)
	}
	if got := (model.UsagePolicy{SensitiveRetention: 180 * day}).Retention("settings.update"); got != 180*day {
		t.Fatal("longer audit retention was shortened")
	}
}
