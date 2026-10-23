package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/vpsmanager/backend/internal/config"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
)

type memorySettingsStore struct {
	sync.Mutex
	row    model.SystemSettings
	fail   bool
	audits []model.AuditEvent
}

func (s *memorySettingsStore) Load(context.Context) (model.SystemSettings, error) {
	s.Lock()
	defer s.Unlock()
	return s.row, nil
}
func (s *memorySettingsStore) Save(_ context.Context, revision uint64, row model.SystemSettings, audit *model.AuditEvent) error {
	s.Lock()
	defer s.Unlock()
	if s.fail {
		return errors.New("storage unavailable")
	}
	if revision != s.row.Revision {
		return repository.ErrSettingsConflict
	}
	s.row = row
	s.audits = append(s.audits, *audit)
	return nil
}
func settingsTestBase() config.Config {
	return config.Config{MonitorInterval: 5, SSHTimeout: 10, SSHMaxIdle: 300, ExecTimeout: 30, ExecTimeoutMax: 300, ExecOutputLimit: 8 << 20, ExecOutputStreamLimit: 4 << 20, LogLevel: "info", LogFormat: "json", LoginRateLimit: 10, DBMaxOpenConnections: 32, UsageWriteConcurrency: 4, UsageActiveLimit: 4096, UsageRetentionDays: 30, UsageSensitiveRetentionDays: 90, MetricsRetentionDryRun: true, Environment: map[string]bool{"AGENT_HUB_URL": true, "DB_MAX_OPEN_CONNECTIONS": true}}
}
func newMemorySettings(t *testing.T, base config.Config) (*SystemSettingsService, *memorySettingsStore) {
	t.Helper()
	store := &memorySettingsStore{row: model.SystemSettings{ID: 1, Revision: 1, Overrides: []byte(`{}`)}}
	s, err := NewSystemSettingsService(context.Background(), store, base)
	if err != nil {
		t.Fatal(err)
	}
	return s, store
}
func settingRequest(t *testing.T, s *SystemSettingsService, values string, confirm bool) (SettingsView, error) {
	t.Helper()
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(values), &patch); err != nil {
		t.Fatal(err)
	}
	return s.Update(context.Background(), SettingsUpdate{Revision: s.row.Revision, Values: patch, ConfirmRetentionChange: confirm}, model.AuditEvent{UserID: 1, Username: "admin"})
}
func TestSystemSettingsPrecedenceResetAndRestart(t *testing.T) {
	base := settingsTestBase()
	base.AgentHubURL = "https://deployment.example"
	base.AgentAllowHTTP = true
	s, store := newMemorySettings(t, base)
	view, err := settingRequest(t, s, `{"AGENT_HUB_URL":"", "AGENT_ALLOW_HTTP":false, "MONITOR_INTERVAL":10, "DB_MAX_OPEN_CONNECTIONS":48}`, false)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Runtime().Current()
	if c.AgentHubURL != "" || c.AgentAllowHTTP || c.MonitorInterval != 10 || c.DBMaxOpenConnections != 32 {
		t.Fatalf("incorrect runtime: %+v", c)
	}
	for _, v := range view.Settings {
		if v.Key == "DB_MAX_OPEN_CONNECTIONS" && (!v.RestartRequired || v.Value != 48 || v.EffectiveValue != 32 || v.Source != "database") {
			t.Fatalf("incorrect capacity state: %+v", v)
		}
	}
	if len(store.audits) != 1 || store.audits[0].OperationID == nil {
		t.Fatal("missing atomic audit")
	}
	restarted, err := NewSystemSettingsService(context.Background(), store, base)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Runtime().Current().DBMaxOpenConnections != 48 || restarted.Runtime().Current().AgentHubURL != "" {
		t.Fatal("restart lost overrides")
	}
	if _, err = settingRequest(t, restarted, `{"AGENT_HUB_URL":null,"AGENT_ALLOW_HTTP":null,"DB_MAX_OPEN_CONNECTIONS":null}`, false); err != nil {
		t.Fatal(err)
	}
	c = restarted.Runtime().Current()
	if c.AgentHubURL != base.AgentHubURL || !c.AgentAllowHTTP || c.DBMaxOpenConnections != 48 {
		t.Fatal("reset did not inherit deployment or changed live capacity")
	}
	third, err := NewSystemSettingsService(context.Background(), store, base)
	if err != nil {
		t.Fatal(err)
	}
	if third.Runtime().Current().DBMaxOpenConnections != 32 {
		t.Fatal("reset did not survive restart")
	}
}
func TestSystemSettingsValidationAndFailedSaveAreAtomic(t *testing.T) {
	s, store := newMemorySettings(t, settingsTestBase())
	for _, patch := range []string{`{"JWT_SECRET":"changed"}`, `{"LOGIN_RATE_LIMIT":0}`, `{"AGENT_HUB_URL":"http://example.com"}`, `{"AGENT_HUB_URL":"https://user:password@example.com"}`, `{"EXEC_TIMEOUT":40,"EXEC_TIMEOUT_MAX":20}`, `{"EXEC_OUTPUT_LIMIT":100,"EXEC_OUTPUT_STREAM_LIMIT":101}`, `{"MONITOR_INTERVAL":61}`, `{"MONITOR_INTERVAL":1.5}`, `{"AGENT_ALLOW_HTTP":"false"}`, `{"DB_MAX_OPEN_CONNECTIONS":11}`, `{"USAGE_LOG_RETENTION_DAYS":0}`} {
		if _, err := settingRequest(t, s, patch, false); !errors.Is(err, ErrSettingsInvalid) {
			t.Errorf("accepted %s: %v", patch, err)
		}
	}
	if s.row.Revision != 1 || len(store.audits) != 0 {
		t.Fatal("invalid requests changed persisted settings")
	}
	store.fail = true
	if _, err := settingRequest(t, s, `{"MONITOR_INTERVAL":10}`, false); err == nil {
		t.Fatal("accepted failed save")
	}
	if s.Runtime().Current().MonitorInterval != 5 || s.row.Revision != 1 {
		t.Fatal("failed save changed runtime")
	}
}
func TestSystemSettingsRetentionConfirmationAndConcurrentRevision(t *testing.T) {
	s, store := newMemorySettings(t, settingsTestBase())
	if _, err := settingRequest(t, s, `{"USAGE_LOG_RETENTION_DAYS":10}`, false); !errors.Is(err, ErrSettingsRetentionConfirmation) {
		t.Fatalf("missing retention guard: %v", err)
	}
	if _, err := settingRequest(t, s, `{"METRICS_RETENTION_ENABLED":true,"METRICS_RETENTION_DRY_RUN":false}`, false); !errors.Is(err, ErrSettingsRetentionConfirmation) {
		t.Fatalf("missing deletion guard: %v", err)
	}
	other, err := NewSystemSettingsService(context.Background(), store, settingsTestBase())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settingRequest(t, s, `{"USAGE_LOG_RETENTION_DAYS":10}`, true); err != nil {
		t.Fatal(err)
	}
	if _, err := settingRequest(t, other, `{"LOG_LEVEL":"debug"}`, false); !errors.Is(err, repository.ErrSettingsConflict) {
		t.Fatalf("lost update accepted: %v", err)
	}
	if err := other.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if other.Runtime().Current().UsageRetentionDays != 10 {
		t.Fatal("other instance did not refresh")
	}
}

func TestRuntimeSettingsKeepSSHLeaseSnapshotAndMonitorDesiredState(t *testing.T) {
	base := settingsTestBase()
	base.AgentHubURL = "https://old.example"
	r := config.NewRuntime(base)
	ssh := NewSSHService(nil, nil, nil, 0, 0)
	ssh.SetRuntimeConfig(r)
	oldSSH := ssh.settingsSnapshot()
	m := NewResidentMonitor(nil, nil, nil, "", false, 5)
	m.SetRuntimeConfig(r)
	old := m.settings()
	job := context.WithValue(context.Background(), residentSettingsKey{}, old)
	n := model.AgentNode{ServerID: 1, Deployment: "ready", TokenHash: "token", ConfigHash: old.hash("token")}
	m.nodes[1] = &residentRuntime{node: n}
	if m.Statuses(context.Background(), []uint{1})[1].Deployment != "ready" {
		t.Fatal("matching config not ready")
	}
	base.AgentHubURL = "https://new.example"
	base.MonitorInterval = 15
	base.ExecOutputLimit = 1024
	r.Store(base)
	if m.installSettings(job) != old || m.settings().URL != "https://new.example" {
		t.Fatal("in-flight installation mixed configurations")
	}
	if m.Statuses(context.Background(), []uint{1})[1].Deployment != "pending" {
		t.Fatal("old agent falsely reported updated")
	}
	if oldSSH.outputLimit != 8<<20 || ssh.settingsSnapshot().outputLimit != 1024 {
		t.Fatal("active SSH policy changed")
	}
	base.AgentHubURL = ""
	r.Store(base)
	if m.Statuses(context.Background(), []uint{1})[1].Deployment != "setup_required" {
		t.Fatal("cleared URL not shown")
	}
}
