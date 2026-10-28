package config

import "testing"

func TestLoadRecoveryEmailBaseURLIsIndependentOfGatewayURL(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENVIRONMENT", "test")
	t.Setenv("API_BASE_URL", "http://api:8080")
	t.Setenv("RESTMAIL_RECOVERY_EMAIL_BASE_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RecoveryEmailBaseURL != "" {
		t.Fatalf("recovery links need an explicit public origin: %q", cfg.RecoveryEmailBaseURL)
	}
	t.Setenv("RESTMAIL_RECOVERY_EMAIL_BASE_URL", "https://mail.example.test")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RecoveryEmailBaseURL != "https://mail.example.test" || cfg.APIBaseURL != "http://api:8080" {
		t.Fatalf("gateway %q and recovery %q must remain independent", cfg.APIBaseURL, cfg.RecoveryEmailBaseURL)
	}
}
