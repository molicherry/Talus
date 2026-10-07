package service

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func authOwnerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI")
		}
		t.Skip("set TEST_DATABASE_URL to run this integration test")
	}
	open := func(schema string) *gorm.DB {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.RuntimeParams == nil {
			cfg.RuntimeParams = map[string]string{}
		}
		if schema != "" {
			cfg.RuntimeParams["search_path"] = schema
		}
		pool := stdlib.OpenDB(*cfg)
		t.Cleanup(func() { _ = pool.Close() })
		db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	admin := open("")
	schema := fmt.Sprintf("talus_auth_owner_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	db := open(schema)
	if err := db.AutoMigrate(&model.User{}, &model.APIKey{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestFirstAdminSetupBindsLegacyKeys(t *testing.T) {
	db := authOwnerTestDB(t)
	ctx := context.Background()
	legacy := model.APIKey{Name: "legacy", KeyHash: "legacy-hash", KeyPrefix: "legacy01", Scopes: []string{"servers:read"}}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	keyRepo := repository.NewAPIKeyRepo(db)
	if bound, err := keyRepo.BindUnownedToDefaultAdmin(ctx); err != nil || bound != 0 {
		t.Fatalf("startup without administrator: bound=%d err=%v", bound, err)
	}
	if err := db.Exec("ALTER SEQUENCE users_id_seq RESTART WITH 41").Error; err != nil {
		t.Fatal(err)
	}
	jwtSvc := token.NewJWTService("test-secret", time.Hour)
	svc := NewAuthService(repository.NewUserRepo(db), jwtSvc, db)
	signed, err := svc.Login(ctx, "first-admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := jwtSvc.ValidateToken(signed)
	if err != nil || claims.UserID != 41 || claims.Role != "admin" {
		t.Fatalf("first administrator login: claims=%+v err=%v", claims, err)
	}
	key, err := keyRepo.FindByID(ctx, legacy.ID)
	if err != nil || key.UserID != claims.UserID {
		t.Fatalf("setup did not bind legacy key: key=%+v err=%v", key, err)
	}
	if _, err := svc.Login(ctx, "first-admin", "test-password"); err != nil {
		t.Fatalf("existing administrator login changed: %v", err)
	}
}

func TestFirstAdminSetupRollsBackFailedKeyBinding(t *testing.T) {
	db := authOwnerTestDB(t)
	ctx := context.Background()
	legacy := model.APIKey{Name: "legacy", KeyHash: "legacy-hash", KeyPrefix: "legacy01"}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE FUNCTION reject_owner_binding() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test binding failure'; END $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_owner_binding BEFORE UPDATE OF user_id ON api_keys FOR EACH ROW EXECUTE FUNCTION reject_owner_binding()`).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewAuthService(repository.NewUserRepo(db), token.NewJWTService("test-secret", time.Hour), db)
	if _, err := svc.Login(ctx, "first-admin", "test-password"); err == nil {
		t.Fatal("setup returned success despite failed legacy key binding")
	}
	count, err := repository.NewUserRepo(db).Count(ctx)
	if err != nil || count != 0 {
		t.Fatalf("failed setup committed administrator: count=%d err=%v", count, err)
	}
	key, err := repository.NewAPIKeyRepo(db).FindByID(ctx, legacy.ID)
	if err != nil || key.UserID != 0 {
		t.Fatalf("failed setup changed legacy key: key=%+v err=%v", key, err)
	}
	if err := db.Exec(`DROP TRIGGER reject_owner_binding ON api_keys`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, "first-admin", "test-password"); err != nil {
		t.Fatalf("setup could not retry after binding failure: %v", err)
	}
	key, err = repository.NewAPIKeyRepo(db).FindByID(ctx, legacy.ID)
	if err != nil || key.UserID == 0 {
		t.Fatalf("retried setup left legacy key unbound: key=%+v err=%v", key, err)
	}
}

func TestConcurrentFirstAdminSetupKeepsOneDefaultOwner(t *testing.T) {
	db := authOwnerTestDB(t)
	ctx := context.Background()
	legacy := model.APIKey{Name: "legacy", KeyHash: "legacy-hash", KeyPrefix: "legacy01"}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewAuthService(repository.NewUserRepo(db), token.NewJWTService("test-secret", time.Hour), db)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, username := range []string{"first-admin-a", "first-admin-b"} {
		go func() {
			<-start
			_, err := svc.createFirstUser(ctx, username, "test-password")
			results <- err
		}()
	}
	close(start)
	succeeded := 0
	for range 2 {
		if <-results == nil {
			succeeded++
		}
	}
	var admins []model.User
	if err := db.Find(&admins).Error; err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || len(admins) != 1 || admins[0].Role != "admin" {
		t.Fatalf("concurrent setup created multiple administrators: successes=%d users=%+v", succeeded, admins)
	}
	key, err := repository.NewAPIKeyRepo(db).FindByID(ctx, legacy.ID)
	if err != nil || key.UserID != admins[0].ID {
		t.Fatalf("concurrent setup picked a different owner: key=%+v err=%v", key, err)
	}
}
