package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/vpsmanager/backend/internal/agentproto"
	"github.com/vpsmanager/backend/internal/config"
	"github.com/vpsmanager/backend/internal/gate"
	"github.com/vpsmanager/backend/internal/handler"
	"github.com/vpsmanager/backend/internal/model"
	"github.com/vpsmanager/backend/internal/pkg/crypto"
	"github.com/vpsmanager/backend/internal/pkg/sshpool"
	"github.com/vpsmanager/backend/internal/pkg/token"
	"github.com/vpsmanager/backend/internal/repository"
	"github.com/vpsmanager/backend/internal/server"
	mw "github.com/vpsmanager/backend/internal/server/middleware"
	"github.com/vpsmanager/backend/internal/service"
	"github.com/vpsmanager/backend/internal/telemetry"
	"github.com/vpsmanager/backend/internal/update"
	"github.com/vpsmanager/backend/internal/usage"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func main() {
	cfg := config.Load()

	setupLogger(cfg.LogFormat, cfg.LogLevel)

	// Validate the effective configuration after loading persisted overrides.

	// Initialize JWT service
	jwtSvc := token.NewJWTService(cfg.JWTSecret, 24*time.Hour)

	// Agent artifacts: validate the bundled agent binaries and
	// their manifest before touching the database or binding a port. A missing,
	// corrupt, mis-architecture or mis-versioned artifact is fatal — the Hub never
	// falls back to a single architecture.
	if err := verifyAgentArtifacts(cfg.AgentArtifactsDir); err != nil {
		slog.Error("agent artifacts are invalid", "error", err)
		os.Exit(1)
	}

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

	// Recognize pre-scope schemas before AutoMigrate can fill in today's defaults.
	permissionSchemaCtx, cancelPermissionSchema := context.WithTimeout(context.Background(), 30*time.Second)
	permissionSchemaPrepared, permissionSchemaErr := repository.PrepareAPIKeyPermissionsSchema(permissionSchemaCtx, db)
	cancelPermissionSchema()
	if permissionSchemaErr != nil {
		slog.Error("failed to prepare API key permissions", "error", permissionSchemaErr)
		os.Exit(1)
	}

	// JWT session versioning (REQ-05). Explicit, idempotent schema migration
	// rather than relying on AutoMigrate for this security-relevant column.
	if err := repository.PrepareUserTokenVersionSchema(db); err != nil {
		slog.Error("failed to add users.token_version", "error", err)
		os.Exit(1)
	}

	// Metrics rollup storage: the 6h rollup table and its
	// materialization checkpoints. Idempotent and explicit.
	if err := repository.PrepareMetricsRollupSchema(db); err != nil {
		slog.Error("failed to prepare metrics rollup schema", "error", err)
		os.Exit(1)
	}

	// 4. AutoMigrate all models in FK dependency order (User → Credential → Server → APIKey → Metric)
	var autoMigrateErr error
	for attempt := 0; attempt < 30; attempt++ {
		autoMigrateErr = db.AutoMigrate(
			&model.User{},
			&model.SSHCredential{},
			&model.Server{},
			&model.TrustedAgentArtifact{},
			&model.AgentNode{},
			&model.AgentReceipt{},
			&model.ServerConnectionConfig{},
			&model.APIKey{},
			&model.Metric{},
			&model.Service{},
			&model.AuditEvent{},
			&model.UsageLogInstance{},
			&model.UsageLog{},
			&model.UsageLogBackfillState{},
			&model.SystemSettings{},
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
	settingsCtx, cancelSettings := context.WithTimeout(context.Background(), 15*time.Second)
	settingsSvc, settingsErr := service.NewSystemSettingsService(settingsCtx, repository.NewSystemSettingsRepo(db), *cfg)
	cancelSettings()
	if settingsErr != nil {
		slog.Error("cannot load system settings", "error", settingsErr)
		os.Exit(1)
	}
	runtimeConfig := settingsSvc.Runtime()
	effectiveConfig := runtimeConfig.Current()
	cfg = &effectiveConfig
	sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConnections)
	sqlDB.SetMaxIdleConns(cfg.DBMaxOpenConnections / 2)
	slog.SetDefault(slog.New(config.RuntimeLogHandler{Runtime: runtimeConfig,
		JSON: slog.NewJSONHandler(os.Stdout, nil), Text: slog.NewTextHandler(os.Stdout, nil)}))

	// Upgrade encrypted records before starting HTTP, connection pools or any
	// background writer. Keep the original master key and database volume when
	// replacing the Docker container. Per-record commits make retries resumable.
	migrationCtx, stopMigration := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	migrationReport, migrationErr := repository.MigrateSecrets(migrationCtx, db, masterKey, slog.Default())
	stopMigration()
	if migrationErr != nil {
		slog.Error("credential migration failed; startup stopped", "error", migrationErr)
		os.Exit(1)
	}

	if migrationReport.Migrated > 0 {
		// Return the old Argon2 workspace once, before serving traffic. Normal
		// v2 startups/requests do not force GC or allocate that workspace.
		debug.FreeOSMemory()
	}

	// A fresh installation had no api_keys table during preparation. Only that
	// path needs to settle the migration markers now; existing schemas are done.
	if !permissionSchemaPrepared {
		permissionCtx, cancelPermissionMigration := context.WithTimeout(context.Background(), 30*time.Second)
		_, permissionErr := repository.PrepareAPIKeyPermissionsSchema(permissionCtx, db)
		cancelPermissionMigration()
		if permissionErr != nil {
			slog.Error("API key permission migration failed; startup stopped", "error", permissionErr)
			os.Exit(1)
		}
	}

	// 4. Create TimescaleDB hypertable for metrics
	if err := db.Exec("SELECT create_hypertable('metrics', 'time', chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE)").Error; err != nil {
		slog.Error("failed to create metrics hypertable", "error", err)
		os.Exit(1)
	}
	slog.Info("metrics hypertable ready")

	// Dependency chain — Auth
	userRepo := repository.NewUserRepo(db)
	userGate := gate.New(userRepo.TokenVersion, userRepo.LockedTokenVersion)
	terminalRegistry := gate.NewRegistry()
	authSvc := service.NewAuthService(userRepo, jwtSvc, db)
	authSvc.SetUserGate(userGate)
	authSvc.SetTerminalRegistry(terminalRegistry)
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
	sshPool.SetRuntimeConfig(runtimeConfig)

	// Dependency chain — Servers (needed before API Keys for server ID validation)
	serverRepo := repository.NewServerRepo(db)
	metricRepo := repository.NewMetricRepo(db)
	metricRepo.SetRuntimeConfig(runtimeConfig)
	// One shared telemetry registry for the background paths: the frozen
	// vocabulary is validated at the call site, so an unknown label value fails
	// closed instead of reaching the metrics.
	telemetryRegistry := telemetry.NewRegistry()
	serverSvc := service.NewServerService(serverRepo, metricRepo, sshPool)
	serverSvc.SetMasterKey(masterKey)
	serverSvc.SetFreshnessThreshold(cfg.MonitorFreshnessThreshold())

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
	usageRepo.SetRuntimeConfig(runtimeConfig)
	usageRecorder := usage.NewRecorder(usageRepo, auditRepo, usage.Options{
		DBMaxOpenConnections: cfg.DBMaxOpenConnections,
		WriteConcurrency:     cfg.UsageWriteConcurrency,
		ActiveLimit:          cfg.UsageActiveLimit,
		LeaseDuration:        usagePolicy.LeaseDuration,
		OrdinaryRetention:    usagePolicy.OrdinaryRetention,
		SensitiveRetention:   usagePolicy.SensitiveRetention,
	})
	usageRecorder.SetRuntimeConfig(runtimeConfig)
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
	// The SSH layer owns the in-use gauge: one increment per acquired pool slot and
	// one decrement per real settlement, so the reading follows the resource and
	// not a caller's foreground return (N03).
	sshSvc.SetLeaseObserver(func(consumer service.LeaseConsumer, delta float64) {
		_ = telemetryRegistry.AddGauge("talus_ssh_leases_in_use",
			map[string]string{"consumer": string(consumer)}, delta)
	})
	sshSvc.SetOutputLimits(cfg.ExecOutputLimit, cfg.ExecOutputStreamLimit)
	sshSvc.SetRuntimeConfig(runtimeConfig)
	sftpH := handler.NewSFTPHandler(service.NewSFTPService(sshSvc, []byte(cfg.JWTSecret)))
	terminalSvc := service.NewTerminalService(sshSvc)
	terminalSvc.SetSessionRegistry(terminalRegistry)
	terminalSvc.SetAPIKeyPermissions(apiKeySvc.PermissionsGate(), apiKeySvc.TerminalRegistry())

	execH := handler.NewExecHandler(sshSvc, handler.ExecOptions{
		DefaultTimeoutSeconds: cfg.ExecTimeout,
		MaxTimeoutSeconds:     cfg.ExecTimeoutMax,
	})
	execH.SetRuntimeConfig(runtimeConfig)
	connectionH := handler.NewServerConnectionHandler(sshSvc)
	terminalH := handler.NewTerminalHandler(terminalSvc, jwtSvc)
	terminalH.SetUserGate(userGate)

	// Dependency chain — Metrics
	// The sampling-gap threshold follows the effective collection interval (R07).
	metricRepo.SetDeltaGapThreshold(time.Duration(cfg.MonitorInterval) * time.Second)
	agentDeployer := service.NewAgentDeploymentService(sshSvc, cfg.AgentArtifactsDir)
	agentArtifacts := repository.NewAgentArtifactRepo(db)
	agentManifest, err := agentDeployer.Manifest()
	if err != nil {
		slog.Error("cannot load verified agent manifest")
		os.Exit(1)
	}
	registerCtx, cancelRegister := context.WithTimeout(context.Background(), 15*time.Second)
	registerErr := agentArtifacts.Register(registerCtx, agentManifest)
	cancelRegister()
	if registerErr != nil {
		slog.Error("cannot register trusted agent artifacts")
		os.Exit(1)
	}
	agentDeployer.SetTrustedArtifacts(agentArtifacts)
	agentNodes := repository.NewAgentNodeRepo(db, masterKey)
	monitorSvc := service.NewResidentMonitor(agentNodes, serverRepo, agentDeployer, cfg.AgentHubURL, cfg.AgentAllowHTTP, cfg.MonitorInterval)
	monitorSvc.SetRuntimeConfig(runtimeConfig)
	settingsSvc.SetOnChange(monitorSvc.NotifyTopologyChange)
	monitorSvc.SetTelemetry(telemetryRegistry)
	serverSvc.SetMonitoringProvider(monitorSvc)
	metricsH := handler.NewMetricsHandler(metricRepo)

	// The monitor joins the application root context so shutdown can stop the
	// scheduler, cancel in-flight collections and wait for them before the
	// connection pool and storage are closed below.
	// A created or deleted node is picked up immediately instead of waiting for
	// the monitor's next interval.
	serverSvc.SetTopologyNotifier(monitorSvc)

	appCtx, stopApp := context.WithCancel(context.Background())
	defer stopApp()
	go settingsSvc.Poll(appCtx)
	updateChecker := update.NewChecker(server.Version)
	updateDone := make(chan struct{})
	go func() {
		defer close(updateDone)
		updateChecker.Run(appCtx)
	}()
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		monitorSvc.Start(appCtx)
	}()

	// Metrics storage maintenance: hourly rollup refresh and the
	// daily retention pass, joined on the same root context.
	maintenance := service.NewMetricsMaintenance(metricRepo)
	// Retention follows the documented rollout: disabled by default, a dry run
	// reports the plan, and only enabled && !dry-run deletes.
	maintenance.SetRetention(cfg.MetricsRetentionEnabled, cfg.MetricsRetentionDryRun)
	maintenance.SetRuntimeConfig(runtimeConfig)
	maintenance.SetTelemetry(telemetryRegistry, nil)
	maintenanceDone := make(chan struct{})
	go func() {
		defer close(maintenanceDone)
		maintenance.Run(appCtx)
	}()

	apiKeyAuth := mw.APIKeyIdentityValidatorFunc(func(ctx context.Context, rawKey string) (usage.Principal, error) {
		k, err := apiKeySvc.Validate(ctx, rawKey)
		if err != nil {
			return usage.Principal{}, err
		}
		p := usage.Principal{AuthType: "api_key", APIKeyID: &k.ID, APIKeyName: k.Name, APIKeyPrefix: k.KeyPrefix, Role: "admin", Scopes: k.Scopes, ServerIDs: k.ServerIDs, PermissionsRevision: k.PermissionsRevision}
		if k.UserID != 0 {
			p.UserID = &k.UserID
		}
		return p, nil
	})

	settingsHandler := handler.NewSystemSettingsHandler(settingsSvc, usageRepo)
	loginLimiter := mw.NewIPRateLimiter(time.Minute, cfg.LoginRateLimit, cfg.TrustProxy)
	loginLimiter.SetRuntimeConfig(runtimeConfig)
	router := server.NewRouter(server.RouteConfig{
		GetSettingsHandler:           settingsHandler.Get,
		UpdateSettingsHandler:        settingsHandler.Update,
		AgentHTTPHandler:             monitorSvc.AgentHTTP,
		AgentWebSocketHandler:        monitorSvc.AgentWebSocket,
		JWTService:                   jwtSvc,
		UserGate:                     userGate,
		APIKeyAuth:                   apiKeyAuth,
		UsageRecorder:                usageRecorder,
		ListUsageLogsHandler:         usageHandler.List,
		GetUsageLogHandler:           usageHandler.Get,
		FilterUsageLogOptionsHandler: usageFilterOptionsHandler.List,
		RevealLimiter:                mw.NewRateLimiter(1*time.Minute, 5),
		LoginLimiter:                 loginLimiter,
		CheckUpdatesHandler:          handler.NewUpdateHandler(updateChecker),
		// Auth
		LoginHandler:          authHandler.Login,
		SetupHandler:          authHandler.Setup,
		ProfileHandler:        authHandler.Profile,
		ChangePasswordHandler: authHandler.ChangePassword,
		// Servers
		ListServersHandler:          serverHandler.List,
		ListServerSummariesHandler:  serverHandler.ListSummaries,
		CreateServerHandler:         serverHandler.Create,
		GetServerHandler:            serverHandler.Get,
		UpdateServerHandler:         serverHandler.Update,
		DeleteServerHandler:         serverHandler.Delete,
		TrustServerHostKeyHandler:   serverHandler.TrustHostKey,
		TestServerConnectionHandler: connectionH.Test,
		// Credentials
		ListCredentialsHandler:  credHandler.List,
		CreateCredentialHandler: credHandler.Create,
		UpdateCredentialHandler: credHandler.Update,
		DeleteCredentialHandler: credHandler.Delete,
		RevealCredentialHandler: credHandler.Reveal,
		// Exec
		ExecHandler: execH.Execute,
		// Terminal
		TerminalHandler:           terminalH.Handle,
		SFTPListHandler:           sftpH.List,
		SFTPStatHandler:           sftpH.Stat,
		SFTPUploadHandler:         sftpH.Upload,
		SFTPDownloadHandler:       sftpH.Download,
		SFTPMkdirHandler:          sftpH.Mkdir,
		SFTPRenameHandler:         sftpH.Rename,
		SFTPRemoveHandler:         sftpH.Remove,
		SFTPReadTextHandler:       sftpH.ReadText,
		SFTPSaveTextHandler:       sftpH.SaveText,
		SFTPStartUploadHandler:    sftpH.StartUpload,
		SFTPUploadStatusHandler:   sftpH.UploadStatus,
		SFTPUploadChunkHandler:    sftpH.UploadChunk,
		SFTPCompleteUploadHandler: sftpH.CompleteUpload,
		SFTPCancelUploadHandler:   sftpH.CancelUpload,
		SFTPArchiveHandler:        sftpH.Archive,
		// Metrics
		MetricsHandler: metricsH.Get,
		// API Keys
		CreateAPIKeyHandler:            apiKeyHandler.Create,
		UpdateAPIKeyPermissionsHandler: apiKeyHandler.UpdatePermissions,
		RotateAPIKeyHandler:            apiKeyHandler.Rotate,
		ListAPIKeysHandler:             apiKeyHandler.List,
		DeleteAPIKeyHandler:            apiKeyHandler.Delete,
		RevealAPIKeyHandler:            apiKeyHandler.Reveal,
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
		Addr:        ":" + cfg.Port,
		Handler:     router,
		ReadTimeout: 15 * time.Second,
		// WriteTimeout is disabled globally: a single 15s deadline truncates
		// long Exec/Relay responses. Per-route budgets are applied by
		// middleware.WriteDeadline.
		WriteTimeout: 0,
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

	// Stop scheduling and drain in-flight collections before the pool and the
	// database are closed by the deferred cleanups.
	stopApp()
	<-monitorDone
	<-maintenanceDone
	<-updateDone

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

// verifyAgentArtifacts validates the bundled agent artifacts. AGENT_ARTIFACTS_DIR
// points at the directory holding the manifest and both arch-specific binaries
// (the Hub image ships them in /usr/local/bin).
func verifyAgentArtifacts(dir string) error {
	manifest, err := agentproto.LoadManifest(dir)
	if err != nil {
		return err
	}
	if err := manifest.Verify(dir); err != nil {
		return err
	}
	slog.Info("agent artifacts verified", "dir", dir, "build_version", manifest.BuildVersion, "protocol_version", manifest.ProtocolVersion, "agents", len(manifest.Agents))
	return nil
}
