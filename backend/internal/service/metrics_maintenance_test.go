package service

import (
	"testing"

	"github.com/vpsmanager/backend/internal/config"
)

// TestReviewRetentionRolloutDecision pins the documented rollout: retention is
// off by default, a dry run reports without deleting, and only an explicitly
// enabled non-dry-run deletes anything.
func TestReviewRetentionRolloutDecision(t *testing.T) {
	for _, tc := range []struct {
		enabled, dryRun bool
		want            retentionDecisionKind
	}{
		{false, false, retentionDisabled},
		{false, true, retentionDisabled},
		{true, true, retentionDryRunOnly},
		{true, false, retentionApply},
	} {
		if got := retentionDecision(tc.enabled, tc.dryRun); got != tc.want {
			t.Fatalf("retentionDecision(%v, %v) = %v, want %v", tc.enabled, tc.dryRun, got, tc.want)
		}
	}
}

// TestRereviewOmittedDryRunKeepsTheRolloutDry is N01 end to end: the same chain the
// server wires (environment -> config.Load -> SetRetention -> decision) must land on
// dry run when the dry-run switch is omitted, and only an explicit false may select
// the applying decision.
func TestRereviewOmittedDryRunKeepsTheRolloutDry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dryRun  string
		want    retentionDecisionKind
		wantDry bool
	}{
		{"dry-run omitted", "", retentionDryRunOnly, true},
		{"dry-run explicitly on", "true", retentionDryRunOnly, true},
		{"dry-run explicitly off", "false", retentionApply, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost/test")
			t.Setenv("VPSMANAGER_MASTER_KEY", "0123456789abcdef0123456789abcdef")
			t.Setenv("JWT_SECRET", "test-secret")
			t.Setenv("METRICS_RETENTION_ENABLED", "true")
			t.Setenv("METRICS_RETENTION_DRY_RUN", tc.dryRun)

			cfg := config.Load()
			if err := cfg.Validate(); err != nil {
				t.Fatalf("valid configuration rejected: %v", err)
			}
			if cfg.MetricsRetentionDryRun != tc.wantDry {
				t.Fatalf("dry_run = %v, want %v (METRICS_RETENTION_DRY_RUN=%q)",
					cfg.MetricsRetentionDryRun, tc.wantDry, tc.dryRun)
			}
			maintenance := NewMetricsMaintenance(nil)
			maintenance.SetRetention(cfg.MetricsRetentionEnabled, cfg.MetricsRetentionDryRun)
			if got := retentionDecision(maintenance.retentionEnabled, maintenance.retentionDryRun); got != tc.want {
				t.Fatalf("rollout decision = %v, want %v", got, tc.want)
			}
		})
	}
}
