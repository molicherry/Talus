package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vpsmanager/backend/internal/config"
	"github.com/vpsmanager/backend/internal/handler"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func main() {
	cfg := config.Load()

	setupLogger(cfg.LogFormat, cfg.LogLevel)

	// Initialize JWT service
	jwtSvc := token.NewJWTService(cfg.JWTSecret, 24*time.Hour)

	// Decrypt master key for credential encryption
	masterKey, err := crypto.NewMasterKey(cfg.MasterKey)
	if err != nil {
		slog.Error("failed to initialize master key", "error", err)
		os.Exit(1)
	}

	db, err := gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	})
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}

	// --- Database initialization (AutoMigrate replaces golang-migrate) ---

	// 1. Enable TimescaleDB extension
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS timescaledb").Error; err != nil {
		slog.Error("failed to create timescaledb extension", "error", err)
		os.Exit(1)
	}
	slog.Info("timescaledb extension ready")

	// 2. Clean up orphan columns from old migration
	if err := db.Exec("DO $$ BEGIN IF EXISTS (SELECT FROM pg_tables WHERE tablename = 'metrics') THEN ALTER TABLE metrics DROP COLUMN IF EXISTS net_rx, DROP COLUMN IF EXISTS net_tx; END IF; END $$").Error; err != nil {
		slog.Error("failed to drop orphan columns", "error", err)
		os.Exit(1)
	}
	slog.Info("orphan columns cleaned")

	// 3. Rename constraints to match GORM naming convention (uni_<table>_<column>)
	// The DB was initially created with PostgreSQL default constraint names (<table>_<column>_key),
	// but GORM AutoMigrate expects uni_<table>_<column>. Rename them before migration.
	slog.Info("renaming constraints to GORM convention")
	renameQueries := []string{
		`DO $$ BEGIN IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'users_username_key') THEN ALTER TABLE users RENAME CONSTRAINT users_username_key TO uni_users_username; END IF; END $$`,
		`DO $$ BEGIN IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'users_api_key_hash_key') THEN ALTER TABLE users RENAME CONSTRAINT users_api_key_hash_key TO uni_users_api_key_hash; END IF; END $$`,
		`DO $$ BEGIN IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'servers_name_key') THEN ALTER TABLE servers RENAME CONSTRAINT servers_name_key TO uni_servers_name; END IF; END $$`,
		`DO $$ BEGIN IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'api_keys_key_hash_key') THEN ALTER TABLE api_keys RENAME CONSTRAINT api_keys_key_hash_key TO uni_api_keys_key_hash; END IF; END $$`,
	}
	for _, q := range renameQueries {
		if err := db.Exec(q).Error; err != nil {
			slog.Error("failed to rename constraint", "error", err)
			os.Exit(1)
		}
	}
	slog.Info("constraints renamed")

	// 4. AutoMigrate all models in FK dependency order (User → Credential → Server → APIKey → Metric)
	var autoMigrateErr error
	for attempt := 0; attempt < 30; attempt++ {
		autoMigrateErr = db.AutoMigrate(
			&model.User{},
			&model.SSHCredential{},
			&model.Server{},
			&model.APIKey{},
			&model.Metric{},
			&model.Service{},
		)
		if autoMigrateErr == nil {
			break
		}
		slog.Warn("auto-migrate failed, retrying...", "attempt", attempt+1, "error", autoMigrateErr)
		time.Sleep(1 * time.Second)
	}
	if autoMigrateErr != nil {
		slog.Error("auto-migrate failed after retries", "error", autoMigrateErr)
		os.Exit(1)
	}
	slog.Info("auto-migrate complete")

	// 4. Create TimescaleDB hypertable for metrics
	if err := db.Exec("SELECT create_hypertable('metrics', 'time', chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE)").Error; err != nil {
		slog.Error("failed to create metrics hypertable", "error", err)
		os.Exit(1)
	}
	slog.Info("metrics hypertable ready")

	// Dependency chain — Auth
	userRepo := repository.NewUserRepo(db)
	authSvc := service.NewAuthService(userRepo, jwtSvc, db)
	authHandler := handler.NewAuthHandler(authSvc)

	// Dependency chain — Servers (needed before API Keys for server ID validation)
	serverRepo := repository.NewServerRepo(db)
	metricRepo := repository.NewMetricRepo(db)
	serverSvc := service.NewServerService(serverRepo, metricRepo)

	// Dependency chain — API Keys
	apiKeyRepo := repository.NewAPIKeyRepo(db)
	apiKeySvc := service.NewAPIKeyService(apiKeyRepo, serverRepo, masterKey)
	apiKeyHandler := handler.NewAPIKeyHandler(apiKeySvc)
	serverHandler := handler.NewServerHandler(serverSvc)

	// Dependency chain — Credentials
	credRepo := repository.NewCredentialRepo(db)
	credSvc := service.NewCredentialService(credRepo, masterKey)
	credHandler := handler.NewCredentialHandler(credSvc)

	// Dependency chain — Services
	serviceRepo := repository.NewServiceRepo(db)
	serviceSvc := service.NewServiceRelayService(serviceRepo, masterKey)
	serviceHandler := handler.NewServiceHandler(serviceSvc)

	// Dependency chain — SSH
	sshPool := sshpool.NewPool(
		time.Duration(cfg.SSHMaxIdle)*time.Second,
		3,
	)
	defer sshPool.Close()

	sshDialTimeout := time.Duration(cfg.SSHTimeout) * time.Second
	execDefaultTimeout := time.Duration(cfg.ExecTimeout) * time.Second
	sshSvc := service.NewSSHService(sshPool, serverRepo, credSvc, sshDialTimeout, execDefaultTimeout)
	terminalSvc := service.NewTerminalService(sshSvc)

	execH := handler.NewExecHandler(sshSvc)
	terminalH := handler.NewTerminalHandler(terminalSvc)

	// Dependency chain — Metrics
	monitorSvc := service.NewMonitorService(sshSvc, metricRepo, serverRepo, time.Duration(cfg.MonitorInterval)*time.Second)
	metricsH := handler.NewMetricsHandler(metricRepo)

	go monitorSvc.Start(context.Background())

	apiKeyAuth := mw.APIKeyValidatorFunc(func(ctx context.Context, rawKey string) (uint, string, string, []string, []uint, error) {
		k, err := apiKeySvc.Validate(ctx, rawKey)
		if err != nil {
			return 0, "", "", nil, nil, err
		}
		return k.ID, k.Name, "admin", k.Scopes, k.ServerIDs, nil
	})

	router := server.NewRouter(server.RouteConfig{
		JWTService:    jwtSvc,
		APIKeyAuth:    apiKeyAuth,
		RevealLimiter: mw.NewRateLimiter(1*time.Minute, 5),
		// Auth
		LoginHandler:           authHandler.Login,
		SetupHandler:           authHandler.Setup,
		ProfileHandler:         authHandler.Profile,
		ChangePasswordHandler:  authHandler.ChangePassword,
		// Servers
		ListServersHandler:   serverHandler.List,
		CreateServerHandler:  serverHandler.Create,
		GetServerHandler:     serverHandler.Get,
		UpdateServerHandler:  serverHandler.Update,
		DeleteServerHandler:  serverHandler.Delete,
		// Credentials
		ListCredentialsHandler:  credHandler.List,
		CreateCredentialHandler: credHandler.Create,
		UpdateCredentialHandler: credHandler.Update,
		DeleteCredentialHandler: credHandler.Delete,
		RevealCredentialHandler: credHandler.Reveal,
		// Exec
		ExecHandler: execH.Execute,
		// Terminal
		TerminalHandler: terminalH.Handle,
		// Metrics
		MetricsHandler: metricsH.Get,
		// API Keys
		CreateAPIKeyHandler: apiKeyHandler.Create,
		ListAPIKeysHandler:  apiKeyHandler.List,
		DeleteAPIKeyHandler: apiKeyHandler.Delete,
		RevealAPIKeyHandler: apiKeyHandler.Reveal,
		// Services
		CreateServiceHandler:        serviceHandler.Create,
		ListServicesHandler:         serviceHandler.List,
		GetServiceHandler:           serviceHandler.Get,
		UpdateServiceHandler:        serviceHandler.Update,
		DeleteServiceHandler:        serviceHandler.Delete,
		RelayServiceHandler:         serviceHandler.Relay,
		GetServiceCredentialsHandler: serviceHandler.GetCredentials,
		// Static files
		StaticDir: os.Getenv("STATIC_DIR"),
	})

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in a goroutine
	go func() {
		slog.Info("server starting", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("server shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}

	slog.Info("server stopped")
}

func setupLogger(format, level string) {
	var handler slog.Handler

	opts := &slog.HandlerOptions{
		Level: parseLevel(level),
	}

	switch format {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	slog.SetDefault(slog.New(handler))
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
