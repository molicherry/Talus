package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// Settings is the only allowlist exposed by the settings API. Deployment secrets
// and migration switches must never be added to this list.
type Setting struct {
	Key     string   `json:"key"`
	Field   string   `json:"-"`
	Group   string   `json:"group"`
	Kind    string   `json:"kind"`
	Apply   string   `json:"apply"`
	Min     int64    `json:"min,omitempty"`
	Max     int64    `json:"max,omitempty"`
	Choices []string `json:"choices,omitempty"`
}

// SettingValidationError identifies an invalid field without exposing its value
// in API responses or persisted-override diagnostics.
type SettingValidationError struct {
	Key string
	Err error
}

func (e *SettingValidationError) Error() string { return e.Key + ": " + e.Err.Error() }
func (e *SettingValidationError) Unwrap() error { return e.Err }

func invalidSetting(key, format string, args ...any) error {
	return &SettingValidationError{Key: key, Err: fmt.Errorf(format, args...)}
}

var Settings = []Setting{
	{Key: "AGENT_HUB_URL", Field: "AgentHubURL", Group: "monitoring", Kind: "string", Apply: "agent"},
	{Key: "MONITOR_INTERVAL", Field: "MonitorInterval", Group: "monitoring", Kind: "number", Apply: "agent", Min: 1, Max: 60},
	{Key: "AGENT_ALLOW_HTTP", Field: "AgentAllowHTTP", Group: "monitoring", Kind: "boolean", Apply: "agent"},
	{Key: "SSH_TIMEOUT", Field: "SSHTimeout", Group: "ssh", Kind: "number", Apply: "next_operation", Min: 1, Max: 300},
	{Key: "SSH_MAX_IDLE", Field: "SSHMaxIdle", Group: "ssh", Kind: "number", Apply: "next_operation", Min: 1, Max: 86400},
	{Key: "EXEC_TIMEOUT", Field: "ExecTimeout", Group: "execution", Kind: "number", Apply: "next_operation", Min: 1, Max: 300},
	{Key: "EXEC_TIMEOUT_MAX", Field: "ExecTimeoutMax", Group: "execution", Kind: "number", Apply: "next_operation", Min: 1, Max: 300},
	{Key: "EXEC_OUTPUT_LIMIT", Field: "ExecOutputLimit", Group: "execution", Kind: "number", Apply: "next_operation", Min: 1, Max: 8388608},
	{Key: "EXEC_OUTPUT_STREAM_LIMIT", Field: "ExecOutputStreamLimit", Group: "execution", Kind: "number", Apply: "next_operation", Min: 1, Max: 4194304},
	{Key: "USAGE_LOG_RETENTION_DAYS", Field: "UsageRetentionDays", Group: "retention", Kind: "number", Apply: "maintenance", Min: 1, Max: 36500},
	{Key: "USAGE_LOG_SENSITIVE_RETENTION_DAYS", Field: "UsageSensitiveRetentionDays", Group: "retention", Kind: "number", Apply: "maintenance", Min: 1, Max: 36500},
	{Key: "METRICS_RETENTION_ENABLED", Field: "MetricsRetentionEnabled", Group: "retention", Kind: "boolean", Apply: "maintenance"},
	{Key: "METRICS_RETENTION_DRY_RUN", Field: "MetricsRetentionDryRun", Group: "retention", Kind: "boolean", Apply: "maintenance"},
	{Key: "LOGIN_RATE_LIMIT", Field: "LoginRateLimit", Group: "security", Kind: "number", Apply: "live", Min: 1, Max: 10000},
	{Key: "LOG_LEVEL", Field: "LogLevel", Group: "logging", Kind: "string", Apply: "live", Choices: []string{"debug", "info", "warn", "error"}},
	{Key: "LOG_FORMAT", Field: "LogFormat", Group: "logging", Kind: "string", Apply: "live", Choices: []string{"json", "text"}},
	{Key: "DB_MAX_OPEN_CONNECTIONS", Field: "DBMaxOpenConnections", Group: "capacity", Kind: "number", Apply: "restart", Min: 12, Max: 10000},
	{Key: "USAGE_LOG_WRITE_CONCURRENCY", Field: "UsageWriteConcurrency", Group: "capacity", Kind: "number", Apply: "restart", Min: 1, Max: 10000},
	{Key: "USAGE_LOG_ACTIVE_LIMIT", Field: "UsageActiveLimit", Group: "capacity", Kind: "number", Apply: "restart", Min: 1, Max: 1000000},
}

func SettingValue(c Config, d Setting) any {
	return reflect.ValueOf(c).FieldByName(d.Field).Interface()
}

// MergeSettings applies a sparse override document. A missing key inherits the
// deployment value; false, zero and an empty string remain explicit values.
func MergeSettings(base Config, values map[string]json.RawMessage) (Config, error) {
	result := base
	// An explicit, validated database boolean supersedes only that variable's
	// parse error. Unoverridden malformed deletion switches still fail closed.
	if base.metricsRetentionParseErrors != nil {
		result.MetricsRetentionParseError = nil
		for key, err := range base.metricsRetentionParseErrors {
			if _, overridden := values[key]; !overridden {
				result.MetricsRetentionParseError = errors.Join(result.MetricsRetentionParseError, err)
			}
		}
	}
	known := make(map[string]Setting, len(Settings))
	for _, d := range Settings {
		known[d.Key] = d
	}
	for key, raw := range values {
		d, ok := known[key]
		if !ok {
			return base, invalidSetting(key, "unknown setting")
		}
		field := reflect.ValueOf(&result).Elem().FieldByName(d.Field)
		if strings.TrimSpace(string(raw)) == "null" {
			return base, invalidSetting(key, "null is not a stored override")
		}
		switch d.Kind {
		case "string":
			var v string
			if json.Unmarshal(raw, &v) != nil || len(v) > 2048 {
				return base, invalidSetting(key, "invalid string")
			}
			if key == "AGENT_HUB_URL" {
				v = strings.TrimRight(strings.TrimSpace(v), "/")
			}
			if len(d.Choices) > 0 {
				valid := false
				for _, choice := range d.Choices {
					valid = valid || choice == v
				}
				if !valid {
					return base, invalidSetting(key, "invalid choice")
				}
			}
			field.SetString(v)
		case "boolean":
			var v bool
			if json.Unmarshal(raw, &v) != nil {
				return base, invalidSetting(key, "expected boolean")
			}
			field.SetBool(v)
		case "number":
			var v int64
			if json.Unmarshal(raw, &v) != nil || v < d.Min || v > d.Max {
				return base, invalidSetting(key, "expected integer in [%d, %d]", d.Min, d.Max)
			}
			field.SetInt(v)
		}
	}
	if err := result.Validate(); err != nil {
		return base, err
	}
	return result, nil
}

// ApplyLive preserves the boot-time allocation sizes until the next restart.
func ApplyLive(desired, boot Config) Config {
	target, initial := reflect.ValueOf(&desired).Elem(), reflect.ValueOf(boot)
	for _, setting := range Settings {
		if setting.Apply == "restart" {
			target.FieldByName(setting.Field).Set(initial.FieldByName(setting.Field))
		}
	}
	return desired
}

func RetentionConfirmationRequired(before, after Config) bool {
	return after.UsageRetentionDays < before.UsageRetentionDays ||
		after.UsageSensitiveRetentionDays < before.UsageSensitiveRetentionDays ||
		(after.MetricsRetentionEnabled && !after.MetricsRetentionDryRun && (!before.MetricsRetentionEnabled || before.MetricsRetentionDryRun))
}
