package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
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

	// Agent artifacts: the directory holding agents-manifest.json and both
	// architecture-specific binaries (REQUIREMENTS §8.2).
	AgentArtifactsDir string

	// Metrics retention rollout (REQUIREMENTS §8.3.1 rollout: disabled by default,
	// then an operator-visible dry run, then enabled). The dry run always plans
	// and reports; only enabled && !dry-run deletes anything.
	MetricsRetentionEnabled bool
	MetricsRetentionDryRun  bool
	// MetricsRetentionParseError records an unparsable rollout switch so Validate
	// rejects startup instead of silently changing the deletion behaviour.
	MetricsRetentionParseError error

	// Monitoring
	MonitorInterval int
	// MonitorToleratedRounds and MonitorAllowedLatency derive the online/expired
	// freshness threshold together with MonitorInterval (REQUIREMENTS §8.1):
	// threshold = I×(rounds+1) + latency. The defaults (1 round, 0 latency)
	// reproduce the historical flat 120s at the default 60s interval.
	MonitorToleratedRounds int
	MonitorAllowedLatency  int

	// SSH
	SSHTimeout int
	SSHMaxIdle int

	// Exec budgets (REQUIREMENTS §7.1.2). The handler resolves the effective
	// timeout as request -> ExecTimeout -> ExecTimeoutMax; the output limits cap
	// what is retained in memory (stdout+stderr total, and each stream).
	ExecTimeout           int
	ExecTimeoutMax        int
	ExecOutputLimit       int64
	ExecOutputStreamLimit int64

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

// Validate rejects illegal configuration before the server starts serving.
func (c *Config) Validate() error {
	if c.MonitorInterval <= 0 {
		return fmt.Errorf("MONITOR_INTERVAL must be a positive integer, got %d", c.MonitorInterval)
	}
	if c.MetricsRetentionParseError != nil {
		return c.MetricsRetentionParseError
	}
	if c.MonitorToleratedRounds < 0 {
		return fmt.Errorf("MONITOR_TOLERATED_ROUNDS must not be negative, got %d", c.MonitorToleratedRounds)
	}
	if c.MonitorAllowedLatency < 0 {
		return fmt.Errorf("MONITOR_ALLOWED_LATENCY_SECONDS must not be negative, got %d", c.MonitorAllowedLatency)
	}
	if c.ExecTimeout <= 0 {
		return fmt.Errorf("EXEC_TIMEOUT must be positive, got %d", c.ExecTimeout)
	}
	if c.ExecTimeoutMax <= 0 || c.ExecTimeoutMax < c.ExecTimeout || c.ExecTimeoutMax > 300 {
		return fmt.Errorf("EXEC_TIMEOUT_MAX must be positive, >= EXEC_TIMEOUT and <= 300, got %d/%d", c.ExecTimeoutMax, c.ExecTimeout)
	}
	if c.ExecOutputLimit <= 0 || c.ExecOutputLimit > 8<<20 {
		return fmt.Errorf("EXEC_OUTPUT_LIMIT must be positive and <= 8388608, got %d", c.ExecOutputLimit)
	}
	if c.ExecOutputStreamLimit <= 0 || c.ExecOutputStreamLimit > c.ExecOutputLimit || c.ExecOutputStreamLimit > 4<<20 {
		return fmt.Errorf("EXEC_OUTPUT_STREAM_LIMIT must be positive, <= EXEC_OUTPUT_LIMIT and <= 4194304, got %d/%d", c.ExecOutputStreamLimit, c.ExecOutputLimit)
	}
	if c.SSHTimeout <= 0 {
		return fmt.Errorf("SSH_TIMEOUT must be positive, got %d", c.SSHTimeout)
	}
	if c.LoginRateLimit < 0 {
		return fmt.Errorf("LOGIN_RATE_LIMIT must not be negative, got %d", c.LoginRateLimit)
	}
	return nil
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
	cfg.AgentArtifactsDir = getEnvOrDefault("AGENT_ARTIFACTS_DIR", "/usr/local/bin")
	// The retention rollout switches are parsed strictly: a value that cannot be
	// interpreted must never turn the safe default (dry run) into deletion, and
	// Validate refuses to start instead (F01). The safe defaults are installed
	// first so a parse failure cannot leave the zero values (enabled=false,
	// dryRun=false) in place — dryRun=false is the deleting configuration.
	cfg.MetricsRetentionEnabled = false
	cfg.MetricsRetentionDryRun = true
	if enabled, err := getEnvBoolStrict("METRICS_RETENTION_ENABLED"); err != nil {
		cfg.MetricsRetentionParseError = err
	} else {
		cfg.MetricsRetentionEnabled = enabled
	}
	if dryRun, err := getEnvBoolStrict("METRICS_RETENTION_DRY_RUN"); err != nil {
		cfg.MetricsRetentionParseError = err
	} else {
		cfg.MetricsRetentionDryRun = dryRun
	}
	cfg.MonitorInterval = getEnvIntOrDefault("MONITOR_INTERVAL", 60)
	cfg.MonitorToleratedRounds = getEnvIntOrDefault("MONITOR_TOLERATED_ROUNDS", 1)
	cfg.MonitorAllowedLatency = getEnvIntOrDefault("MONITOR_ALLOWED_LATENCY_SECONDS", 0)
	cfg.SSHTimeout = getEnvIntOrDefault("SSH_TIMEOUT", 10)
	cfg.SSHMaxIdle = getEnvIntOrDefault("SSH_MAX_IDLE", 300)
	cfg.ExecTimeout = getEnvIntOrDefault("EXEC_TIMEOUT", 30)
	cfg.ExecTimeoutMax = getEnvIntOrDefault("EXEC_TIMEOUT_MAX", 300)
	cfg.ExecOutputLimit = int64(getEnvIntOrDefault("EXEC_OUTPUT_LIMIT", 8<<20))
	cfg.ExecOutputStreamLimit = int64(getEnvIntOrDefault("EXEC_OUTPUT_STREAM_LIMIT", 4<<20))
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

// MonitorFreshnessThreshold is the age after which a node without a fresh metric
// is reported offline/expired: I×(tolerated rounds+1) + allowed latency.
func (c *Config) MonitorFreshnessThreshold() time.Duration {
	rounds := c.MonitorToleratedRounds + 1
	if rounds < 1 {
		rounds = 1
	}
	return time.Duration(c.MonitorInterval*rounds)*time.Second +
		time.Duration(c.MonitorAllowedLatency)*time.Second
}

// getEnvBoolStrict parses a boolean environment variable and reports a value it
// cannot interpret. Callers that gate a destructive behaviour must use this: the
// lenient getEnvBool would silently fall back to false, which for a dry-run switch
// means "delete".
func getEnvBoolStrict(key string) (bool, error) {
	val := os.Getenv(key)
	if val == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		return false, fmt.Errorf("%s=%q is not a valid boolean", key, val)
	}
	return parsed, nil
}
