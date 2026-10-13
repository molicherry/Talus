package config

import "testing"

// TestRereviewInvalidDryRunCannotEnableDeletion pins F01: an unparsable rollout
// switch must be refused at startup and must never flip the safe dry-run default
// into deletion.
func TestRereviewInvalidDryRunCannotEnableDeletion(t *testing.T) {
	cases := []struct {
		name    string
		enabled string
		dryRun  string
		wantErr bool
	}{
		{"valid enabled with dry run", "true", "true", false},
		{"valid enabled without dry run", "true", "false", false},
		{"default is disabled and dry", "", "", false},
		{"unparsable dry run is refused", "true", "tru", true},
		{"unparsable enabled is refused", "yess", "true", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Load() fatal-exits without the required variables.
			t.Setenv("DATABASE_URL", "postgres://localhost/test")
			t.Setenv("VPSMANAGER_MASTER_KEY", "0123456789abcdef0123456789abcdef")
			t.Setenv("JWT_SECRET", "test-secret")
			t.Setenv("METRICS_RETENTION_ENABLED", tc.enabled)
			t.Setenv("METRICS_RETENTION_DRY_RUN", tc.dryRun)
			cfg := Load()
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("an unparsable rollout switch must fail validation (enabled=%v dryRun=%v)",
					cfg.MetricsRetentionEnabled, cfg.MetricsRetentionDryRun)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("valid configuration rejected: %v", err)
			}
			// An unparsable value must never authorise deletion.
			if tc.dryRun == "tru" && cfg.MetricsRetentionEnabled && !cfg.MetricsRetentionDryRun {
				t.Fatal("an unparsable dry-run must not remove the dry-run protection")
			}
		})
	}
}
