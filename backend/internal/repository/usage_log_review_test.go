package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

func TestUsageCleanupReclaimsOnlyUnreferencedInactiveInstances(t *testing.T) {
	db, repo := usageTestDB(t, UsageLogRepoConfig{OrdinaryRetention: time.Hour})
	ctx := context.Background()
	old := time.Now().Add(-2 * time.Hour)
	cases := []struct {
		name      string
		expired   bool
		retired   bool
		reference string
		keep      bool
	}{
		{name: "healthy_empty", keep: true},
		{name: "expired_empty", expired: true},
		{name: "retired_empty", retired: true},
		{name: "expired_running", expired: true, reference: "running", keep: true},
		{name: "expired_terminal", expired: true, reference: "terminal", keep: true},
		{name: "retired_terminal", retired: true, reference: "terminal", keep: true},
		{name: "expired_pending_legacy", expired: true, reference: "legacy", keep: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner := usageUUID(800 + i)
			if err := repo.RegisterInstance(ctx, owner); err != nil {
				t.Fatal(err)
			}
			if tc.reference != "" {
				row := usageRecord(900+i, &owner)
				if tc.reference != "running" {
					row = usageFinish(row, "succeeded")
				}
				if err := repo.Upsert(ctx, &row); err != nil {
					t.Fatal(err)
				}
				if tc.reference == "legacy" {
					// An unfinished backfill keeps even an expired legacy summary.
					if err := db.Model(&model.UsageLog{}).Where("id = ?", row.ID).Updates(map[string]interface{}{"source": "audit_legacy", "retention_at": old}).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			changes := map[string]interface{}{"last_seen_at": old}
			if tc.expired {
				changes["lease_until"] = old
			}
			if tc.retired {
				changes["retired_at"] = old
			}
			if err := db.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner).Updates(changes).Error; err != nil {
				t.Fatal(err)
			}
			if err := repo.Cleanup(ctx); err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := db.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if (count == 1) != tc.keep {
				t.Fatalf("instance count=%d, keep=%t", count, tc.keep)
			}
		})
	}
}

func TestUsageCleanupReclaimsExpiredInstanceAfterLastSummary(t *testing.T) {
	db, repo := usageTestDB(t, UsageLogRepoConfig{OrdinaryRetention: time.Hour})
	ctx := context.Background()
	owner := usageUUID(820)
	if err := repo.RegisterInstance(ctx, owner); err != nil {
		t.Fatal(err)
	}
	row := usageFinish(usageRecord(920, &owner), "succeeded")
	if err := repo.Upsert(ctx, &row); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := db.Model(&model.UsageLog{}).Where("id = ?", row.ID).Update("retention_at", old).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner).Update("lease_until", old).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, row.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expired summary remained: %v", err)
	}
	if got := repo.CleanedCount(); got != 1 {
		t.Fatalf("summary deletion count=%d, want 1", got)
	}
	if err := repo.Heartbeat(ctx, owner); !errors.Is(err, ErrUsageOwnerMissing) {
		t.Fatalf("deleted instance renewed: %v", err)
	}
	late := usageFinish(usageRecord(921, &owner), "succeeded")
	if err := repo.Upsert(ctx, &late); !errors.Is(err, ErrUsageOwnerMissing) {
		t.Fatalf("deleted instance accepted a late operation: %v", err)
	}
}

func TestUsageCleanupKeepsHealthyInstanceAfterLastSummary(t *testing.T) {
	db, repo := usageTestDB(t, UsageLogRepoConfig{OrdinaryRetention: time.Hour})
	ctx := context.Background()
	owner := usageUUID(821)
	if err := repo.RegisterInstance(ctx, owner); err != nil {
		t.Fatal(err)
	}
	row := usageFinish(usageRecord(922, &owner), "succeeded")
	if err := repo.Upsert(ctx, &row); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLog{}).Where("id = ?", row.ID).Update("retention_at", time.Now().Add(-2*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, row.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expired summary remained: %v", err)
	}
	if err := repo.Heartbeat(ctx, owner); err != nil {
		t.Fatalf("healthy instance removed with its last summary: %v", err)
	}
}

func TestUsageCleanupSkipsLockedEmptyInstanceRenewal(t *testing.T) {
	db, repo := usageTestDB(t)
	ctx := context.Background()
	owner := usageUUID(822)
	if err := repo.RegisterInstance(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner).Update("lease_until", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner).Update("lease_until", time.Now().Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	timeout, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := repo.Cleanup(timeout); err != nil {
		t.Fatalf("cleanup waited behind an empty instance's renewal: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.Heartbeat(ctx, owner); err != nil {
		t.Fatalf("renewed empty instance removed: %v", err)
	}
}

func TestUsageReconcileTrackedSnapshotsUnderOwnerLock(t *testing.T) {
	db, repo := usageTestDB(t, UsageLogRepoConfig{ReconcileGrace: time.Microsecond})
	ctx := context.Background()
	owner := usageUUID(823)
	if err := repo.RegisterInstance(ctx, owner); err != nil {
		t.Fatal(err)
	}
	tracked := usageRecord(923, &owner)
	orphan := usageRecord(924, &owner)
	for _, row := range []*model.UsageLog{&tracked, &orphan} {
		if err := repo.Upsert(ctx, row); err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.UsageLog{}).Where("id = ?", row.ID).Update("recorded_at", time.Now().Add(-time.Hour)).Error; err != nil {
			t.Fatal(err)
		}
	}
	newRunning := usageRecord(925, &owner)
	var blockedWrite error
	callbacks := 0
	if err := repo.ReconcileTracked(ctx, owner, func() []string {
		callbacks++
		// A Begin admitted after this snapshot may enter memory immediately,
		// but its first INSERT must wait for the owner's UPDATE lock. Use a
		// bounded write to prove that lock, without relying on scheduling sleeps.
		writeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		attempt := newRunning
		blockedWrite = repo.Upsert(writeCtx, &attempt)
		return []string{tracked.OperationID}
	}); err != nil {
		t.Fatal(err)
	}
	if callbacks != 1 || !errors.Is(blockedWrite, context.DeadlineExceeded) {
		t.Fatalf("snapshot callbacks=%d, concurrent write error=%v", callbacks, blockedWrite)
	}
	if got := requireUsage(t, repo, tracked); got.Outcome != "running" || got.RetentionAt != nil {
		t.Fatalf("tracked operation was reconciled: %+v", got)
	}
	if got := requireUsage(t, repo, orphan); got.Outcome != "unknown" || got.RecoveryReason != "finalization_unavailable" || got.RetentionAt == nil {
		t.Fatalf("orphan was not reconciled: %+v", got)
	}
	var count int64
	if err := db.Model(&model.UsageLog{}).Where("operation_id = ?", newRunning.OperationID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("new running operation committed while the snapshot held its owner lock")
	}
	if err := repo.Upsert(ctx, &newRunning); err != nil {
		t.Fatalf("new operation could not write after reconciliation committed: %v", err)
	}
	if got := requireUsage(t, repo, newRunning); got.Outcome != "running" || got.RecoveryReason != "" || got.RetentionAt != nil {
		t.Fatalf("new running operation was misclassified: %+v", got)
	}
}
