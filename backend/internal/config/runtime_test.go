package config

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestRuntimeLoggerUpdatesDerivedLoggers(t *testing.T) {
	var buf bytes.Buffer
	r := NewRuntime(Config{LogLevel: "info", LogFormat: "json"})
	logger := slog.New(RuntimeLogHandler{Runtime: r, JSON: slog.NewJSONHandler(&buf, nil), Text: slog.NewTextHandler(&buf, nil)}).With("component", "test").WithGroup("group")
	logger.Debug("hidden")
	if buf.Len() != 0 {
		t.Fatal("debug ignored level")
	}
	r.Store(Config{LogLevel: "debug", LogFormat: "text"})
	logger.Debug("visible")
	if !strings.Contains(buf.String(), "level=DEBUG") || !strings.Contains(buf.String(), "component=test") {
		t.Fatalf("derived logger did not update: %s", buf.String())
	}
	if !logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug not enabled")
	}
}
func TestRuntimeSnapshotsAreConsistentUnderConcurrentUpdates(t *testing.T) {
	r := NewRuntime(Config{MonitorInterval: 5, SSHTimeout: 5})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 1000; n++ {
				c := r.Current()
				if c.MonitorInterval != c.SSHTimeout {
					t.Error("mixed snapshot")
					return
				}
			}
		}()
	}
	for n := 0; n < 1000; n++ {
		r.Store(Config{MonitorInterval: n, SSHTimeout: n})
	}
	wg.Wait()
}

func TestSystemSettingsOverridesMalformedRetentionEnvironmentByKey(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("VPSMANAGER_MASTER_KEY", "test-master-key")
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("METRICS_RETENTION_ENABLED", "invalid")
	t.Setenv("METRICS_RETENTION_DRY_RUN", "invalid")
	base := Load()
	if _, err := MergeSettings(*base, nil); err == nil {
		t.Fatal("malformed deployment accepted without overrides")
	}
	values := map[string]json.RawMessage{"METRICS_RETENTION_ENABLED": []byte(`false`)}
	if _, err := MergeSettings(*base, values); err == nil {
		t.Fatal("one override hid another malformed switch")
	}
	values["METRICS_RETENTION_DRY_RUN"] = []byte(`true`)
	current, err := MergeSettings(*base, values)
	if err != nil || current.MetricsRetentionEnabled || !current.MetricsRetentionDryRun {
		t.Fatalf("valid saved policy did not supersede deployment: %v", err)
	}
	values["METRICS_RETENTION_DRY_RUN"] = []byte(" \nnull ")
	if _, err := MergeSettings(*base, values); err == nil {
		t.Fatal("stored null accepted")
	}
}

func TestSystemSettingsDisplayCompatibleDeploymentDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("VPSMANAGER_MASTER_KEY", "test-master-key")
	t.Setenv("JWT_SECRET", "test-secret")
	for _, key := range []string{"USAGE_LOG_RETENTION_DAYS", "USAGE_LOG_SENSITIVE_RETENTION_DAYS", "USAGE_LOG_WRITE_CONCURRENCY", "USAGE_LOG_ACTIVE_LIMIT"} {
		t.Setenv(key, "0")
	}
	t.Setenv("LOG_LEVEL", "unknown")
	t.Setenv("LOG_FORMAT", "unknown")
	c := Load()
	if c.UsageRetentionDays != 30 || c.UsageSensitiveRetentionDays != 90 || c.UsageWriteConcurrency != 4 || c.UsageActiveLimit != 4096 || c.LogLevel != "info" || c.LogFormat != "text" {
		t.Fatal("settings display differs from consumer defaults")
	}
}
