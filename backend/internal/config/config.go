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
	RateLimit int
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
	cfg.RateLimit = getEnvIntOrDefault("RATE_LIMIT", 100)

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
