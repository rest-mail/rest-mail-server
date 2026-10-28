package config

import (
	"strings"
	"testing"
)

func TestLoad_EnvironmentDefaultProduction(t *testing.T) {
	clearEnv(t)
	validSecretsProd(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with valid secrets: %v", err)
	}
	if cfg.Environment != "production" || !cfg.IsProduction() {
		t.Fatalf("unset ENVIRONMENT = %q, want production", cfg.Environment)
	}
}

func TestLoad_DefaultEnvironmentRejectsDevelopmentSecrets(t *testing.T) {
	clearEnv(t)

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Fatalf("Load() with unset ENVIRONMENT must reject development secrets, got %v", err)
	}
}

func TestLoad_ExplicitNonProductionEnvironment(t *testing.T) {
	for _, env := range []string{"development", "test"} {
		t.Run(env, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("ENVIRONMENT", env)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() with explicit ENVIRONMENT=%s: %v", env, err)
			}
			if cfg.Environment != env || cfg.IsProduction() {
				t.Fatalf("Environment = %q, want %q", cfg.Environment, env)
			}
		})
	}
}
