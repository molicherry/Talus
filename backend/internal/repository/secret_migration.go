package repository

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const secretMigrationLock int64 = 0x54414c5553435259
const secretMigrationBatchSize = 100

type SecretMigrationReport struct{ Checked, Migrated int }

// SecretMigrationError deliberately omits driver errors and secret material
// from Error(). PostgreSQL constraint errors can otherwise contain row values.
type SecretMigrationError struct {
	Table string
	ID    uint
	Stage string
	Err   error
}

func (e *SecretMigrationError) Error() string {
	return fmt.Sprintf("credential migration: table=%s id=%d stage=%s; keep the original master key and inspect this record", e.Table, e.ID, e.Stage)
}
func (e *SecretMigrationError) Unwrap() error { return e.Err }

type secretTable struct{ table, primaryKey, purpose, expression string }

var secretTables = []secretTable{
	{"ssh_credentials", "id", crypto.PurposeSSH, "jsonb_strip_nulls(jsonb_build_object('password', encrypted_password, 'private_key', encrypted_private_key))"},
	{"services", "id", crypto.PurposeService, "COALESCE(encrypted_credentials, '{}'::jsonb)"},
	{"server_connection_configs", "server_id", crypto.PurposeSOCKS5, "jsonb_strip_nulls(jsonb_build_object('password', encrypted_password))"},
	{"api_keys", "id", crypto.PurposeAPIKey, "CASE WHEN COALESCE(encrypted_raw_key, '') = '' THEN '{}'::jsonb ELSE jsonb_build_object('raw_key', encrypted_raw_key) END"},
}

type secretRow struct {
	ID            uint
	CryptoVersion int
	Salt          []byte
	Secrets       []byte
}

// MigrateSecrets runs before HTTP, SSH pools and background jobs start. It
// includes soft-deleted rows and verifies v2 records on every startup too, so a
// wrong master key fails closed even after an upgrade was already completed.
// Each row commits independently: restarting resumes from its stored version.
func MigrateSecrets(ctx context.Context, db *gorm.DB, master *crypto.MasterKey, log *slog.Logger) (report SecretMigrationReport, err error) {
	if log == nil {
		log = slog.Default()
	}
	pool, err := db.DB()
	if err != nil {
		return report, errors.New("credential migration: database pool unavailable")
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		return report, errors.New("credential migration: database connection unavailable")
	}
	defer conn.Close()
	log.Info("credential migration waiting for lock")
	if _, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", secretMigrationLock); err != nil {
		// A cancelled query may have acquired the session lock just before its
		// response was lost. Discard this connection instead of pooling it.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return report, errors.New("credential migration: could not acquire lock")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		unlockErr := conn.QueryRowContext(cleanup, "SELECT pg_advisory_unlock($1)", secretMigrationLock).Scan(&unlocked)
		if unlockErr != nil || !unlocked {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, errors.New("credential migration: could not release lock"))
		}
	}()
	// Pin every migration query/transaction to the lock-owning connection.
	// Silence SQL logging: statements and driver details can contain ciphertext.
	pinned := db.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: db.Logger.LogMode(logger.Silent)})
	pinned.Statement.ConnPool = conn
	log.Info("credential migration started", "target_version", crypto.VersionCurrent)
	for _, spec := range secretTables {
		var after uint
		for {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			var rows []secretRow
			query := fmt.Sprintf("SELECT %s AS id, crypto_version, salt, %s AS secrets FROM %s WHERE %s > ? ORDER BY %s LIMIT ?", spec.primaryKey, spec.expression, spec.table, spec.primaryKey, spec.primaryKey)
			if e := pinned.Raw(query, after, secretMigrationBatchSize).Scan(&rows).Error; e != nil {
				return report, &SecretMigrationError{spec.table, after, "read", e}
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				if e := ctx.Err(); e != nil {
					return report, e
				}
				changed, e := migrateSecretRow(pinned, master, spec, row)
				if e != nil {
					return report, e
				}
				report.Checked++
				if changed {
					report.Migrated++
				}
				after = row.ID
			}
			log.Info("credential migration progress", "table", spec.table, "last_id", after, "checked", report.Checked, "migrated", report.Migrated)
		}
	}
	log.Info("credential migration complete", "checked", report.Checked, "migrated", report.Migrated)
	return report, nil
}

func migrateSecretRow(db *gorm.DB, master *crypto.MasterKey, spec secretTable, row secretRow) (bool, error) {
	fail := func(stage string, err error) (bool, error) {
		return false, &SecretMigrationError{spec.table, row.ID, stage, err}
	}
	if row.CryptoVersion != crypto.VersionLegacy && row.CryptoVersion != crypto.VersionCurrent {
		return fail("unsupported_version", errors.New("unsupported encryption version"))
	}
	var fields map[string]string
	if err := json.Unmarshal(row.Secrets, &fields); err != nil {
		return fail("decode", err)
	}
	if len(fields) == 0 {
		return false, nil
	} // Historical API keys without recoverable raw values remain usable.
	old, err := master.ForRecord(row.CryptoVersion, row.Salt, spec.purpose)
	if err != nil {
		return fail("derive", err)
	}
	plain := make(map[string][]byte, len(fields))
	defer func() {
		for _, value := range plain {
			clear(value)
		}
	}()
	for field, encoded := range fields {
		value, e := old.Decrypt(field, encoded)
		if e != nil {
			return fail("decrypt", e)
		}
		plain[field] = value
	}
	if row.CryptoVersion == crypto.VersionCurrent {
		return false, nil
	}
	salt, err := crypto.GenerateSalt()
	if err != nil {
		return fail("random", err)
	}
	current, err := master.ForRecord(crypto.VersionCurrent, salt, spec.purpose)
	if err != nil {
		return fail("derive", err)
	}
	for field, value := range plain {
		encoded, e := current.Encrypt(field, value)
		if e != nil {
			return fail("encrypt", e)
		}
		verified, e := current.Decrypt(field, encoded)
		matches := e == nil && bytes.Equal(value, verified)
		clear(verified)
		if !matches {
			return fail("verify", errors.New("new ciphertext verification failed"))
		}
		fields[field] = encoded
	}
	updates := map[string]any{"crypto_version": crypto.VersionCurrent, "salt": salt}
	switch spec.purpose {
	case crypto.PurposeSSH:
		// Omitted fields stay NULL; never change auth type, labels or timestamps.
		for _, pair := range [][2]string{{"password", "encrypted_password"}, {"private_key", "encrypted_private_key"}} {
			if encoded, ok := fields[pair[0]]; ok {
				updates[pair[1]] = encoded
			}
		}
	case crypto.PurposeService:
		encoded, e := json.Marshal(fields)
		if e != nil {
			return fail("encode", e)
		}
		updates["encrypted_credentials"] = string(encoded)
	case crypto.PurposeSOCKS5:
		updates["encrypted_password"] = fields["password"]
	case crypto.PurposeAPIKey:
		updates["encrypted_raw_key"] = fields["raw_key"]
	}
	// Compare the complete encrypted snapshot, not a timestamp. A concurrent
	// edit must never be overwritten. Updating only these columns also preserves
	// API-key ownership/scopes/hash and all unrelated business fields.
	result := db.Table(spec.table).
		Where(spec.primaryKey+" = ? AND crypto_version = ? AND salt IS NOT DISTINCT FROM ? AND ("+spec.expression+") = ?::jsonb", row.ID, row.CryptoVersion, row.Salt, string(row.Secrets)).
		Updates(updates)
	if result.Error != nil {
		return fail("write", result.Error)
	}
	if result.RowsAffected != 1 {
		return fail("concurrent_change", errors.New("encrypted record changed during migration"))
	}
	return true, nil
}
