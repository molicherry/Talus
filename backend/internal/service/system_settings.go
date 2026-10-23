package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Revision uint64        `json:"revision"`
	Settings []SettingView `json:"settings"`
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
}

func NewSystemSettingsService(ctx context.Context, store settingsStore, base config.Config) (*SystemSettingsService, error) {
	row, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	values := map[string]json.RawMessage{}
	if err = json.Unmarshal(row.Overrides, &values); err != nil {
		return nil, err
	}
	boot, err := config.MergeSettings(base, values)
	if err != nil {
		return nil, err
	}
	return &SystemSettingsService{store: store, base: base, boot: boot, runtime: config.NewRuntime(boot), row: row, overrides: values}, nil
}
func (s *SystemSettingsService) Runtime() *config.Runtime { return s.runtime }

// SetOnChange is wired before serving requests or starting Refresh.
func (s *SystemSettingsService) SetOnChange(f func()) { s.onChange = f }

func (s *SystemSettingsService) viewLocked() SettingsView {
	desired, _ := config.MergeSettings(s.base, s.overrides)
	effective := s.runtime.Current()
	view := SettingsView{Revision: s.row.Revision, Settings: make([]SettingView, 0, len(config.Settings))}
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
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal(row.Overrides, &values); err != nil {
		return err
	}
	desired, err := config.MergeSettings(s.base, values)
	if err != nil {
		return err
	}
	s.row, s.overrides = row, values
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
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_ = s.Refresh(readCtx)
			cancel()
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
			return SettingsView{}, fmt.Errorf("%w: %s", ErrSettingsInvalid, k)
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
		return SettingsView{}, fmt.Errorf("%w: %v", ErrSettingsInvalid, err)
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
	details, _ := json.Marshal(map[string]any{"revision": next.Revision, "changes": changes})
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
	s.runtime.Store(config.ApplyLive(desired, s.boot))
	if s.onChange != nil {
		s.onChange()
	}
	return s.viewLocked(), nil
}
