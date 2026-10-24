package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vpsmanager/backend/internal/model"
)

func TestSystemSettingsPersistCASAndAuditRollback(t *testing.T) {
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.SystemSettings{}, &model.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	r := NewSystemSettingsRepo(db)
	ctx := context.Background()
	row, err := r.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if row.Revision != 1 {
		t.Fatal("missing initial revision")
	}
	row.Revision = 2
	row.Overrides = []byte(`{"MONITOR_INTERVAL":10}`)
	if err := r.Save(ctx, 1, row, &model.AuditEvent{Action: "settings.update"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, 1, row, &model.AuditEvent{Action: "settings.update"}); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("stale update accepted: %v", err)
	}
	var count int64
	db.Model(&model.AuditEvent{}).Count(&count)
	if count != 1 {
		t.Fatalf("unexpected audit count: %d", count)
	}
	if err := db.Migrator().DropTable(&model.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	row.Revision = 3
	row.Overrides = []byte(`{"MONITOR_INTERVAL":20}`)
	if err := r.Save(ctx, 2, row, &model.AuditEvent{Action: "settings.update"}); err == nil {
		t.Fatal("save succeeded without audit")
	}
	loaded, err := r.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]int
	if err := json.Unmarshal(loaded.Overrides, &values); err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 2 || values["MONITOR_INTERVAL"] != 10 {
		t.Fatalf("failed transaction changed settings: %+v", loaded)
	}
}
