package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/vpsmanager/backend/internal/config"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/usage"
)

var ErrSettingsInvalid = errors.New("invalid settings")
var ErrSettingsRetentionConfirmation = errors.New("retention confirmation required")

type settingsStore interface {
	Load(context.Context) (model.SystemSettings, error)
	Save(context.Context, uint64, model.SystemSettings, *model.AuditEvent) error
}

type SettingView struct {
	config.Setting
	Value           any    `json:"value"`
	EffectiveValue  any    `json:"effective_value"`
	FallbackValue   any    `json:"fallback_value"`
	Source          string `json:"source"`
	FallbackSource  string `json:"fallback_source"`
	RestartRequired bool   `json:"restart_required"`
}
type SettingsView struct {
	Revision uint64            `json:"revision"`
	Settings []SettingView     `json:"settings"`
	Recovery *SettingsRecovery `json:"recovery,omitempty"`
}

type SettingsRecovery struct {
	Revision uint64 `json:"revision"`
	Key      string `json:"key"`
}

// SettingsLoadError deliberately omits values and parser error text from logs.
type SettingsLoadError struct {
	Revision uint64
	Key      string
	Err      error
}

func (e *SettingsLoadError) Error() string {
	return fmt.Sprintf("invalid system settings revision=%d key=%q", e.Revision, e.Key)
}
func (e *SettingsLoadError) Unwrap() error { return e.Err }

func mergeStoredSettings(base config.Config, row model.SystemSettings) (config.Config, map[string]json.RawMessage, error) {
	values := map[string]json.RawMessage{}
	err := json.Unmarshal(row.Overrides, &values)
	var desired config.Config
	if err == nil && values == nil {
		err = errors.New("overrides must be an object")
	}
	if err == nil {
		desired, err = config.MergeSettings(base, values)
	}
	if err != nil {
		key := "overrides"
		var field *config.SettingValidationError
		if errors.As(err, &field) {
			key = field.Key
		}
		return base, nil, &SettingsLoadError{Revision: row.Revision, Key: key, Err: err}
	}
	return desired, values, nil
}

type SettingsUpdate struct {
	Revision               uint64                     `json:"revision"`
	Values                 map[string]json.RawMessage `json:"values"`
	ConfirmRetentionChange bool                       `json:"confirm_retention_change"`
}

type SystemSettingsService struct {
	mu         sync.Mutex
	store      settingsStore
	base, boot config.Config
	runtime    *config.Runtime
	row        model.SystemSettings
	overrides  map[string]json.RawMessage
	onChange   func()
	recovery   *SettingsRecovery
}

func NewSystemSettingsService(ctx context.Context, store settingsStore, base config.Config) (*SystemSettingsService, error) {
	row, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	boot, values, err := mergeStoredSettings(base, row)
	var recovery *SettingsRecovery
	if err != nil {
		if !base.SettingsIgnoreInvalidOverrides {
			return nil, err
		}
		var invalid *SettingsLoadError
		if !errors.As(err, &invalid) {
			return nil, err
		}
		// Discard the invalid document only in memory. A later, audited settings
		// save replaces it using the same optimistic revision check as usual.
		boot, err = config.MergeSettings(base, nil)
		if err != nil {
			return nil, fmt.Errorf("settings recovery deployment configuration is invalid: %w", err)
		}
		values = map[string]json.RawMessage{}
		recovery = &SettingsRecovery{Revision: row.Revision, Key: invalid.Key}
		slog.Warn("ignoring invalid system settings overrides; using deployment configuration", "revision", row.Revision, "key", invalid.Key)
	}
	return &SystemSettingsService{store: store, base: base, boot: boot, runtime: config.NewRuntime(boot), row: row, overrides: values, recovery: recovery}, nil
}
func (s *SystemSettingsService) Runtime() *config.Runtime { return s.runtime }

// SetOnChange is wired before serving requests or starting Refresh.
func (s *SystemSettingsService) SetOnChange(f func()) { s.onChange = f }

func (s *SystemSettingsService) viewLocked() SettingsView {
	desired, _ := config.MergeSettings(s.base, s.overrides)
	effective := s.runtime.Current()
	view := SettingsView{Revision: s.row.Revision, Settings: make([]SettingView, 0, len(config.Settings)), Recovery: s.recovery}
	for _, d := range config.Settings {
		source := "default"
		if s.base.Environment[d.Key] {
			source = "environment"
		}
		v := SettingView{Setting: d, Value: config.SettingValue(desired, d), EffectiveValue: config.SettingValue(effective, d), FallbackValue: config.SettingValue(s.base, d), Source: source, FallbackSource: source}
		if _, ok := s.overrides[d.Key]; ok {
			v.Source = "database"
		}
		v.RestartRequired = d.Apply == "restart" && !reflect.DeepEqual(v.Value, v.EffectiveValue)
		view.Settings = append(view.Settings, v)
	}
	return view
}

func (s *SystemSettingsService) Get(ctx context.Context) (SettingsView, error) {
	if err := s.Refresh(ctx); err != nil {
		return SettingsView{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewLocked(), nil
}

func (s *SystemSettingsService) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.store.Load(ctx)
	if err != nil {
		return err
	}
	if row.Revision == s.row.Revision {
		return nil
	}
	desired, values, err := mergeStoredSettings(s.base, row)
	if err != nil {
		return err
	}
	s.row, s.overrides = row, values
	s.recovery = nil
	s.runtime.Store(config.ApplyLive(desired, s.boot))
	if s.onChange != nil {
		s.onChange()
	}
	return nil
}

// Poll also propagates changes made by another Hub without requiring a page load.
func (s *SystemSettingsService) Poll(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	s.poll(ctx, ticker.C)
}

func (s *SystemSettingsService) poll(ctx context.Context, ticks <-chan time.Time) {
	var lastFailure time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now, ok := <-ticks:
			if !ok {
				return
			}
			readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := s.Refresh(readCtx)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				if lastFailure.IsZero() || now.Sub(lastFailure) >= time.Minute {
					slog.Warn("system settings refresh failed; retaining last valid configuration", "error", err)
					lastFailure = now
				}
			} else if !lastFailure.IsZero() {
				slog.Info("system settings refresh recovered")
				lastFailure = time.Time{}
			}
		}
	}
}

func (s *SystemSettingsService) Update(ctx context.Context, req SettingsUpdate, audit model.AuditEvent) (SettingsView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Revision != s.row.Revision {
		return SettingsView{}, repository.ErrSettingsConflict
	}
	if len(req.Values) == 0 || len(req.Values) > len(config.Settings) {
		return SettingsView{}, ErrSettingsInvalid
	}
	known := make(map[string]bool, len(config.Settings))
	for _, d := range config.Settings {
		known[d.Key] = true
	}
	values := make(map[string]json.RawMessage, len(s.overrides))
	for k, v := range s.overrides {
		values[k] = v
	}
	for k, v := range req.Values {
		if !known[k] {
			return SettingsView{}, &config.SettingValidationError{Key: k, Err: ErrSettingsInvalid}
		}
		if strings.TrimSpace(string(v)) == "null" {
			delete(values, k)
		} else {
			values[k] = v
		}
	}
	before, _ := config.MergeSettings(s.base, s.overrides)
	desired, err := config.MergeSettings(s.base, values)
	if err != nil {
		return SettingsView{}, fmt.Errorf("%w: %w", ErrSettingsInvalid, err)
	}
	if config.RetentionConfirmationRequired(before, desired) && !req.ConfirmRetentionChange {
		return SettingsView{}, ErrSettingsRetentionConfirmation
	}
	// Store canonical values (notably whitespace/trailing slash normalization).
	changes := make(map[string]any, len(req.Values))
	for _, d := range config.Settings {
		if _, ok := values[d.Key]; ok {
			values[d.Key], _ = json.Marshal(config.SettingValue(desired, d))
		}
		if _, ok := req.Values[d.Key]; ok {
			_, wasOverride := s.overrides[d.Key]
			_, isOverride := values[d.Key]
			changes[d.Key] = map[string]any{"before": config.SettingValue(before, d), "after": config.SettingValue(desired, d), "was_override": wasOverride, "is_override": isOverride}
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return SettingsView{}, err
	}
	next := model.SystemSettings{ID: 1, Revision: s.row.Revision + 1, Overrides: encoded}
	detail := map[string]any{"revision": next.Revision, "changes": changes}
	if s.recovery != nil {
		detail["recovery"] = s.recovery
	}
	details, _ := json.Marshal(detail)
	audit.Action = "settings.update"
	audit.ResourceType = "settings"
	audit.ResourceID = 1
	audit.Details = string(details)
	if audit.OperationID == nil || *audit.OperationID == "" {
		id := usage.NewOperation("settings.update", "", "", "PUT", "").OperationID()
		audit.OperationID = &id
	}
	if err = s.store.Save(ctx, s.row.Revision, next, &audit); err != nil {
		return SettingsView{}, err
	}
	s.row, s.overrides = next, values
	s.recovery = nil
	s.runtime.Store(config.ApplyLive(desired, s.boot))
	if s.onChange != nil {
		s.onChange()
	}
	return s.viewLocked(), nil
}
