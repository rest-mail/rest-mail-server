package config

import "testing"

// Issue #196: every fail-closed security gate keyed on ENVIRONMENT == "production"
// exactly, so "Production", "PRODUCTION" and the "prod" shorthand booted with
// warnings only — a casing typo silently dropped all production hardening. Load()
// must refuse an insecure config for ANY prod-family ENVIRONMENT value.
func TestEnvCase_ProductionEnforcementIsCaseInsensitive(t *testing.T) {
	for _, env := range []string{"Production", "PRODUCTION", "prod", "PROD", "  production  "} {
		t.Run(env, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("ENVIRONMENT", env)
			t.Setenv("JWT_SECRET", "")                            // insecure: empty secret
			t.Setenv("MASTER_KEY", "a-strong-master-key-16plus") // valid, so JWT is the sole finding

			if _, err := Load(); err == nil {
				t.Fatalf("Load() with ENVIRONMENT=%q and an empty JWT_SECRET must refuse to boot, got nil", env)
			}
		})
	}
}

// IsProduction / isProductionEnv are the shared predicate every enforcement site
// keys on. They must recognize the whole prod family, trimmed and
// case-insensitively, and must not misclassify non-prod environments — and the
// exported and internal forms must always agree.
func TestEnvCase_IsProductionEnv(t *testing.T) {
	prod := []string{"production", "Production", "PRODUCTION", "prod", "PROD", "  Prod  ", "prod-eu", "production-us"}
	for _, env := range prod {
		c := &Config{Environment: env}
		if !c.IsProduction() {
			t.Errorf("IsProduction(%q) = false, want true", env)
		}
		if c.isProductionEnv() != c.IsProduction() {
			t.Errorf("isProductionEnv(%q) disagrees with IsProduction", env)
		}
	}
	nonProd := []string{"development", "dev", "test", "staging", "local", ""}
	for _, env := range nonProd {
		c := &Config{Environment: env}
		if c.IsProduction() {
			t.Errorf("IsProduction(%q) = true, want false", env)
		}
		if c.isProductionEnv() != c.IsProduction() {
			t.Errorf("isProductionEnv(%q) disagrees with IsProduction", env)
		}
	}
}
