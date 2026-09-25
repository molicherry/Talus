package repository

import (
	"fmt"

	"gorm.io/gorm"
)

// Data-migration bookkeeping. AutoMigrate handles schema, but a data backfill
// must run exactly once: running it on every start would keep re-applying a
// default to rows created after the migration, silently undoing a deliberate
// choice (see the services:read scope backfill).
//
// `schema_migrations` is also golang-migrate's default table name (columns
// version/dirty). A deployment upgraded from that era still has it, and
// CREATE TABLE IF NOT EXISTS would silently leave it alone — then the `id`
// column this code needs does not exist. So the table is only reused when it is
// actually ours; otherwise a Talus-owned fallback table is used.
const (
	migrationsTable         = "schema_migrations"
	migrationsFallbackTable = "talus_data_migrations"

	createMigrationsTableSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
		id text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`
	createFallbackTableSQL = `CREATE TABLE IF NOT EXISTS talus_data_migrations (
		id text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`

	insertMigrationSQL         = `INSERT INTO schema_migrations (id) VALUES (?)`
	insertFallbackMigrationSQL = `INSERT INTO talus_data_migrations (id) VALUES (?)`
)

// migrationTable resolves which table to record migrations in for this database.
func migrationTable(db *gorm.DB) (string, error) {
	var tableExists, hasIDColumn bool
	if err := db.Raw(`SELECT EXISTS (
		SELECT 1 FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = 'schema_migrations'
	)`).Scan(&tableExists).Error; err != nil {
		return "", fmt.Errorf("inspect %s: %w", migrationsTable, err)
	}

	if !tableExists {
		if err := db.Exec(createMigrationsTableSQL).Error; err != nil {
			return "", fmt.Errorf("create %s: %w", migrationsTable, err)
		}
		return migrationsTable, nil
	}

	if err := db.Raw(`SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'schema_migrations' AND column_name = 'id'
	)`).Scan(&hasIDColumn).Error; err != nil {
		return "", fmt.Errorf("inspect %s columns: %w", migrationsTable, err)
	}
	if hasIDColumn {
		return migrationsTable, nil
	}

	// A foreign (e.g. golang-migrate) schema_migrations table is in the way.
	if err := db.Exec(createFallbackTableSQL).Error; err != nil {
		return "", fmt.Errorf("create %s: %w", migrationsFallbackTable, err)
	}
	return migrationsFallbackTable, nil
}

// ApplyOnce runs sql exactly once, keyed by id. It returns true when this call
// performed the migration and false when it had already been applied.
//
// The marker is written after sql; a crash between the two would re-run the
// migration on the next start, so sql must itself be idempotent.
func ApplyOnce(db *gorm.DB, id, sql string) (bool, error) {
	table, err := migrationTable(db)
	if err != nil {
		return false, err
	}

	var count int64
	if err := db.Table(table).Where("id = ?", id).Count(&count).Error; err != nil {
		return false, fmt.Errorf("check migration %s: %w", id, err)
	}
	if count > 0 {
		return false, nil
	}

	if err := db.Exec(sql).Error; err != nil {
		return false, fmt.Errorf("apply migration %s: %w", id, err)
	}

	insert := insertMigrationSQL
	if table == migrationsFallbackTable {
		insert = insertFallbackMigrationSQL
	}
	if err := db.Exec(insert, id).Error; err != nil {
		return false, fmt.Errorf("record migration %s: %w", id, err)
	}
	return true, nil
}
