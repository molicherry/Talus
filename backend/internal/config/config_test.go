package config

import (
	"testing"
	"time"
)

func validConfig() *Config {
	return &Config{
		AgentArtifactsDir:     "/usr/local/bin",
		MonitorInterval:       60,
		ExecTimeout:           30,
		ExecTimeoutMax:        300,
		ExecOutputLimit:       8 << 20,
		ExecOutputStreamLimit: 4 << 20,
		SSHTimeout:            10,
		LoginRateLimit:        10,
	}
}

func TestValidateAcceptsDefaults(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("a valid configuration was rejected: %v", err)
	}
}

func TestResidentMonitoringConfiguration(t *testing.T) {
	c := validConfig()
	c.MonitorInterval = 5
	c.AgentHubURL = "https://hub.example.com/talus"
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	c.AgentHubURL = "http://hub.example.com"
	if c.Validate() == nil {
		t.Fatal("unencrypted public endpoint accepted without opt-in")
	}
	c.AgentAllowHTTP = true
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	c.MonitorInterval = 600
	if c.Validate() == nil {
		t.Fatal("resident interval exceeds supported bounds")
	}
}

func TestValidateRejectsIllegalValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"zero monitor interval", func(c *Config) { c.MonitorInterval = 0 }},
		{"negative monitor interval", func(c *Config) { c.MonitorInterval = -1 }},
		{"timeout above hard cap", func(c *Config) { c.ExecTimeoutMax = 301 }},
		{"total output above hard cap", func(c *Config) { c.ExecOutputLimit = (8 << 20) + 1 }},
		{"stream output above hard cap", func(c *Config) { c.ExecOutputStreamLimit = (4 << 20) + 1 }},
		{"zero exec timeout", func(c *Config) { c.ExecTimeout = 0 }},
		{"exec max below default", func(c *Config) { c.ExecTimeoutMax = 10 }},
		{"zero output limit", func(c *Config) { c.ExecOutputLimit = 0 }},
		{"stream limit above total", func(c *Config) { c.ExecOutputStreamLimit = c.ExecOutputLimit + 1 }},
		{"zero ssh timeout", func(c *Config) { c.SSHTimeout = 0 }},
		{"negative rate limit", func(c *Config) { c.LoginRateLimit = -1 }},
	}
	for _, tc := range cases {
		cfg := validConfig()
		tc.mutate(cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s: expected a validation error", tc.name)
		}
	}
}

func TestValidateAcceptsReducedExecutionLimits(t *testing.T) {
	c := validConfig()
	c.ExecTimeout, c.ExecTimeoutMax = 5, 10
	c.ExecOutputLimit, c.ExecOutputStreamLimit = 1024, 512
	if err := c.Validate(); err != nil {
		t.Fatalf("reduced execution limits were rejected: %v", err)
	}
}

// TestMonitorFreshnessThresholdDerivation pins T3.1's derived threshold: the
// historical 120s is I×(rounds+1)+latency at the default 60s interval, and a
// longer interval scales with it instead of staying flat.
func TestMonitorFreshnessThresholdDerivation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want time.Duration
	}{
		{"default interval reproduces 120s", Config{MonitorInterval: 60, MonitorToleratedRounds: 1}, 120 * time.Second},
		{"600s interval is not a flat threshold", Config{MonitorInterval: 600, MonitorToleratedRounds: 1}, 1200 * time.Second},
		{"tolerated rounds add whole intervals", Config{MonitorInterval: 60, MonitorToleratedRounds: 3}, 240 * time.Second},
		{"allowed latency is added", Config{MonitorInterval: 60, MonitorToleratedRounds: 1, MonitorAllowedLatency: 30}, 150 * time.Second},
		{"zero rounds still tolerates the current interval", Config{MonitorInterval: 60, MonitorToleratedRounds: 0}, 60 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.MonitorFreshnessThreshold(); got != tc.want {
				t.Fatalf("threshold = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestValidateRejectsNegativeMonitorTuning pins the startup validation.
func TestValidateRejectsNegativeMonitorTuning(t *testing.T) {
	base := *validConfig()
	base.MonitorToleratedRounds = 1
	if err := base.Validate(); err != nil {
		t.Fatalf("baseline config must be valid: %v", err)
	}
	bad := base
	bad.MonitorToleratedRounds = -1
	if err := bad.Validate(); err == nil {
		t.Fatal("negative MONITOR_TOLERATED_ROUNDS must be rejected")
	}
	bad2 := base
	bad2.MonitorAllowedLatency = -5
	if err := bad2.Validate(); err == nil {
		t.Fatal("negative MONITOR_ALLOWED_LATENCY_SECONDS must be rejected")
	}
}
