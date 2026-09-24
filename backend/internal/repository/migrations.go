package repository

import (
	"fmt"

	"gorm.io/gorm"
)

// migrationsTable tracks one-time data migrations. AutoMigrate handles schema,
// but a data backfill must run exactly once: running it on every start would
// keep re-applying a default to rows created after the migration, silently
// undoing a deliberate choice (see the services:read scope backfill).
const (
	migrationsTable         = "schema_migrations"
	createMigrationsTableQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
		id text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`
	insertMigrationSQL = `INSERT INTO schema_migrations (id) VALUES (?)`
)

// ApplyOnce runs sql exactly once, keyed by id. It returns true when this call
// performed the migration and false when it had already been applied.
//
// The marker is written after sql; a crash between the two would re-run the
// migration on the next start, so sql must itself be idempotent.
func ApplyOnce(db *gorm.DB, id, sql string) (bool, error) {
	if err := db.Exec(createMigrationsTableQL).Error; err != nil {
		return false, fmt.Errorf("create %s: %w", migrationsTable, err)
	}

	var count int64
	if err := db.Table(migrationsTable).Where("id = ?", id).Count(&count).Error; err != nil {
		return false, fmt.Errorf("check migration %s: %w", id, err)
	}
	if count > 0 {
		return false, nil
	}

	if err := db.Exec(sql).Error; err != nil {
		return false, fmt.Errorf("apply migration %s: %w", id, err)
	}
	if err := db.Exec(insertMigrationSQL, id).Error; err != nil {
		return false, fmt.Errorf("record migration %s: %w", id, err)
	}
	return true, nil
}
