package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	// Database
	DatabaseURL string

	// Secrets
	MasterKey string
	JWTSecret string

	// Server
	Port string

	// Logging
	LogFormat string
	LogLevel  string

	// Monitoring
	MonitorInterval int

	// SSH
	SSHTimeout  int
	SSHMaxIdle  int
	ExecTimeout int

	// Rate Limiting
	LoginRateLimit int
	TrustProxy     bool

	DBMaxOpenConnections        int
	UsageWriteConcurrency       int
	UsageActiveLimit            int
	UsageRetentionDays          int
	UsageSensitiveRetentionDays int
	UsageLegacyWritersDrained   bool
}

// Load reads configuration from environment variables with sensible defaults.
// Fatals if required variables are missing.
func Load() *Config {
	cfg := &Config{}

	// Required
	cfg.DatabaseURL = requireEnv("DATABASE_URL")
	cfg.MasterKey = requireEnv("VPSMANAGER_MASTER_KEY")
	cfg.JWTSecret = requireEnv("JWT_SECRET")

	// Optional with defaults
	cfg.Port = getEnvOrDefault("PORT", "8080")
	cfg.LogFormat = getEnvOrDefault("LOG_FORMAT", "json")
	cfg.LogLevel = getEnvOrDefault("LOG_LEVEL", "info")
	cfg.MonitorInterval = getEnvIntOrDefault("MONITOR_INTERVAL", 60)
	cfg.SSHTimeout = getEnvIntOrDefault("SSH_TIMEOUT", 10)
	cfg.SSHMaxIdle = getEnvIntOrDefault("SSH_MAX_IDLE", 300)
	cfg.ExecTimeout = getEnvIntOrDefault("EXEC_TIMEOUT", 30)
	cfg.LoginRateLimit = getEnvIntOrDefault("LOGIN_RATE_LIMIT", 10)
	cfg.TrustProxy = getEnvBool("TRUST_PROXY")
	cfg.DBMaxOpenConnections = getEnvIntOrDefault("DB_MAX_OPEN_CONNECTIONS", 32)
	if cfg.DBMaxOpenConnections < 12 {
		cfg.DBMaxOpenConnections = 12
	}
	cfg.UsageWriteConcurrency = getEnvIntOrDefault("USAGE_LOG_WRITE_CONCURRENCY", 4)
	cfg.UsageActiveLimit = getEnvIntOrDefault("USAGE_LOG_ACTIVE_LIMIT", 4096)
	cfg.UsageRetentionDays = getEnvIntOrDefault("USAGE_LOG_RETENTION_DAYS", 30)
	cfg.UsageSensitiveRetentionDays = getEnvIntOrDefault("USAGE_LOG_SENSITIVE_RETENTION_DAYS", 90)
	cfg.UsageLegacyWritersDrained = getEnvBool("USAGE_LOG_LEGACY_WRITERS_DRAINED")

	return cfg
}

func requireEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		fmt.Fprintf(os.Stderr, "FATAL: required environment variable %q is not set\n", key)
		os.Exit(1)
	}
	return val
}

func getEnvOrDefault(key, defaultVal string) string {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	return val
}

func getEnvIntOrDefault(key string, defaultVal int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: environment variable %q=%q is not a valid integer, using default %d\n", key, val, defaultVal)
		return defaultVal
	}
	return n
}

func getEnvBool(key string) bool {
	val := os.Getenv(key)
	if val == "" {
		return false
	}
	b, err := strconv.ParseBool(val)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: environment variable %q=%q is not a valid boolean, using false\n", key, val)
		return false
	}
	return b
}
