package repository

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vpsmanager/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	ErrUsageOwnerMissing = errors.New("owner_missing")
	ErrUsageOwnerRetired = errors.New("owner_retired")
	ErrUsageLeaseExpired = errors.New("owner_lease_expired")
	ErrUsageInvalid      = errors.New("invalid_usage_record")
	ErrUsageStorage      = errors.New("usage_storage_unavailable")
	ErrUsagePermanent    = errors.New("usage_storage_permanent_failure")
)

type UsageLogRepoConfig struct {
	LeaseDuration, OrdinaryRetention, SensitiveRetention, ReconcileGrace time.Duration
	BatchSize                                                            int
}

type UsageLogFilter struct {
	From, To                                           time.Time
	Action, Outcome, AuthType, ResourceType, RequestID string
	UserID, APIKeyID, ResourceID, ServerID             *uint
}

type UsageLogRepo struct {
	db               *gorm.DB
	config           UsageLogRepoConfig
	reconciled       atomic.Uint64
	cleaned          atomic.Uint64
	cleanedInstances atomic.Uint64
	cleanupReported  atomic.Bool
	reportSlot       chan struct{}
}

func NewUsageLogRepo(db *gorm.DB, configs ...UsageLogRepoConfig) *UsageLogRepo {
	c := UsageLogRepoConfig{ReconcileGrace: 30 * time.Second, BatchSize: 200}
	if len(configs) > 0 {
		v := configs[0]
		if v.LeaseDuration > 0 {
			c.LeaseDuration = v.LeaseDuration
		}
		if v.OrdinaryRetention > 0 {
			c.OrdinaryRetention = v.OrdinaryRetention
		}
		if v.SensitiveRetention > 0 {
			c.SensitiveRetention = v.SensitiveRetention
		}
		if v.ReconcileGrace > 0 {
			c.ReconcileGrace = v.ReconcileGrace
		}
		if v.BatchSize > 0 {
			c.BatchSize = v.BatchSize
		}
	}
	policy := (model.UsagePolicy{LeaseDuration: c.LeaseDuration, OrdinaryRetention: c.OrdinaryRetention, SensitiveRetention: c.SensitiveRetention}).WithDefaults()
	c.LeaseDuration, c.OrdinaryRetention, c.SensitiveRetention = policy.LeaseDuration, policy.OrdinaryRetention, policy.SensitiveRetention
	// Persistence failures are reported with fixed codes by the recorder. GORM
	// must not also emit SQL containing snapshots or legacy audit free text.
	return &UsageLogRepo{db: db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}), config: c, reportSlot: make(chan struct{}, 1)}
}

func usageErr(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrUsageOwnerMissing, ErrUsageOwnerRetired, ErrUsageLeaseExpired, ErrUsageInvalid, ErrUsagePermanent, ErrUsageStorage, gorm.ErrRecordNotFound, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	for _, invalid := range []error{gorm.ErrInvalidData, gorm.ErrInvalidField, gorm.ErrInvalidValue, gorm.ErrDuplicatedKey, gorm.ErrForeignKeyViolated} {
		if errors.Is(err, invalid) {
			return ErrUsageInvalid
		}
	}
	var coded interface{ SQLState() string }
	if errors.As(err, &coded) {
		code := coded.SQLState()
		if len(code) >= 2 {
			switch code[:2] {
			case "22", "23": // Data and integrity failures cannot improve on retry.
				return ErrUsageInvalid
			case "0A", "28", "3D", "3F", "42": // Unsupported, authorization, or schema errors.
				return ErrUsagePermanent
			}
		}
	}
	return ErrUsageStorage
}

func databaseNow(tx *gorm.DB) (time.Time, error) {
	var now time.Time
	err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error
	return now, err
}

func (r *UsageLogRepo) lockOwner(tx *gorm.DB, id, strength string, valid bool) (*model.UsageLogInstance, time.Time, error) {
	var owner model.UsageLogInstance
	if err := tx.Clauses(clause.Locking{Strength: strength}).Where("instance_id = ?", id).First(&owner).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, time.Time{}, ErrUsageOwnerMissing
		}
		return nil, time.Time{}, err
	}
	now, err := databaseNow(tx)
	if err != nil {
		return nil, now, err
	}
	if owner.RetiredAt != nil {
		return &owner, now, ErrUsageOwnerRetired
	}
	if valid && !owner.LeaseUntil.After(now) {
		return &owner, now, ErrUsageLeaseExpired
	}
	return &owner, now, nil
}

func (r *UsageLogRepo) RegisterInstance(ctx context.Context, id string) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var missingReferenced bool
		if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM usage_logs WHERE owner_instance_id = ?) AND NOT EXISTS (SELECT 1 FROM usage_log_instances WHERE instance_id = ?)", id, id).Scan(&missingReferenced).Error; err != nil {
			return err
		}
		if missingReferenced {
			return ErrUsageOwnerMissing
		}
		now, err := databaseNow(tx)
		if err != nil {
			return err
		}
		owner := model.UsageLogInstance{InstanceID: id, LastSeenAt: now, LeaseUntil: now.Add(r.config.LeaseDuration)}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&owner)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			_, _, err = r.lockOwner(tx, id, "UPDATE", true)
		}
		return err
	})
	return usageErr(err)
}

func (r *UsageLogRepo) Heartbeat(ctx context.Context, id string) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, now, err := r.lockOwner(tx, id, "UPDATE", false)
		if err != nil {
			return err
		}
		return tx.Model(&model.UsageLogInstance{}).Where("instance_id = ?", id).Updates(map[string]interface{}{"last_seen_at": now, "lease_until": now.Add(r.config.LeaseDuration)}).Error
	})
	return usageErr(err)
}

func recoveryUnknown(v *model.UsageLog) bool {
	return v.Outcome == "unknown" && (v.RecoveryReason == "owner_lease_expired" || v.RecoveryReason == "finalization_unavailable")
}
func sameOwner(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func (r *UsageLogRepo) retention(action string) time.Duration {
	return (model.UsagePolicy{OrdinaryRetention: r.config.OrdinaryRetention, SensitiveRetention: r.config.SensitiveRetention}).Retention(action)
}

func (r *UsageLogRepo) Upsert(ctx context.Context, input *model.UsageLog) error {
	if input == nil || input.OperationID == "" || input.StartedAt.IsZero() || input.Action == "" {
		return ErrUsageInvalid
	}
	v := *input
	v.StartedAt = v.StartedAt.UTC().Truncate(time.Microsecond)
	if v.Source == "" {
		v.Source = "operation"
	}
	if v.AuthType == "" {
		v.AuthType = "unauthenticated"
	}
	switch v.Outcome {
	case "running", "succeeded", "failed", "rejected", "cancelled", "unknown":
	default:
		return ErrUsageInvalid
	}
	if v.Outcome == "running" && v.OwnerInstanceID == nil {
		return ErrUsageOwnerMissing
	}
	if len(v.Metadata) == 0 {
		v.Metadata = json.RawMessage(`{}`)
	}
	if len(v.Metadata) > 4096 || !json.Valid(v.Metadata) {
		return ErrUsageInvalid
	}
	if v.Outcome == "running" {
		v.FinishedAt = nil
		v.DurationMS = nil
		v.RetentionAt = nil
		v.ReconciledAt = nil
		v.RecoveryReason = ""
	} else if v.FinishedAt != nil {
		v.RetentionAt = v.FinishedAt
	} else if v.Source == "audit_legacy" {
		v.RetentionAt = &v.StartedAt
	} else if v.ReconciledAt != nil {
		v.RetentionAt = v.ReconciledAt
	} else {
		return ErrUsageInvalid
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var now time.Time
		var err error
		if v.OwnerInstanceID != nil {
			_, now, err = r.lockOwner(tx, *v.OwnerInstanceID, "SHARE", true)
		} else {
			now, err = databaseNow(tx)
		}
		if err != nil {
			return err
		}
		var old model.UsageLog
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_id = ?", v.OperationID).First(&old).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if v.RetentionAt != nil && !v.RetentionAt.Add(r.retention(v.Action)).After(now) {
				return nil
			}
			v.ID = 0
			v.RecordedAt = now
			created := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "operation_id"}}, DoNothing: true}).Create(&v)
			if created.Error != nil {
				return created.Error
			}
			if created.RowsAffected > 0 {
				input.ID = v.ID
				return nil
			}
			err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_id = ?", v.OperationID).First(&old).Error
		}
		if err != nil {
			return err
		}
		input.ID = old.ID
		if !old.StartedAt.Equal(v.StartedAt) || old.AuthType != v.AuthType || old.Source != v.Source || !sameOwner(old.OwnerInstanceID, v.OwnerInstanceID) {
			return ErrUsageInvalid
		}
		if old.Outcome != "running" && !recoveryUnknown(&old) {
			return nil
		}
		if v.StateSeq <= old.StateSeq {
			return nil
		}
		if recoveryUnknown(&old) && v.Outcome == "running" && old.RecoveryReason != "owner_lease_expired" {
			return nil
		}
		values := map[string]interface{}{
			"finished_at": v.FinishedAt, "duration_ms": v.DurationMS, "reconciled_at": v.ReconciledAt, "retention_at": v.RetentionAt, "outcome": v.Outcome, "phase": v.Phase, "state_seq": v.StateSeq, "recovery_reason": v.RecoveryReason,
			"resource_type": v.ResourceType, "resource_id": v.ResourceID, "resource_name_snapshot": v.ResourceNameSnapshot, "server_id": v.ServerID, "user_id": v.UserID, "username_snapshot": v.UsernameSnapshot, "api_key_id": v.APIKeyID, "api_key_name_snapshot": v.APIKeyNameSnapshot, "api_key_prefix_snapshot": v.APIKeyPrefixSnapshot,
			"request_id": v.RequestID, "method": v.Method, "route_pattern": v.RoutePattern, "http_status": v.HTTPStatus, "client_address": v.ClientAddress, "exit_code": v.ExitCode, "upstream_status": v.UpstreamStatus, "error_reason": v.ErrorReason, "metadata": clause.Expr{SQL: "?::jsonb", Vars: []interface{}{string(v.Metadata)}}, "legacy_audit_event_id": v.LegacyAuditEventID,
		}
		return tx.Model(&model.UsageLog{}).Where("id = ?", old.ID).Updates(values).Error
	})
	return usageErr(err)
}

func usageQuery(tx *gorm.DB, f UsageLogFilter) *gorm.DB {
	q := tx.Model(&model.UsageLog{}).Where("started_at >= ? AND started_at < ?", f.From, f.To)
	for col, v := range map[string]string{"action": f.Action, "outcome": f.Outcome, "auth_type": f.AuthType, "resource_type": f.ResourceType, "request_id": f.RequestID} {
		if v != "" {
			q = q.Where(col+" = ?", v)
		}
	}
	for col, v := range map[string]*uint{"user_id": f.UserID, "api_key_id": f.APIKeyID, "resource_id": f.ResourceID, "server_id": f.ServerID} {
		if v != nil {
			q = q.Where(col+" = ?", *v)
		}
	}
	return q
}

func listUsage(tx *gorm.DB, f UsageLogFilter, bound uint64, afterTime *time.Time, afterID uint64, limit int) ([]model.UsageLog, error) {
	if limit < 1 || limit > 101 {
		return nil, ErrUsageInvalid
	}
	q := usageQuery(tx, f).Where("id <= ?", bound)
	if afterTime != nil {
		q = q.Where("(started_at,id) < (?,?)", *afterTime, afterID)
	}
	items := make([]model.UsageLog, 0)
	err := q.Order("started_at DESC, id DESC").Limit(limit).Find(&items).Error
	return items, err
}

func (r *UsageLogRepo) Get(ctx context.Context, id uint64) (*model.UsageLog, error) {
	if id == 0 || id > uint64(1<<63-1) {
		return nil, gorm.ErrRecordNotFound
	}
	var v model.UsageLog
	err := r.db.WithContext(ctx).First(&v, id).Error
	if err != nil {
		return nil, usageErr(err)
	}
	return &v, nil
}
func (r *UsageLogRepo) List(ctx context.Context, f UsageLogFilter, bound uint64, afterTime *time.Time, afterID uint64, limit int) ([]model.UsageLog, error) {
	v, e := listUsage(r.db.WithContext(ctx), f, bound, afterTime, afterID, limit)
	return v, usageErr(e)
}
func (r *UsageLogRepo) FirstPage(ctx context.Context, f UsageLogFilter, limit int) (items []model.UsageLog, bound uint64, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY").Error; e != nil {
			return e
		}
		if e := tx.Model(&model.UsageLog{}).Select("COALESCE(MAX(id),0)").Scan(&bound).Error; e != nil {
			return e
		}
		var e error
		items, e = listUsage(tx, f, bound, nil, 0, limit)
		return e
	})
	return items, bound, usageErr(err)
}

func (r *UsageLogRepo) OwnerUnknown(ctx context.Context, id string) ([]string, error) {
	ids := make([]string, 0)
	err := r.db.WithContext(ctx).Model(&model.UsageLog{}).Where("owner_instance_id = ? AND outcome = 'unknown' AND recovery_reason = 'owner_lease_expired'", id).Order("id").Limit(r.config.BatchSize).Pluck("operation_id", &ids).Error
	return ids, usageErr(err)
}

func (r *UsageLogRepo) ReconciledCount() uint64      { return r.reconciled.Load() }
func (r *UsageLogRepo) CleanedCount() uint64         { return r.cleaned.Load() }
func (r *UsageLogRepo) CleanedInstanceCount() uint64 { return r.cleanedInstances.Load() }

// Reconcile never invents a business result or advances an owner's sequence.
// The static tracked list remains supported for callers without local state.
func (r *UsageLogRepo) Reconcile(ctx context.Context, ownerID string, tracked []string) error {
	return r.ReconcileTracked(ctx, ownerID, func() []string { return tracked })
}

// ReconcileTracked takes the in-memory tracking snapshot after locking the
// local owner. New inserts must take that owner's SHARE lock, so a Begin that
// arrives after this snapshot cannot persist a row until reconciliation ends.
// snapshot must only read memory and must never wait on database work.
func (r *UsageLogRepo) ReconcileTracked(ctx context.Context, ownerID string, snapshot func() []string) error {
	if ownerID == "" {
		ownerID = "00000000-0000-0000-0000-000000000000"
	}
	var owners []string
	err := r.db.WithContext(ctx).Table("usage_logs AS logs").Joins("LEFT JOIN usage_log_instances AS instances ON logs.owner_instance_id = instances.instance_id").Where("(logs.outcome = 'running' AND (instances.instance_id IS NULL OR instances.retired_at IS NOT NULL OR instances.lease_until <= clock_timestamp() OR logs.owner_instance_id = ?)) OR (logs.owner_instance_id = ? AND logs.outcome = 'unknown' AND logs.recovery_reason = 'owner_lease_expired')", ownerID, ownerID).Distinct("logs.owner_instance_id").Limit(r.config.BatchSize).Pluck("logs.owner_instance_id", &owners).Error
	if err != nil {
		return usageErr(err)
	}
	remaining := r.config.BatchSize
	for _, id := range owners {
		if remaining <= 0 {
			break
		}
		var changed uint64
		if id == "" {
			continue
		}
		err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var owner model.UsageLogInstance
			e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("instance_id = ?", id).First(&owner).Error
			missing := errors.Is(e, gorm.ErrRecordNotFound)
			if e != nil && !missing {
				return e
			}
			// SKIP LOCKED must not be mistaken for a missing owner.
			if missing {
				var n int64
				if e = tx.Model(&model.UsageLogInstance{}).Where("instance_id = ?", id).Count(&n).Error; e != nil {
					return e
				}
				if n > 0 {
					return nil
				}
			}
			now, e := databaseNow(tx)
			if e != nil {
				return e
			}
			stale := missing || owner.RetiredAt != nil || !owner.LeaseUntil.After(now)
			if !stale && id != ownerID {
				return nil
			}
			var tracked []string
			seen := map[string]bool{}
			if !stale && snapshot != nil {
				tracked = snapshot()
				for _, operationID := range tracked {
					seen[operationID] = true
				}
			}
			var records []model.UsageLog
			q := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("owner_instance_id = ?", id)
			if stale {
				q = q.Where("outcome = 'running'")
			} else {
				q = q.Where("outcome = 'running' OR (outcome = 'unknown' AND recovery_reason = 'owner_lease_expired')")
				q = q.Where("recorded_at <= ?", now.Add(-r.config.ReconcileGrace))
				if len(tracked) > 0 {
					q = q.Where("operation_id NOT IN ?", tracked)
				}
			}
			if e = q.Order("id").Limit(remaining).Find(&records).Error; e != nil {
				return e
			}
			for _, v := range records {
				if !stale && (seen[v.OperationID] || v.RecordedAt.Add(r.config.ReconcileGrace).After(now)) {
					continue
				}
				reason := "finalization_unavailable"
				if missing {
					reason = "owner_missing"
				} else if stale {
					reason = "owner_lease_expired"
				}
				values := map[string]interface{}{"outcome": "unknown", "recovery_reason": reason}
				if v.ReconciledAt == nil {
					values["reconciled_at"] = now
					values["retention_at"] = now
				}
				updated := tx.Model(&model.UsageLog{}).Where("id = ? AND outcome IN ('running','unknown')", v.ID).Updates(values)
				if e = updated.Error; e != nil {
					return e
				}
				changed += uint64(updated.RowsAffected)
			}
			return nil
		})
		if err != nil {
			return usageErr(err)
		}
		r.reconciled.Add(changed)
		remaining -= int(changed)
	}
	return nil
}

// CleanupReport contains only fixed dimensions. Arbitrary historical action,
// source, and outcome text is grouped under "other", never emitted to logs.
type UsageCleanupGroupCount struct {
	Source  string `json:"source"`
	Action  string `json:"action"`
	Outcome string `json:"outcome"`
	Count   int64  `json:"count"`
}

var usageReportActions = []string{
	"server.create", "server.update", "server.delete", "server.host_key.trust", "server.exec", "server.terminal",
	"credential.create", "credential.update", "credential.delete", "credential.reveal",
	"api_key.create", "api_key.delete", "api_key.reveal",
	"service.create", "service.update", "service.delete", "service.relay", "service.credentials",
}

func (r *UsageLogRepo) cleanupCandidates(db *gorm.DB) *gorm.DB {
	return db.Table("usage_logs AS logs").Where("logs.outcome <> 'running' AND logs.retention_at IS NOT NULL AND logs.retention_at <= clock_timestamp() - (CASE WHEN logs.action IN ? OR RIGHT(logs.action, ?) = ? THEN ?::double precision ELSE ?::double precision END * INTERVAL '1 second')", model.UsageSensitiveActions(), len(model.UsageDeleteActionSuffix), model.UsageDeleteActionSuffix, r.config.SensitiveRetention.Seconds(), r.config.OrdinaryRetention.Seconds()).Where("logs.source <> 'audit_legacy' OR EXISTS (SELECT 1 FROM usage_log_backfill_states WHERE id = ? AND completed_at IS NOT NULL)", usageBackfillID).Where("NOT (logs.outcome = 'unknown' AND logs.recovery_reason = 'owner_lease_expired') OR NOT EXISTS (SELECT 1 FROM usage_log_instances AS i WHERE i.instance_id = logs.owner_instance_id AND i.retired_at IS NULL AND i.lease_until > clock_timestamp())")
}

func (r *UsageLogRepo) cleanupReport(db *gorm.DB) ([]UsageCleanupGroupCount, error) {
	groups := make([]UsageCleanupGroupCount, 0)
	err := r.cleanupCandidates(db).Select("CASE WHEN logs.source IN ? THEN logs.source ELSE 'other' END AS source, CASE WHEN logs.action IN ? THEN logs.action ELSE 'other' END AS action, CASE WHEN logs.outcome IN ? THEN logs.outcome ELSE 'other' END AS outcome, COUNT(*) AS count", []string{"operation", "audit_legacy"}, usageReportActions, []string{"succeeded", "failed", "rejected", "cancelled", "unknown"}).Group("1,2,3").Order("1,2,3").Scan(&groups).Error
	return groups, usageErr(err)
}

func (r *UsageLogRepo) CleanupReport(ctx context.Context) ([]UsageCleanupGroupCount, error) {
	return r.cleanupReport(r.db.WithContext(ctx))
}

// PrepareCleanup reports eligibility before enabling deletion. A caller can
// allow a larger read budget than each subsequent small deletion batch.
func (r *UsageLogRepo) PrepareCleanup(ctx context.Context) error {
	return r.ensureCleanupReported(ctx)
}

func logCleanupReport(scope string, groups []UsageCleanupGroupCount) {
	var total int64
	for _, group := range groups {
		total += group.Count
	}
	slog.Info("usage retention cleanup eligibility", "scope", scope, "eligible", total, "groups", groups)
}

func (r *UsageLogRepo) ensureCleanupReported(ctx context.Context) error {
	if r.cleanupReported.Load() {
		return nil
	}
	select {
	case r.reportSlot <- struct{}{}:
		defer func() { <-r.reportSlot }()
	default:
		return ErrUsageStorage
	}
	if r.cleanupReported.Load() {
		return nil
	}
	groups, err := r.CleanupReport(ctx)
	if err != nil {
		return err
	}
	logCleanupReport("initial_cleanup", groups)
	r.cleanupReported.Store(true)
	return nil
}

func (r *UsageLogRepo) Cleanup(ctx context.Context) error {
	// A failed report leaves cleanup disabled until a later successful report.
	if err := r.ensureCleanupReported(ctx); err != nil {
		return err
	}
	// Candidates are read without row locks, then each transaction locks owner
	// before log. Every eligibility check is repeated under those locks.
	var ids []uint64
	err := r.cleanupCandidates(r.db.WithContext(ctx)).Order("logs.retention_at,logs.id").Limit(r.config.BatchSize).Pluck("logs.id", &ids).Error
	if err != nil {
		return usageErr(err)
	}
	for _, id := range ids {
		var deleted int64
		err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var candidate model.UsageLog
			if e := tx.First(&candidate, id).Error; e != nil {
				if errors.Is(e, gorm.ErrRecordNotFound) {
					return nil
				}
				return e
			}
			var owner *model.UsageLogInstance
			if candidate.OwnerInstanceID != nil {
				var v model.UsageLogInstance
				e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("instance_id = ?", *candidate.OwnerInstanceID).First(&v).Error
				if e == nil {
					owner = &v
				} else if !errors.Is(e, gorm.ErrRecordNotFound) {
					return e
				} else {
					var n int64
					if e = tx.Model(&model.UsageLogInstance{}).Where("instance_id = ?", *candidate.OwnerInstanceID).Count(&n).Error; e != nil {
						return e
					}
					if n > 0 {
						return nil
					}
				}
			}
			var v model.UsageLog
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).First(&v, id).Error; e != nil {
				if errors.Is(e, gorm.ErrRecordNotFound) {
					return nil
				}
				return e
			}
			now, e := databaseNow(tx)
			if e != nil {
				return e
			}
			if v.Outcome == "running" || v.RetentionAt == nil || v.RetentionAt.Add(r.retention(v.Action)).After(now) {
				return nil
			}
			if v.Source == "audit_legacy" {
				var count int64
				if e = tx.Model(&model.UsageLogBackfillState{}).Where("id = ? AND completed_at IS NOT NULL", usageBackfillID).Count(&count).Error; e != nil {
					return e
				}
				if count == 0 {
					return nil
				}
			}
			if v.Outcome == "unknown" && v.RecoveryReason == "owner_lease_expired" && owner != nil {
				if owner.RetiredAt == nil && owner.LeaseUntil.After(now) {
					return nil
				}
				if owner.RetiredAt == nil {
					if e = tx.Model(&model.UsageLogInstance{}).Where("instance_id = ?", owner.InstanceID).Update("retired_at", now).Error; e != nil {
						return e
					}
				}
			}
			result := tx.Delete(&v)
			deleted = result.RowsAffected
			return result.Error
		})
		if err != nil {
			return usageErr(err)
		}
		r.cleaned.Add(uint64(deleted))
	}
	return r.cleanupInstances(ctx)
}

func (r *UsageLogRepo) cleanupInstances(ctx context.Context) error {
	var owners []string
	err := r.db.WithContext(ctx).Model(&model.UsageLogInstance{}).Where("(retired_at IS NOT NULL OR lease_until <= clock_timestamp()) AND NOT EXISTS (SELECT 1 FROM usage_logs WHERE owner_instance_id = usage_log_instances.instance_id)").Order("lease_until, instance_id").Limit(r.config.BatchSize).Pluck("instance_id", &owners).Error
	if err != nil {
		return usageErr(err)
	}
	for _, id := range owners {
		var deleted int64
		err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var owner model.UsageLogInstance
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).First(&owner, "instance_id = ?", id).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			now, err := databaseNow(tx)
			if err != nil {
				return err
			}
			if owner.RetiredAt == nil && owner.LeaseUntil.After(now) {
				return nil
			}
			// Inserts hold the owner's SHARE lock, so references cannot appear
			// between this recheck and deletion. Never remove a healthy owner.
			result := tx.Where("instance_id = ? AND NOT EXISTS (SELECT 1 FROM usage_logs WHERE owner_instance_id = ?)", id, id).Delete(&model.UsageLogInstance{})
			deleted = result.RowsAffected
			return result.Error
		})
		if err != nil {
			return usageErr(err)
		}
		r.cleanedInstances.Add(uint64(deleted))
	}
	return nil
}

const usageBackfillID = "usage-log-audit-legacy-v2"
const usageBackfillLock int64 = 734864170319

func (r *UsageLogRepo) BackfillStatus(ctx context.Context) (*model.UsageLogBackfillState, error) {
	var state model.UsageLogBackfillState
	err := r.db.WithContext(ctx).First(&state, "id = ?", usageBackfillID).Error
	if err != nil {
		return nil, usageErr(err)
	}
	return &state, nil
}

func legacyOperationID(id uint) string {
	b := sha1.Sum([]byte(fmt.Sprintf("talus/usage-log/audit-legacy/v2/%d", id)))
	b[6] = (b[6] & 15) | 80
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func legacyLabel(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(value, ""))
	if len(value) > 256 {
		value = value[:256]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}
func legacyAddress(value string) string {
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
	}
	return ""
}

// Backfill is authorized only after legacy writers and their transactions have
// drained. Every batch uses the same connection as the session advisory lock.
func (r *UsageLogRepo) Backfill(ctx context.Context) error {
	err := r.db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		// Connection() gives a non-cloning DB handle; take a cloning session so
		// First's ErrRecordNotFound and query clauses cannot poison later batches.
		conn = conn.Session(&gorm.Session{NewDB: true})
		var locked bool
		if e := conn.Raw("SELECT pg_try_advisory_lock(?)", usageBackfillLock).Scan(&locked).Error; e != nil {
			return e
		}
		if !locked {
			return nil
		}
		defer func() {
			release, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			conn.WithContext(release).Exec("SELECT pg_advisory_unlock(?)", usageBackfillLock)
		}()
		var state model.UsageLogBackfillState
		e := conn.Where("id = ?", usageBackfillID).First(&state).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			state.ID = usageBackfillID
			if e = conn.Model(&model.AuditEvent{}).Where("operation_id IS NULL").Select("COALESCE(MAX(id),0)").Scan(&state.FinalBound).Error; e != nil {
				return e
			}
			if e = conn.Create(&state).Error; e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
		if state.CompletedAt != nil {
			return nil
		}
		// A final full pass is safe because cleanup is disabled until complete.
		for pass := 0; pass < 2; pass++ {
			position := state.Checkpoint
			if pass == 1 {
				position = 0
			}
			for {
				var events []model.AuditEvent
				if e = conn.Where("operation_id IS NULL AND id > ? AND id <= ?", position, state.FinalBound).Order("id").Limit(r.config.BatchSize).Find(&events).Error; e != nil {
					return e
				}
				if len(events) == 0 {
					break
				}
				e = conn.Transaction(func(tx *gorm.DB) error {
					for _, event := range events {
						// Legacy claims conflated API-key IDs and user IDs. Preserve a
						// safe historical label, never invent a trusted user association.
						v := model.UsageLog{OperationID: legacyOperationID(event.ID), LegacyAuditEventID: &event.ID, StartedAt: event.CreatedAt, RetentionAt: &event.CreatedAt, Outcome: "unknown", Phase: "closed", Action: event.Action, ResourceType: event.ResourceType, ResourceID: &event.ResourceID, AuthType: "legacy_unknown", UsernameSnapshot: legacyLabel(event.Username), ClientAddress: legacyAddress(event.IPAddress), Source: "audit_legacy", Metadata: json.RawMessage(`{}`)}
						if event.ResourceID == 0 {
							v.ResourceID = nil
						}
						inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&v)
						if inserted.Error != nil {
							return inserted.Error
						}
						state.Imported += uint64(inserted.RowsAffected)
					}
					position = uint64(events[len(events)-1].ID)
					if pass == 0 {
						state.Checkpoint = position
					}
					return tx.Model(&model.UsageLogBackfillState{}).Where("id = ?", state.ID).Updates(map[string]interface{}{"checkpoint": state.Checkpoint, "imported": state.Imported}).Error
				})
				if e != nil {
					return e
				}
			}
		}
		var groups []UsageCleanupGroupCount
		var completed bool
		e = conn.Transaction(func(tx *gorm.DB) error {
			updated := tx.Model(&model.UsageLogBackfillState{}).Where("id = ? AND completed_at IS NULL", state.ID).Update("completed_at", gorm.Expr("clock_timestamp()"))
			if updated.Error != nil {
				return updated.Error
			}
			completed = updated.RowsAffected > 0
			var err error
			groups, err = r.cleanupReport(tx.Where("logs.source = 'audit_legacy'"))
			return err
		})
		if e == nil && completed {
			// Complete and its cleanup report commit together: no cleaner can
			// delete legacy summaries before these counts have been captured.
			logCleanupReport("legacy_backfill", groups)
		}
		return e
	})
	return usageErr(err)
}
