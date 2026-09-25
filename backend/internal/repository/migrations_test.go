package repository

import "testing"

// TestApplyOnce guards the one-time data-migration semantics: the services:read
// scope backfill used to run on every startup, so a key created afterwards
// without that scope got it silently re-added on the next restart. A migration
// recorded in schema_migrations must run once and never touch later rows.
func TestApplyOnce(t *testing.T) {
	db := newTestDB(t)

	if err := db.Exec("CREATE TABLE widgets (id serial PRIMARY KEY, flag boolean NOT NULL DEFAULT false)").Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := db.Exec("INSERT INTO widgets (flag) VALUES (true)").Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}

	const id = "set-widget-flag"
	const sql = "UPDATE widgets SET flag = true WHERE flag = false"

	applied, err := ApplyOnce(db, id, sql)
	if err != nil {
		t.Fatalf("first ApplyOnce: %v", err)
	}
	if !applied {
		t.Fatal("first ApplyOnce reported already applied")
	}

	// A row created after the migration deliberately keeps flag = false.
	if err := db.Exec("INSERT INTO widgets (flag) VALUES (false)").Error; err != nil {
		t.Fatalf("insert post-migration row: %v", err)
	}

	applied, err = ApplyOnce(db, id, sql)
	if err != nil {
		t.Fatalf("second ApplyOnce: %v", err)
	}
	if applied {
		t.Fatal("migration was applied a second time")
	}

	var stillFalse int64
	if err := db.Raw("SELECT count(*) FROM widgets WHERE flag = false").Scan(&stillFalse).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if stillFalse != 1 {
		t.Fatalf("post-migration row was modified: %d rows still have flag = false, want 1", stillFalse)
	}

	var markers int64
	if err := db.Table("schema_migrations").Where("id = ?", id).Count(&markers).Error; err != nil {
		t.Fatalf("count markers: %v", err)
	}
	if markers != 1 {
		t.Fatalf("schema_migrations rows = %d, want 1", markers)
	}
}

// TestApplyOnceWithLegacySchemaMigrations guards the upgrade path from the
// golang-migrate era: that deployment already has a schema_migrations table
// with columns version/dirty, so CREATE TABLE IF NOT EXISTS leaves it alone and
// a query on `id` fails — which used to crash startup (os.Exit(1)).
func TestApplyOnceWithLegacySchemaMigrations(t *testing.T) {
	db := newTestDB(t)

	if err := db.Exec("CREATE TABLE schema_migrations (version bigint PRIMARY KEY, dirty boolean NOT NULL)").Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Exec("INSERT INTO schema_migrations (version, dirty) VALUES (1, false)").Error; err != nil {
		t.Fatalf("seed legacy table: %v", err)
	}
	if err := db.Exec("CREATE TABLE widgets (id serial PRIMARY KEY, flag boolean NOT NULL DEFAULT false)").Error; err != nil {
		t.Fatalf("create widgets: %v", err)
	}
	if err := db.Exec("INSERT INTO widgets (flag) VALUES (false)").Error; err != nil {
		t.Fatalf("seed widgets: %v", err)
	}

	const id = "legacy-migration"
	const sql = "UPDATE widgets SET flag = true WHERE flag = false"

	applied, err := ApplyOnce(db, id, sql)
	if err != nil {
		t.Fatalf("ApplyOnce with a legacy schema_migrations: %v", err)
	}
	if !applied {
		t.Fatal("expected the migration to apply")
	}

	applied, err = ApplyOnce(db, id, sql)
	if err != nil {
		t.Fatalf("second ApplyOnce: %v", err)
	}
	if applied {
		t.Fatal("migration applied twice")
	}

	var markers int64
	if err := db.Table("talus_data_migrations").Where("id = ?", id).Count(&markers).Error; err != nil {
		t.Fatalf("count markers: %v", err)
	}
	if markers != 1 {
		t.Fatalf("talus_data_migrations rows = %d, want 1", markers)
	}

	var legacy int64
	if err := db.Table("schema_migrations").Count(&legacy).Error; err != nil {
		t.Fatalf("count legacy: %v", err)
	}
	if legacy != 1 {
		t.Fatalf("legacy schema_migrations rows = %d, want 1 (untouched)", legacy)
	}
}
