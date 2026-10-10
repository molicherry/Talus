package config

import "testing"

func validConfig() *Config {
	return &Config{
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

func TestValidateRejectsIllegalValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"zero monitor interval", func(c *Config) { c.MonitorInterval = 0 }},
		{"negative monitor interval", func(c *Config) { c.MonitorInterval = -1 }},
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
