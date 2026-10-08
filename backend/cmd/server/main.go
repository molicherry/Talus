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
	"github.com/vpsmanager/backend/internal/usage"
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
	sqlDB, err := db.DB()
	if err != nil {
		slog.Error("database pool unavailable")
		os.Exit(1)
	}
	sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConnections)
	sqlDB.SetMaxIdleConns(cfg.DBMaxOpenConnections / 2)

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

	// Fix the legacy key set in an atomic schema migration before AutoMigrate.
	// Optional owner assignment runs in the background after schema preparation.
	ownerSchemaCtx, cancelOwnerSchema := context.WithTimeout(context.Background(), 30*time.Second)
	ownerSchemaErr := repository.NewAPIKeyRepo(db).PrepareLegacyOwnerBinding(ownerSchemaCtx)
	cancelOwnerSchema()
	if ownerSchemaErr != nil {
		slog.Error("failed to prepare legacy API key owner migration", "error", ownerSchemaErr)
		os.Exit(1)
	}

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
			&model.AuditEvent{},
			&model.UsageLogInstance{},
			&model.UsageLog{},
			&model.UsageLogBackfillState{},
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

	// 4.5 One-time backfill: API keys created before services:read existed (the
	// API used to be fail-open) get the scope once. Recorded in schema_migrations
	// so it is not re-applied on every start to keys that deliberately omit it.
	const servicesReadBackfill = `UPDATE api_keys SET scopes = scopes || '["services:read"]'::jsonb WHERE scopes IS NOT NULL AND NOT scopes @> '["services:read"]'::jsonb`
	if applied, err := repository.ApplyOnce(db, "2026-09-24-services-read-scope", servicesReadBackfill); err != nil {
		// Bookkeeping for a scope default must not take the whole service down.
		// ApplyOnce returns before applying when it cannot check the marker.
		slog.Error("services:read scope backfill skipped", "error", err)
	} else if applied {
		slog.Info("services:read scope backfill applied")
	}

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

	// Dependency chain — SSH pool. Created before the server/credential services
	// so they can invalidate cached connections when a server or credential
	// changes. The SSH service itself is wired after the credential service.
	sshPool := sshpool.NewPool(
		time.Duration(cfg.SSHMaxIdle)*time.Second,
		3,
		time.Duration(cfg.SSHTimeout)*time.Second,
	)
	defer sshPool.Close()

	// Dependency chain — Servers (needed before API Keys for server ID validation)
	serverRepo := repository.NewServerRepo(db)
	metricRepo := repository.NewMetricRepo(db)
	serverSvc := service.NewServerService(serverRepo, metricRepo, sshPool)

	// Dependency chain — API Keys
	apiKeyRepo := repository.NewAPIKeyRepo(db)
	ownerBackfill := service.NewLegacyAPIKeyOwnerBackfill(apiKeyRepo)
	ownerBackfillCtx, cancelOwnerBackfill := context.WithCancel(context.Background())
	defer cancelOwnerBackfill()
	authSvc.SetOwnerBackfillTrigger(ownerBackfill.Trigger)
	go ownerBackfill.Run(ownerBackfillCtx)
	apiKeySvc := service.NewAPIKeyService(apiKeyRepo, serverRepo, masterKey)
	auditRepo := repository.NewAuditEventRepo(db)
	usagePolicy := (model.UsagePolicy{
		OrdinaryRetention:  time.Duration(cfg.UsageRetentionDays) * 24 * time.Hour,
		SensitiveRetention: time.Duration(cfg.UsageSensitiveRetentionDays) * 24 * time.Hour,
	}).WithDefaults()
	usageRepo := repository.NewUsageLogRepo(db, repository.UsageLogRepoConfig{LeaseDuration: usagePolicy.LeaseDuration, OrdinaryRetention: usagePolicy.OrdinaryRetention, SensitiveRetention: usagePolicy.SensitiveRetention})
	usageRecorder := usage.NewRecorder(usageRepo, auditRepo, usage.Options{
		DBMaxOpenConnections: cfg.DBMaxOpenConnections,
		WriteConcurrency:     cfg.UsageWriteConcurrency,
		ActiveLimit:          cfg.UsageActiveLimit,
		LeaseDuration:        usagePolicy.LeaseDuration,
		OrdinaryRetention:    usagePolicy.OrdinaryRetention,
		SensitiveRetention:   usagePolicy.SensitiveRetention,
	})
	usageCtx, stopUsage := context.WithCancel(context.Background())
	defer stopUsage()
	usageRecorder.Start(usageCtx)
	defer usageRecorder.Close()
	go func() {
		// The initial read-only report may need longer than a 200ms write
		// batch. Keep it in the same bounded maintenance connection budget.
		retry := time.NewTicker(time.Minute)
		defer retry.Stop()
		for {
			if err := usageRecorder.PrepareCleanupReport(usageCtx); err == nil {
				return
			}
			if usageCtx.Err() != nil {
				return
			}
			slog.Warn("usage retention cleanup report deferred", "reason", "cleanup_report_unavailable")
			select {
			case <-usageCtx.Done():
				return
			case <-retry.C:
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-usageCtx.Done():
				return
			case <-ticker.C:
				s := usageRecorder.Stats()
				slog.Info("usage capture statistics",
					"usage_capture_skipped_total", s.CaptureSkipped,
					"usage_write_failed_total", s.WriteFailed,
					"usage_finalization_retry_total", s.FinalizationRetry,
					"usage_reconciled_total", s.Reconciled,
					"audit_write_failed_total", s.AuditWriteFailed,
					"audit_lost_total", s.AuditLost,
					"usage_finalization_lost_total", s.FinalizationLost,
					"active", s.Active, "pending", s.Pending, "pending_bytes", s.PendingBytes,
					"audit_pending", s.AuditPending, "audit_pending_bytes", s.AuditPendingBytes,
					"oldest_pending_ms", s.OldestPendingMS)
			}
		}
	}()
	usageHandler := handler.NewUsageLogHandler(usageRepo, cfg.JWTSecret)
	usageFilterOptionsHandler := handler.NewUsageLogFilterOptionsHandler(usageRepo)
	if cfg.UsageLegacyWritersDrained {
		go func() {
			if err := usageRepo.Backfill(usageCtx); err != nil {
				slog.Warn("legacy usage backfill paused", "reason", "backfill_unavailable")
				return
			}
			statusCtx, cancelStatus := context.WithTimeout(usageCtx, 2*time.Second)
			defer cancelStatus()
			state, err := usageRepo.BackfillStatus(statusCtx)
			if err != nil {
				slog.Warn("legacy usage backfill status unavailable", "reason", "backfill_status_unavailable")
				return
			}
			if state.CompletedAt != nil {
				slog.Info("legacy usage backfill completed", "imported", state.Imported,
					"final_bound", state.FinalBound, "completed_at", state.CompletedAt)
			}
		}()
	} else {
		slog.Info("legacy usage backfill awaiting coordinated writer drain")
	}
	apiKeyHandler := handler.NewAPIKeyHandler(apiKeySvc, auditRepo)
	serverHandler := handler.NewServerHandler(serverSvc)

	// Dependency chain — Credentials
	credRepo := repository.NewCredentialRepo(db)
	credSvc := service.NewCredentialService(credRepo, serverRepo, masterKey, sshPool)
	credHandler := handler.NewCredentialHandler(credSvc, auditRepo)

	// Dependency chain — Services
	serviceRepo := repository.NewServiceRepo(db)
	serviceSvc := service.NewServiceRelayService(serviceRepo, masterKey)
	serviceHandler := handler.NewServiceHandler(serviceSvc, auditRepo)

	// Dependency chain — SSH

	sshDialTimeout := time.Duration(cfg.SSHTimeout) * time.Second
	execDefaultTimeout := time.Duration(cfg.ExecTimeout) * time.Second
	sshSvc := service.NewSSHService(sshPool, serverRepo, credSvc, sshDialTimeout, execDefaultTimeout)
	terminalSvc := service.NewTerminalService(sshSvc)

	execH := handler.NewExecHandler(sshSvc)
	terminalH := handler.NewTerminalHandler(terminalSvc, jwtSvc)

	// Dependency chain — Metrics
	monitorSvc := service.NewMonitorService(sshSvc, metricRepo, serverRepo, time.Duration(cfg.MonitorInterval)*time.Second)
	metricsH := handler.NewMetricsHandler(metricRepo)

	go monitorSvc.Start(context.Background())

	apiKeyAuth := mw.APIKeyIdentityValidatorFunc(func(ctx context.Context, rawKey string) (usage.Principal, error) {
		k, err := apiKeySvc.Validate(ctx, rawKey)
		if err != nil {
			return usage.Principal{}, err
		}
		p := usage.Principal{AuthType: "api_key", APIKeyID: &k.ID, APIKeyName: k.Name, APIKeyPrefix: k.KeyPrefix, Role: "admin", Scopes: k.Scopes, ServerIDs: k.ServerIDs}
		if k.UserID != 0 {
			p.UserID = &k.UserID
		}
		return p, nil
	})

	router := server.NewRouter(server.RouteConfig{
		JWTService:                   jwtSvc,
		APIKeyAuth:                   apiKeyAuth,
		UsageRecorder:                usageRecorder,
		ListUsageLogsHandler:         usageHandler.List,
		GetUsageLogHandler:           usageHandler.Get,
		FilterUsageLogOptionsHandler: usageFilterOptionsHandler.List,
		RevealLimiter:                mw.NewRateLimiter(1*time.Minute, 5),
		LoginLimiter:                 mw.NewIPRateLimiter(1*time.Minute, cfg.LoginRateLimit, cfg.TrustProxy),
		// Auth
		LoginHandler:          authHandler.Login,
		SetupHandler:          authHandler.Setup,
		ProfileHandler:        authHandler.Profile,
		ChangePasswordHandler: authHandler.ChangePassword,
		// Servers
		ListServersHandler:         serverHandler.List,
		ListServerSummariesHandler: serverHandler.ListSummaries,
		CreateServerHandler:        serverHandler.Create,
		GetServerHandler:           serverHandler.Get,
		UpdateServerHandler:        serverHandler.Update,
		DeleteServerHandler:        serverHandler.Delete,
		TrustServerHostKeyHandler:  serverHandler.TrustHostKey,
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
		CreateServiceHandler:         serviceHandler.Create,
		ListServicesHandler:          serviceHandler.List,
		GetServiceHandler:            serviceHandler.Get,
		UpdateServiceHandler:         serviceHandler.Update,
		DeleteServiceHandler:         serviceHandler.Delete,
		RelayServiceHandler:          serviceHandler.Relay,
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
