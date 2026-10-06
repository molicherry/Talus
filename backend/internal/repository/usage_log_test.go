package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
)

func usageTestDB(t *testing.T, config ...UsageLogRepoConfig) (*gorm.DB, *UsageLogRepo) {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.UsageLog{}, &model.UsageLogInstance{}, &model.UsageLogBackfillState{}, &model.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	return db, NewUsageLogRepo(db, config...)
}
func usageUUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
func usageRecord(n int, owner *string) model.UsageLog {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return model.UsageLog{OperationID: usageUUID(n), StartedAt: now, Outcome: "running", Phase: "preparing", StateSeq: 1, Action: "server.exec", ResourceType: "server", AuthType: "jwt", Source: "operation", OwnerInstanceID: owner, Metadata: json.RawMessage(`{"version":1}`)}
}
func usageFinish(v model.UsageLog, outcome string) model.UsageLog {
	now := time.Now().UTC().Truncate(time.Microsecond)
	d := now.Sub(v.StartedAt).Milliseconds()
	v.FinishedAt = &now
	v.DurationMS = &d
	v.Outcome = outcome
	v.Phase = "closed"
	v.StateSeq++
	return v
}
func requireUsage(t *testing.T, r *UsageLogRepo, v model.UsageLog) model.UsageLog {
	t.Helper()
	got, err := r.Get(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	return *got
}

func TestUsageTerminalIdempotencyAndConcurrentInsert(t *testing.T) {
	db, r := usageTestDB(t)
	ctx := context.Background()
	owner := usageUUID(100)
	if err := r.RegisterInstance(ctx, owner); err != nil {
		t.Fatal(err)
	}
	v := usageFinish(usageRecord(1, &owner), "succeeded")
	if err := r.Upsert(ctx, &v); err != nil {
		t.Fatal(err)
	}
	oldID := v.ID
	if err := r.Upsert(ctx, &v); err != nil {
		t.Fatal(err)
	}
	lower := v
	lower.StateSeq = 1
	lower.Outcome = "running"
	lower.FinishedAt = nil
	lower.DurationMS = nil
	if err := r.Upsert(ctx, &lower); err != nil {
		t.Fatal(err)
	}
	conflict := v
	conflict.StateSeq = 9
	conflict.Outcome = "failed"
	if err := r.Upsert(ctx, &conflict); err != nil {
		t.Fatal(err)
	}
	got := requireUsage(t, r, v)
	if got.ID != oldID || got.Outcome != "succeeded" || got.StateSeq != 2 {
		t.Fatalf("terminal overwritten: %+v", got)
	}
	if !json.Valid(got.Metadata) || string(got.Metadata) == `"{\"version\":1}"` {
		t.Fatalf("metadata became string: %s", got.Metadata)
	}
	short := usageFinish(usageRecord(2, nil), "succeeded")
	short.Action = "server.update"
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); copy := short; errs <- r.Upsert(ctx, &copy) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Model(&model.UsageLog{}).Where("operation_id = ?", short.OperationID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent retries inserted %d rows", count)
	}
}

func TestUsageSequenceAndImmutableIdentity(t *testing.T) {
	_, r := usageTestDB(t)
	ctx := context.Background()
	owner := usageUUID(101)
	if err := r.RegisterInstance(ctx, owner); err != nil {
		t.Fatal(err)
	}
	v := usageRecord(3, &owner)
	if err := r.Upsert(ctx, &v); err != nil {
		t.Fatal(err)
	}
	ready := v
	ready.StateSeq = 3
	ready.Phase = "ready"
	ready.Metadata = json.RawMessage(`{"version":1,"timeout_seconds":5}`)
	if err := r.Upsert(ctx, &ready); err != nil {
		t.Fatal(err)
	}
	stale := v
	stale.StateSeq = 2
	stale.Phase = "handshake"
	if err := r.Upsert(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	got := requireUsage(t, r, v)
	if got.Phase != "ready" || got.StateSeq != 3 {
		t.Fatalf("stale phase overwrote newer state: %+v", got)
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal(got.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["timeout_seconds"] != float64(5) {
		t.Fatalf("update metadata incorrect: %s", got.Metadata)
	}
	changed := ready
	changed.StartedAt = changed.StartedAt.Add(time.Second)
	changed.StateSeq = 4
	if err := r.Upsert(ctx, &changed); !errors.Is(err, ErrUsageInvalid) {
		t.Fatalf("changed ordering accepted: %v", err)
	}
	changed = ready
	changed.AuthType = "api_key"
	changed.StateSeq = 4
	if err := r.Upsert(ctx, &changed); !errors.Is(err, ErrUsageInvalid) {
		t.Fatalf("changed identity accepted: %v", err)
	}
}

func TestUsageOwnerBarriers(t *testing.T) {
	db, r := usageTestDB(t)
	ctx := context.Background()
	owner := usageUUID(102)
	v := usageRecord(4, &owner)
	if err := r.Upsert(ctx, &v); !errors.Is(err, ErrUsageOwnerMissing) {
		t.Fatalf("missing owner wrote: %v", err)
	}
	if err := r.RegisterInstance(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner).Update("lease_until", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(ctx, &v); !errors.Is(err, ErrUsageLeaseExpired) {
		t.Fatalf("expired owner wrote: %v", err)
	}
	if err := r.Heartbeat(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(ctx, &v); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner).Update("retired_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	finish := usageFinish(v, "succeeded")
	if err := r.Upsert(ctx, &finish); !errors.Is(err, ErrUsageOwnerRetired) {
		t.Fatalf("retired owner completed: %v", err)
	}
	if err := r.Heartbeat(ctx, owner); !errors.Is(err, ErrUsageOwnerRetired) {
		t.Fatalf("retired owner renewed: %v", err)
	}
	if err := r.RegisterInstance(ctx, owner); !errors.Is(err, ErrUsageOwnerRetired) {
		t.Fatalf("retired owner reregistered: %v", err)
	}
	if err := db.Delete(&model.UsageLogInstance{}, "instance_id = ?", owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterInstance(ctx, owner); !errors.Is(err, ErrUsageOwnerMissing) {
		t.Fatalf("missing referenced owner reregistered: %v", err)
	}
	finish.OperationID = usageUUID(5)
	if err := r.Upsert(ctx, &finish); !errors.Is(err, ErrUsageOwnerMissing) {
		t.Fatalf("missing owner recreated: %v", err)
	}
}

func TestUsageRecoveryAndActiveResume(t *testing.T) {
	db, r := usageTestDB(t, UsageLogRepoConfig{ReconcileGrace: time.Microsecond, BatchSize: 1})
	ctx := context.Background()
	owner := usageUUID(103)
	healthy := usageUUID(104)
	for _, id := range []string{owner, healthy} {
		if err := r.RegisterInstance(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	active := usageRecord(6, &owner)
	other := usageRecord(7, &healthy)
	if err := r.Upsert(ctx, &active); err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(ctx, &other); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner).Update("lease_until", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, healthy, []string{other.OperationID}); err != nil {
		t.Fatal(err)
	}
	got := requireUsage(t, r, active)
	if got.Outcome != "unknown" || got.RecoveryReason != "owner_lease_expired" || got.FinishedAt != nil || got.DurationMS != nil || got.RetentionAt == nil {
		t.Fatalf("invalid recovery: %+v", got)
	}
	retained := *got.RetentionAt
	if err := r.Reconcile(ctx, healthy, []string{other.OperationID}); err != nil {
		t.Fatal(err)
	}
	got = requireUsage(t, r, active)
	if !got.RetentionAt.Equal(retained) {
		t.Fatal("recovery extended retention")
	}
	if requireUsage(t, r, other).Outcome != "running" {
		t.Fatal("healthy tracked owner was interrupted")
	}
	if err := r.Heartbeat(ctx, owner); err != nil {
		t.Fatal(err)
	}
	ids, err := r.OwnerUnknown(ctx, owner)
	if err != nil || len(ids) != 1 || ids[0] != active.OperationID {
		t.Fatalf("owner recovery ids: %v %v", ids, err)
	}
	active.StateSeq = 2
	active.Phase = "ready"
	if err := r.Upsert(ctx, &active); err != nil {
		t.Fatal(err)
	}
	got = requireUsage(t, r, active)
	if got.Outcome != "running" || got.RetentionAt != nil || got.ReconciledAt != nil || got.RecoveryReason != "" {
		t.Fatalf("active correction not cleared: %+v", got)
	}
	finish := usageFinish(active, "succeeded")
	if err := r.Upsert(ctx, &finish); err != nil {
		t.Fatal(err)
	}
}

func TestUsageLocalOrphansNotStarvedByTrackedRows(t *testing.T) {
	db, r := usageTestDB(t, UsageLogRepoConfig{ReconcileGrace: time.Microsecond, BatchSize: 1})
	ctx := context.Background()
	owner := usageUUID(105)
	if err := r.RegisterInstance(ctx, owner); err != nil {
		t.Fatal(err)
	}
	tracked := usageRecord(8, &owner)
	orphan := usageRecord(9, &owner)
	for _, v := range []*model.UsageLog{&tracked, &orphan} {
		if err := r.Upsert(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.UsageLog{}).Where("owner_instance_id = ?", owner).Update("recorded_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, owner, []string{tracked.OperationID}); err != nil {
		t.Fatal(err)
	}
	got := requireUsage(t, r, orphan)
	if got.Outcome != "unknown" || got.RecoveryReason != "finalization_unavailable" || got.FinishedAt != nil {
		t.Fatalf("orphan not repaired: %+v", got)
	}
	if requireUsage(t, r, tracked).Outcome != "running" {
		t.Fatal("tracked row altered")
	}
}

func TestUsageCleanupRetiresOwnerWithoutFinishedTime(t *testing.T) {
	db, r := usageTestDB(t, UsageLogRepoConfig{OrdinaryRetention: time.Hour, SensitiveRetention: 2 * time.Hour})
	ctx := context.Background()
	owner := usageUUID(106)
	if err := r.RegisterInstance(ctx, owner); err != nil {
		t.Fatal(err)
	}
	v := usageRecord(10, &owner)
	if err := r.Upsert(ctx, &v); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner).Update("lease_until", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLog{}).Where("id = ?", v.ID).Updates(map[string]interface{}{"retention_at": time.Now().Add(-2 * time.Hour), "reconciled_at": time.Now().Add(-2 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	// A healthy owner returning before cleanup wins and preserves its summary.
	if err := r.Heartbeat(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := r.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, v.ID); err != nil {
		t.Fatal("renewed owner row deleted", err)
	}
	if err := db.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner).Update("lease_until", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, v.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("unknown retained: %v", err)
	}
	late := usageFinish(v, "succeeded")
	if err := r.Upsert(ctx, &late); !errors.Is(err, ErrUsageOwnerRetired) {
		t.Fatalf("late completion revived: %v", err)
	}
	if err := r.Heartbeat(ctx, owner); !errors.Is(err, ErrUsageOwnerRetired) {
		t.Fatalf("retirement reversed: %v", err)
	}
}

func TestUsageCleanupSensitiveRowsDoNotStarveOrdinary(t *testing.T) {
	db, r := usageTestDB(t, UsageLogRepoConfig{OrdinaryRetention: time.Hour, SensitiveRetention: 3 * time.Hour, BatchSize: 1})
	ctx := context.Background()
	for i, action := range []string{"credential.reveal", "server.update"} {
		v := usageFinish(usageRecord(11+i, nil), "succeeded")
		v.Action = action
		ret := time.Now().Add(-2 * time.Hour)
		v.RetentionAt = &ret
		v.FinishedAt = &ret
		if err := db.Create(&v).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var items []model.UsageLog
	if err := db.Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Action != "credential.reveal" {
		t.Fatalf("incorrect classified retention: %+v", items)
	}
}

func TestUsageAuditIndependentAndLegacyBackfill(t *testing.T) {
	db, r := usageTestDB(t, UsageLogRepoConfig{OrdinaryRetention: time.Hour, SensitiveRetention: time.Hour, BatchSize: 1})
	ctx := context.Background()
	audit := NewAuditEventRepo(db)
	op := usageUUID(14)
	event := model.AuditEvent{OperationID: &op, Action: "credential.reveal", Username: "admin", Details: "SECRET_MARKER"}
	if err := audit.Create(ctx, &event); err != nil {
		t.Fatal(err)
	}
	retry := model.AuditEvent{OperationID: &op, Action: "credential.reveal", Username: "admin"}
	if err := audit.Create(ctx, &retry); err != nil {
		t.Fatal(err)
	}
	if retry.ID != event.ID {
		t.Fatal("audit retry duplicated")
	}
	missing := usageUUID(107)
	v := usageFinish(usageRecord(14, &missing), "succeeded")
	if err := r.Upsert(ctx, &v); !errors.Is(err, ErrUsageOwnerMissing) {
		t.Fatalf("expected UsageLog fault: %v", err)
	}
	var count int64
	if err := db.Model(&model.AuditEvent{}).Where("operation_id = ?", op).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("usage failure removed audit", err, count)
	}
	created := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Microsecond)
	old := model.AuditEvent{BaseModel: model.BaseModel{CreatedAt: created}, UserID: 1, Username: "old-\nadmin", IPAddress: "[2001:db8::1]:1234", Action: "credential.reveal", ResourceType: "credential", ResourceID: 1, Details: "SECRET_MARKER"}
	if err := audit.Create(ctx, &old); err != nil {
		t.Fatal(err)
	}
	if err := r.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	var logs []model.UsageLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].LegacyAuditEventID == nil || *logs[0].LegacyAuditEventID != old.ID || logs[0].Source != "audit_legacy" || logs[0].Outcome != "unknown" || logs[0].FinishedAt != nil {
		t.Fatalf("legacy mapping invalid: %+v", logs)
	}
	if string(logs[0].Metadata) != "{}" {
		t.Fatalf("legacy details copied: %s", logs[0].Metadata)
	}
	if logs[0].AuthType != "legacy_unknown" || logs[0].UserID != nil || logs[0].APIKeyID != nil || logs[0].UsernameSnapshot != "old-admin" || logs[0].ClientAddress != "2001:db8::1" {
		t.Fatalf("legacy identity guessed or unsafe: %+v", logs[0])
	}
	if err := r.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLog{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("cleared legacy resurrected", err, count)
	}
	if err := db.Model(&model.AuditEvent{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("legacy audit changed", err, count)
	}
}

func TestUsageKeysetSameTimestampAndBound(t *testing.T) {
	_, r := usageTestDB(t)
	ctx := context.Background()
	started := time.Now().UTC().Truncate(time.Microsecond)
	for i := 20; i < 24; i++ {
		v := usageFinish(usageRecord(i, nil), "succeeded")
		v.StartedAt = started
		v.Action = "server.update"
		if err := r.Upsert(ctx, &v); err != nil {
			t.Fatal(err)
		}
	}
	f := UsageLogFilter{From: started.Add(-time.Hour), To: started.Add(time.Hour)}
	first, bound, err := r.FirstPage(ctx, f, 2)
	if err != nil || len(first) != 2 {
		t.Fatal(err, first)
	}
	late := usageFinish(usageRecord(24, nil), "succeeded")
	late.StartedAt = started
	late.Action = "server.update"
	if err := r.Upsert(ctx, &late); err != nil {
		t.Fatal(err)
	}
	last := first[len(first)-1]
	second, err := r.List(ctx, f, bound, &last.StartedAt, last.ID, 2)
	if err != nil || len(second) != 2 {
		t.Fatal(err, second)
	}
	ids := map[uint64]bool{}
	previous := bound + 1
	for _, v := range append(first, second...) {
		if ids[v.ID] || v.ID >= previous || v.ID > bound {
			t.Fatalf("unstable keyset: %+v", v)
		}
		ids[v.ID] = true
		previous = v.ID
	}
	last = second[len(second)-1]
	end, err := r.List(ctx, f, bound, &last.StartedAt, last.ID, 2)
	if err != nil || len(end) != 0 {
		t.Fatal(err, end)
	}
}

func TestUsageBoundAllowsLowerIDLateCommit(t *testing.T) {
	db, r := usageTestDB(t)
	ctx := context.Background()
	started := time.Now().UTC().Truncate(time.Microsecond)
	late := usageFinish(usageRecord(30, nil), "succeeded")
	late.StartedAt = started
	late.Action = "server.update"
	late.RetentionAt = late.FinishedAt
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Create(&late).Error; err != nil {
		t.Fatal(err)
	}
	visible := usageFinish(usageRecord(31, nil), "succeeded")
	visible.StartedAt = started
	visible.Action = "server.update"
	if err := r.Upsert(ctx, &visible); err != nil {
		t.Fatal(err)
	}
	f := UsageLogFilter{From: started.Add(-time.Hour), To: started.Add(time.Hour)}
	first, bound, err := r.FirstPage(ctx, f, 1)
	if err != nil || len(first) != 1 || first[0].ID != visible.ID || bound != visible.ID {
		t.Fatalf("unexpected initial bound/page: %v %d %v", first, bound, err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	next, err := r.List(ctx, f, bound, &first[0].StartedAt, first[0].ID, 1)
	if err != nil || len(next) != 1 || next[0].ID != late.ID {
		t.Fatalf("late commit boundary undocumented: %v %v", next, err)
	}
}

func TestUsageBackfillFinalPassRepairsCheckpointHoles(t *testing.T) {
	db, r := usageTestDB(t, UsageLogRepoConfig{BatchSize: 1})
	ctx := context.Background()
	audit := NewAuditEventRepo(db)
	var events []model.AuditEvent
	for i := 0; i < 3; i++ {
		v := model.AuditEvent{Username: "legacy", Action: "service.credentials", ResourceType: "service", ResourceID: 1}
		if err := audit.Create(ctx, &v); err != nil {
			t.Fatal(err)
		}
		events = append(events, v)
	}
	state := model.UsageLogBackfillState{ID: usageBackfillID, Checkpoint: uint64(events[2].ID), FinalBound: uint64(events[2].ID)}
	if err := db.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.UsageLog{}).Count(&count).Error; err != nil || count != 3 {
		t.Fatalf("checkpoint holes not repaired: %v %d", err, count)
	}
	if err := db.First(&state, "id = ?", usageBackfillID).Error; err != nil || state.CompletedAt == nil || state.Imported != 3 {
		t.Fatalf("backfill not sealed: %+v %v", state, err)
	}
}

func TestUsageBackfillHonorsDedicatedSessionLock(t *testing.T) {
	db, r := usageTestDB(t)
	ctx := context.Background()
	audit := NewAuditEventRepo(db)
	event := model.AuditEvent{Username: "legacy", Action: "api_key.reveal"}
	if err := audit.Create(ctx, &event); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", usageBackfillLock); err != nil {
		t.Fatal(err)
	}
	if err := r.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.UsageLog{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("concurrent migrator acquired lock: %v %d", err, count)
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", usageBackfillLock); err != nil {
		t.Fatal(err)
	}
	if err := r.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLog{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("released session failed migration: %v %d", err, count)
	}
}

func TestUsageCleanupSkipsLockedRenewingOwner(t *testing.T) {
	db, r := usageTestDB(t, UsageLogRepoConfig{OrdinaryRetention: time.Hour})
	ctx := context.Background()
	owner := usageUUID(108)
	if err := r.RegisterInstance(ctx, owner); err != nil {
		t.Fatal(err)
	}
	v := usageRecord(32, &owner)
	if err := r.Upsert(ctx, &v); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := db.Model(&model.UsageLog{}).Where("id = ?", v.ID).Updates(map[string]interface{}{"outcome": "unknown", "recovery_reason": "owner_lease_expired", "reconciled_at": old, "retention_at": old}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner).Update("lease_until", old).Error; err != nil {
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
	if err := r.Cleanup(timeout); err != nil {
		t.Fatal("cleanup blocked behind renewal", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, v.ID); err != nil {
		t.Fatal("concurrent renewal lost", err)
	}
}

func TestUsageErrorsContainNoDatabaseInput(t *testing.T) {
	_, r := usageTestDB(t)
	v := usageFinish(usageRecord(33, nil), "succeeded")
	v.Action = "server.update"
	v.OperationID = "SECRET_MARKER_INVALID_UUID"
	err := r.Upsert(context.Background(), &v)
	if !errors.Is(err, ErrUsageStorage) || err.Error() != "usage_storage_unavailable" {
		t.Fatalf("unsafe persistence error: %v", err)
	}
}

func TestUsageReconcileHasOneGlobalRowBudget(t *testing.T) {
	db, r := usageTestDB(t, UsageLogRepoConfig{BatchSize: 2})
	ctx := context.Background()
	for n := 110; n < 112; n++ {
		owner := usageUUID(n)
		if err := r.RegisterInstance(ctx, owner); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			v := usageRecord(n*10+i, &owner)
			if err := r.Upsert(ctx, &v); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner).Update("lease_until", time.Now().Add(-time.Minute)).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Reconcile(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.UsageLog{}).Where("outcome = 'unknown'").Count(&count).Error; err != nil || count != 2 || r.ReconciledCount() != 2 {
		t.Fatalf("batch exceeded global row budget: %d %d %v", count, r.ReconciledCount(), err)
	}
	if err := r.Reconcile(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLog{}).Where("outcome = 'unknown'").Count(&count).Error; err != nil || count != 4 || r.ReconciledCount() != 4 {
		t.Fatalf("next batch did not continue: %d %d %v", count, r.ReconciledCount(), err)
	}
}

func TestUsageMissingOwnerIsUnknownAndCannotBeRegisteredAgain(t *testing.T) {
	db, r := usageTestDB(t)
	ctx := context.Background()
	owner := usageUUID(112)
	if err := r.RegisterInstance(ctx, owner); err != nil {
		t.Fatal(err)
	}
	v := usageRecord(34, &owner)
	if err := r.Upsert(ctx, &v); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&model.UsageLogInstance{}, "instance_id = ?", owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	got := requireUsage(t, r, v)
	if got.Outcome != "unknown" || got.RecoveryReason != "owner_missing" || got.RetentionAt == nil || got.FinishedAt != nil {
		t.Fatalf("missing owner not recovered: %+v", got)
	}
	if err := r.RegisterInstance(ctx, owner); !errors.Is(err, ErrUsageOwnerMissing) {
		t.Fatalf("missing owner silently recreated: %v", err)
	}
}

func TestUsageNoLongerTrackedRecoveredUnknownKeepsRetention(t *testing.T) {
	db, r := usageTestDB(t, UsageLogRepoConfig{ReconcileGrace: time.Microsecond})
	ctx := context.Background()
	owner := usageUUID(113)
	if err := r.RegisterInstance(ctx, owner); err != nil {
		t.Fatal(err)
	}
	v := usageRecord(35, &owner)
	if err := r.Upsert(ctx, &v); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner).Update("lease_until", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	before := requireUsage(t, r, v)
	if err := r.Heartbeat(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, owner, nil); err != nil {
		t.Fatal(err)
	}
	after := requireUsage(t, r, v)
	if after.RecoveryReason != "finalization_unavailable" || !after.RetentionAt.Equal(*before.RetentionAt) || !after.ReconciledAt.Equal(*before.ReconciledAt) {
		t.Fatalf("lost finalization extended unknown ttl: before=%+v after=%+v", before, after)
	}
}
