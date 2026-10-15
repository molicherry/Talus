package repository

import "gorm.io/gorm"

const userTokenVersionMigrationID = "2026-10-10-users-token-version"

// PrepareUserTokenVersionSchema supports both an empty database and an upgrade.
// Bootstrap only the primary key before the explicit version migration; the
// ordinary model migration fills in the rest of the user schema afterwards.
// Creation, the version column and the migration marker commit atomically.
func PrepareUserTokenVersionSchema(db *gorm.DB) error {
	_, err := ApplyOnce(db, userTokenVersionMigrationID, `
		CREATE TABLE IF NOT EXISTS users (id bigserial PRIMARY KEY);
		ALTER TABLE users ADD COLUMN IF NOT EXISTS token_version bigint NOT NULL DEFAULT 0;
	`)
	return err
}
