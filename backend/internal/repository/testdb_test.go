package repository

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// testDatabaseURL returns TEST_DATABASE_URL, failing on CI when it is missing so
// the integration tests cannot silently skip there.
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI (see .github/workflows/ci.yml)")
		}
		t.Skip("set TEST_DATABASE_URL to run this integration test")
	}
	return dsn
}

// newTestDB opens a GORM handle scoped to a fresh schema and drops that schema
// on cleanup. The public schema is never touched, so it is safe to point
// TEST_DATABASE_URL at any Postgres, including a development one.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := testDatabaseURL(t)
	schema := fmt.Sprintf("talus_test_%d", time.Now().UnixNano())

	adminCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	admin := openGorm(t, adminCfg)
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		closeGorm(admin)
	})

	// search_path is set as a pgx runtime parameter, not appended to the DSN.
	// Appending (dsn + " search_path=" + schema) only works for the key=value
	// DSN form; with a postgres:// URL it lands inside the last query parameter
	// (e.g. application_name) and search_path is never applied.
	scopedCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	if scopedCfg.RuntimeParams == nil {
		scopedCfg.RuntimeParams = map[string]string{}
	}
	scopedCfg.RuntimeParams["search_path"] = schema
	db := openGorm(t, scopedCfg)
	t.Cleanup(func() { closeGorm(db) })
	return db
}

// openGorm opens GORM over a pgx connection config so per-connection runtime
// parameters (search_path) are honoured for every pooled connection.
func openGorm(t *testing.T, cfg *pgx.ConnConfig) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: stdlib.OpenDB(*cfg)}), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	return db
}

func closeGorm(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}
