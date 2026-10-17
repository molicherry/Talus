package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"gorm.io/gorm"
)

func secretTestDB(t *testing.T) (*gorm.DB, *crypto.MasterKey) {
	t.Helper()
	db := newTestDB(t)
	if err := db.AutoMigrate(&model.SSHCredential{}, &model.Service{}, &model.ServerConnectionConfig{}, &model.APIKey{}); err != nil {
		t.Fatal(err)
	}
	key, err := crypto.NewMasterKey(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return db, key
}

func oldTestSecret(t *testing.T, key *crypto.MasterKey, value string, fixed bool) ([]byte, string) {
	t.Helper()
	salt := bytes.Repeat([]byte{9}, crypto.SaltSize)
	var derived []byte
	if fixed {
		salt = nil
		derived = key.LegacyDeriveKey()
	} else {
		derived = key.DeriveKey(salt)
	}
	encoded, err := crypto.Encrypt([]byte(value), derived)
	if err != nil {
		t.Fatal(err)
	}
	return salt, encoded
}

func quietSecretLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSecretsUpgradeAllFormatsAndRestart(t *testing.T) {
	db, key := secretTestDB(t)
	salt, password := oldTestSecret(t, key, "ssh-password", false)
	ssh := model.SSHCredential{Name: "unchanged-label", AuthType: "password", Username: "operator", Salt: salt, EncryptedPassword: &password}
	if err := db.Create(&ssh).Error; err != nil {
		t.Fatal(err)
	}
	_, private := oldTestSecret(t, key, "historical-private-key", true)
	historical := model.SSHCredential{Name: "deleted-but-recoverable", AuthType: "private_key", Username: "root", EncryptedPrivateKey: &private}
	if err := db.Create(&historical).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&historical).Error; err != nil {
		t.Fatal(err)
	}
	salt, token := oldTestSecret(t, key, "service-token", false)
	_, second := oldTestSecret(t, key, "second-token", false)
	service := model.Service{Name: "service", BaseURL: "https://example.invalid", Salt: salt, EncryptedCredentials: map[string]string{"token": token, "unicode/密钥": second}}
	if err := db.Create(&service).Error; err != nil {
		t.Fatal(err)
	}
	salt, proxyPassword := oldTestSecret(t, key, "proxy-password", false)
	proxy := model.ServerConnectionConfig{ServerID: 42, Mode: "socks5", SocksHost: "proxy.invalid", SocksPort: 1080, SocksAuthType: "username_password", SocksUsername: "user", EncryptedPassword: &proxyPassword, Salt: salt}
	if err := db.Omit("Server", "JumpServer").Create(&proxy).Error; err != nil {
		t.Fatal(err)
	}
	salt, raw := oldTestSecret(t, key, "api-raw-secret", false)
	hash := sha256.Sum256([]byte("api-raw-secret"))
	api := model.APIKey{Name: "api", UserID: 123, KeyHash: hex.EncodeToString(hash[:]), Scopes: []string{"servers:read"}, EncryptedRawKey: raw, Salt: salt}
	if err := db.Create(&api).Error; err != nil {
		t.Fatal(err)
	}
	noRaw := model.APIKey{Name: "no-recoverable-raw", KeyHash: "historical-hash"}
	if err := db.Create(&noRaw).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&ssh, ssh.ID).Error; err != nil {
		t.Fatal(err)
	}
	// Model the real upgrade: these columns did not exist in the old database.
	for _, spec := range secretTables {
		if err := db.Exec("ALTER TABLE " + spec.table + " DROP COLUMN crypto_version").Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AutoMigrate(&model.SSHCredential{}, &model.Service{}, &model.ServerConnectionConfig{}, &model.APIKey{}); err != nil {
		t.Fatal(err)
	}
	report, err := MigrateSecrets(context.Background(), db, key, quietSecretLog())
	if err != nil {
		t.Fatal(err)
	}
	if report.Checked != 6 || report.Migrated != 5 {
		t.Fatalf("report: %+v", report)
	}
	var got model.SSHCredential
	if err := db.First(&got, ssh.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Name != ssh.Name || got.Username != ssh.Username || !got.UpdatedAt.Equal(ssh.UpdatedAt) {
		t.Fatal("migration changed SSH metadata")
	}
	assertSecret := func(version int, salt []byte, purpose, field, encoded, want string) {
		t.Helper()
		if version != crypto.VersionCurrent {
			t.Fatalf("version %d", version)
		}
		cipher, err := key.ForRecord(version, salt, purpose)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := cipher.Decrypt(field, encoded)
		if err != nil || string(plain) != want {
			t.Fatalf("%s/%s plaintext mismatch: %v", purpose, field, err)
		}
	}
	assertSecret(got.CryptoVersion, got.Salt, crypto.PurposeSSH, "password", *got.EncryptedPassword, "ssh-password")
	got = model.SSHCredential{}
	if err := db.Unscoped().First(&got, historical.ID).Error; err != nil {
		t.Fatal(err)
	}
	assertSecret(got.CryptoVersion, got.Salt, crypto.PurposeSSH, "private_key", *got.EncryptedPrivateKey, "historical-private-key")
	if !got.DeletedAt.Valid {
		t.Fatal("migration resurrected a deleted credential")
	}
	if err := db.First(&service, service.ID).Error; err != nil {
		t.Fatal(err)
	}
	assertSecret(service.CryptoVersion, service.Salt, crypto.PurposeService, "token", service.EncryptedCredentials["token"], "service-token")
	assertSecret(service.CryptoVersion, service.Salt, crypto.PurposeService, "unicode/密钥", service.EncryptedCredentials["unicode/密钥"], "second-token")
	if err := db.First(&proxy, "server_id = ?", 42).Error; err != nil {
		t.Fatal(err)
	}
	assertSecret(proxy.CryptoVersion, proxy.Salt, crypto.PurposeSOCKS5, "password", *proxy.EncryptedPassword, "proxy-password")
	if err := db.First(&api, api.ID).Error; err != nil {
		t.Fatal(err)
	}
	assertSecret(api.CryptoVersion, api.Salt, crypto.PurposeAPIKey, "raw_key", api.EncryptedRawKey, "api-raw-secret")
	if api.UserID != 123 || api.KeyHash != hex.EncodeToString(hash[:]) || len(api.Scopes) != 1 || api.Scopes[0] != "servers:read" {
		t.Fatal("API-key identity or permissions changed")
	}
	report, err = MigrateSecrets(context.Background(), db, key, quietSecretLog())
	if err != nil || report.Migrated != 0 || report.Checked != 6 {
		t.Fatalf("restart: %+v %v", report, err)
	}
	wrong, _ := crypto.NewMasterKey(strings.Repeat("cd", 32))
	if _, err := MigrateSecrets(context.Background(), db, wrong, quietSecretLog()); err == nil {
		t.Fatal("fully migrated database accepted a wrong master key")
	}
}

func TestSecretsMigrationFailureIsResumableAndPrivate(t *testing.T) {
	for _, badVersion := range []int{crypto.VersionLegacy, 99} {
		t.Run(fmt.Sprintf("version_%d", badVersion), func(t *testing.T) {
			db, key := secretTestDB(t)
			salt, encoded := oldTestSecret(t, key, "plain-must-not-leak", false)
			first := model.SSHCredential{AuthType: "password", Username: "one", Salt: salt, EncryptedPassword: &encoded}
			if err := db.Create(&first).Error; err != nil {
				t.Fatal(err)
			}
			bad := "invalid-secret-ciphertext-do-not-log"
			second := model.SSHCredential{CryptoVersion: badVersion, AuthType: "password", Username: "two", Salt: salt, EncryptedPassword: &bad}
			if err := db.Create(&second).Error; err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			report, err := MigrateSecrets(context.Background(), db, key, log)
			var migrationError *SecretMigrationError
			if !errors.As(err, &migrationError) || migrationError.ID != second.ID || report.Migrated != 1 {
				t.Fatalf("expected partial failure: %+v %v", report, err)
			}
			if strings.Contains(err.Error()+logs.String(), bad) || strings.Contains(err.Error()+logs.String(), encoded) || strings.Contains(err.Error()+logs.String(), "plain-must-not-leak") {
				t.Fatal("migration error exposed secrets")
			}
			var unchanged model.SSHCredential
			if err := db.First(&unchanged, second.ID).Error; err != nil {
				t.Fatal(err)
			}
			if unchanged.CryptoVersion != badVersion || *unchanged.EncryptedPassword != bad {
				t.Fatal("bad row was modified")
			}
			if err := db.Model(&model.SSHCredential{}).Where("id = ?", second.ID).Updates(map[string]any{"crypto_version": 1, "encrypted_password": encoded}).Error; err != nil {
				t.Fatal(err)
			}
			report, err = MigrateSecrets(context.Background(), db, key, log)
			if err != nil || report.Migrated != 1 {
				t.Fatalf("resume: %+v %v", report, err)
			}
		})
	}
}

func TestSecretsMigrationRejectsConcurrentEdit(t *testing.T) {
	db, key := secretTestDB(t)
	salt, old := oldTestSecret(t, key, "original", false)
	record := model.SSHCredential{AuthType: "password", Username: "user", Salt: salt, EncryptedPassword: &old}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]string{"password": old})
	snapshot := secretRow{ID: record.ID, CryptoVersion: 1, Salt: salt, Secrets: encoded}
	_, edited := oldTestSecret(t, key, "new-user-value", false)
	if err := db.Model(&record).Update("encrypted_password", edited).Error; err != nil {
		t.Fatal(err)
	}
	if changed, err := migrateSecretRow(db, key, secretTables[0], snapshot); err == nil || changed {
		t.Fatal("migration overwrote a concurrent edit")
	}
	if err := db.First(&record, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.CryptoVersion != 1 || *record.EncryptedPassword != edited {
		t.Fatal("edited record was not preserved")
	}
}

func TestSecretsMigrationLockCancellationAndRelease(t *testing.T) {
	db, key := secretTestDB(t)
	pool, _ := db.DB()
	conn, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "SELECT pg_advisory_lock($1)", secretMigrationLock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := MigrateSecrets(ctx, db, key, quietSecretLog()); err == nil {
		t.Fatal("lock wait ignored cancellation")
	}
	if _, err := conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", secretMigrationLock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if _, err := MigrateSecrets(ctx, db, key, quietSecretLog()); err != nil {
		t.Fatalf("cancelled startup leaked the migration lock: %v", err)
	}
}

func TestSecretsMigrationResumesAfterCancellation(t *testing.T) {
	db, key := secretTestDB(t)
	salt, encoded := oldTestSecret(t, key, "original", false)
	for i := 0; i < 2; i++ {
		record := model.SSHCredential{AuthType: "password", Username: "user", Salt: salt, EncryptedPassword: &encoded}
		if err := db.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register("test:cancel_secret_migration", func(tx *gorm.DB) {
		if tx.Statement.Table == "ssh_credentials" && tx.Error == nil {
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	report, err := MigrateSecrets(ctx, db, key, quietSecretLog())
	if !errors.Is(err, context.Canceled) || report.Migrated != 1 {
		t.Fatalf("cancelled upgrade: %+v %v", report, err)
	}
	if err := db.Callback().Update().Remove("test:cancel_secret_migration"); err != nil {
		t.Fatal(err)
	}
	report, err = MigrateSecrets(context.Background(), db, key, quietSecretLog())
	if err != nil || report.Migrated != 1 || report.Checked != 2 {
		t.Fatalf("restart after cancellation: %+v %v", report, err)
	}
}

func TestSecretsMigrationSerializesConcurrentStarts(t *testing.T) {
	db, key := secretTestDB(t)
	salt, encoded := oldTestSecret(t, key, "original", false)
	record := model.SSHCredential{AuthType: "password", Username: "user", Salt: salt, EncryptedPassword: &encoded}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	type result struct {
		report SecretMigrationReport
		err    error
	}
	finished := make(chan result, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		go func() { report, err := MigrateSecrets(ctx, db, key, quietSecretLog()); finished <- result{report, err} }()
	}
	total := 0
	for i := 0; i < 2; i++ {
		r := <-finished
		if r.err != nil || r.report.Checked != 1 {
			t.Fatalf("concurrent startup: %+v %v", r.report, r.err)
		}
		total += r.report.Migrated
	}
	if total != 1 {
		t.Fatalf("record was migrated %d times", total)
	}
}
