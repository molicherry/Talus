package config

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestApplyLiveUsesSettingApplyMetadata(t *testing.T) {
	var desired, boot Config
	for _, d := range Settings {
		v := reflect.ValueOf(&desired).Elem().FieldByName(d.Field)
		switch v.Kind() {
		case reflect.Bool:
			v.SetBool(true)
		case reflect.String:
			v.SetString("changed")
		case reflect.Int, reflect.Int64:
			v.SetInt(42)
		}
	}
	got := ApplyLive(desired, boot)
	for _, d := range Settings {
		want := SettingValue(desired, d)
		if d.Apply == "restart" {
			want = SettingValue(boot, d)
		}
		if !reflect.DeepEqual(SettingValue(got, d), want) {
			t.Errorf("%s: apply=%s was not respected", d.Key, d.Apply)
		}
	}
}

func TestDeploymentValidationAllowsRepairableOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://unused/test")
	t.Setenv("VPSMANAGER_MASTER_KEY", "test")
	t.Setenv("JWT_SECRET", "test")
	t.Setenv("EXEC_TIMEOUT_MAX", "10")
	t.Setenv("METRICS_RETENTION_DRY_RUN", "malformed")
	c := Load()
	if err := c.ValidateDeployment(); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil {
		t.Fatal("invalid final configuration accepted")
	}
	if _, err := MergeSettings(*c, map[string]json.RawMessage{"EXEC_TIMEOUT_MAX": []byte(`300`), "METRICS_RETENTION_DRY_RUN": []byte(`true`)}); err != nil {
		t.Fatal(err)
	}
	c.MonitorAllowedLatency = -1
	if err := c.ValidateDeployment(); err == nil {
		t.Fatal("deployment-only error deferred")
	}
	t.Setenv("SYSTEM_SETTINGS_IGNORE_INVALID_OVERRIDES", "malformed")
	if err := Load().ValidateDeployment(); err == nil {
		t.Fatal("malformed recovery switch accepted")
	}
}

func TestSettingsValidationIdentifiesField(t *testing.T) {
	base := Config{MonitorInterval: 5, SSHTimeout: 10, ExecTimeout: 30, ExecTimeoutMax: 300, ExecOutputLimit: 8 << 20, ExecOutputStreamLimit: 4 << 20}
	for key, raw := range map[string]string{"MONITOR_INTERVAL": `1.5`, "EXEC_TIMEOUT_MAX": `10`, "AGENT_HUB_URL": `"http://untrusted.example"`, "METRICS_RETENTION_ENABLED": `"false"`} {
		_, err := MergeSettings(base, map[string]json.RawMessage{key: []byte(raw)})
		var field *SettingValidationError
		if !errors.As(err, &field) || field.Key != key {
			t.Errorf("%s: missing structured error: %v", key, err)
		}
	}
}
