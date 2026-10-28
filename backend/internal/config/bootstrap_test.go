package config

import (
	"strings"
	"testing"
)

func TestBootstrapTokenDeploymentValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int
		valid bool
	}{
		{"disabled", 0, true}, {"too_short", 31, false}, {"minimum", 32, true},
		{"generated_hex", 64, true}, {"maximum", 256, true}, {"too_long", 257, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := strings.Repeat("s", tc.size)
			err := (&Config{BootstrapToken: value}).ValidateDeployment()
			if (err == nil) != tc.valid {
				t.Fatalf("size=%d valid=%v got error=%v", tc.size, tc.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), value) {
				t.Fatal("validation error exposed the setup secret")
			}
		})
	}
}

func TestBootstrapTokenCannotReusePersistentSecrets(t *testing.T) {
	proof := strings.Repeat("a", 64)
	for _, cfg := range []Config{
		{BootstrapToken: proof, JWTSecret: proof},
		{BootstrapToken: proof, MasterKey: proof},
	} {
		if err := cfg.ValidateDeployment(); err == nil || strings.Contains(err.Error(), proof) {
			t.Fatalf("setup proof must be independent without exposing it: err=%v", err)
		}
	}
}
