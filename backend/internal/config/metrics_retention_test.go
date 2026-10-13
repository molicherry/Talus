package config

import "testing"

// TestRereviewInvalidDryRunCannotEnableDeletion pins F01: an unparsable rollout
// switch must be refused at startup and must never flip the safe dry-run default
// into deletion.
func TestRereviewInvalidDryRunCannotEnableDeletion(t *testing.T) {
	cases := []struct {
		name        string
		enabled     string
		dryRun      string
		wantErr     bool
		wantEnabled bool
		wantDryRun  bool
	}{
		{"valid enabled with dry run", "true", "true", false, true, true},
		{"an explicit dry-run off is honoured", "true", "false", false, true, false},
		// N01: "not configured" must not be read as "configured to the unsafe
		// value"; the safe default stays in place.
		{"enabled with dry-run omitted stays dry", "true", "", false, true, true},
		{"default is disabled and dry", "", "", false, false, true},
		{"deletion switch omitted leaves retention disabled", "", "false", false, false, false},
		{"unparsable dry run is refused", "true", "tru", true, true, true},
		{"unparsable enabled is refused", "yess", "true", true, false, true},
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
			// The fields, not just Validate: a switch that is unset or unparsable
			// must leave the value the loader installed, so the deleting
			// configuration can only ever be requested explicitly.
			if cfg.MetricsRetentionEnabled != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v (METRICS_RETENTION_ENABLED=%q)",
					cfg.MetricsRetentionEnabled, tc.wantEnabled, tc.enabled)
			}
			if cfg.MetricsRetentionDryRun != tc.wantDryRun {
				t.Fatalf("dry_run = %v, want %v (METRICS_RETENTION_DRY_RUN=%q)",
					cfg.MetricsRetentionDryRun, tc.wantDryRun, tc.dryRun)
			}
		})
	}
}

// TestRereviewEnabledRetentionWithoutDryRunStaysDry is the N01 reproduction: with
// deletion switched on and the dry-run switch omitted, the process must come up in
// dry run. Before the fix this reported enabled=true and dry_run=false.
func TestRereviewEnabledRetentionWithoutDryRunStaysDry(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("VPSMANAGER_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("METRICS_RETENTION_ENABLED", "true")
	t.Setenv("METRICS_RETENTION_DRY_RUN", "")

	cfg := Load()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}
	if !cfg.MetricsRetentionEnabled {
		t.Fatalf("enabled = false, want true (the switch was set)")
	}
	if !cfg.MetricsRetentionDryRun {
		t.Fatal("dry_run = false with the switch omitted: retention would delete on its next run")
	}
}
