package config

import (
	"strings"
	"testing"
)

// The SMTP gateway rewrites every forward's sender with this secret (#775), so
// production refuses to start without a real one. Development warns and starts.
func TestSRSSecretValidatedForTheSMTPGateway(t *testing.T) {
	strong := strings.Repeat("f", MinSRSSecretLength)
	for name, tc := range map[string]struct {
		secret  string
		refused bool
	}{
		"empty":       {"", true},
		"dev default": {devDefaultSRSSecret, true},
		"too short":   {strings.Repeat("f", MinSRSSecretLength-1), true},
		"strong":      {strong, false},
	} {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DB_SSLMODE", "require") // isolate the secret's finding
			cfg := secureGatewayCfg("production")
			cfg.SRSSecret = tc.secret
			err := cfg.ValidateListenerSecurity(RoleSMTPGateway)
			if tc.refused && (err == nil || !strings.Contains(err.Error(), "SRS_SECRET")) {
				t.Fatalf("production: err = %v, want a refusal naming SRS_SECRET", err)
			}
			if !tc.refused && err != nil {
				t.Fatalf("production: %v", err)
			}
			dev := secureGatewayCfg("development")
			dev.SRSSecret = tc.secret
			if err := dev.ValidateListenerSecurity(RoleSMTPGateway); err != nil {
				t.Fatalf("development refused: %v", err)
			}
		})
	}
}

// Only the SMTP gateway uses the secret, so no other process needs it.
func TestSRSSecretNotRequiredElsewhere(t *testing.T) {
	clearEnv(t)
	t.Setenv("DB_SSLMODE", "require")
	for _, role := range []ListenerRole{RoleIMAPGateway, RolePOP3Gateway, RoleAPI} {
		cfg := secureGatewayCfg("production")
		cfg.SRSSecret = ""
		if err := cfg.ValidateListenerSecurity(role); err != nil && strings.Contains(err.Error(), "SRS_SECRET") {
			t.Errorf("%s: %v", role, err)
		}
	}
}

// Load reads both secrets from the environment, and the previous one is optional.
func TestSRSSecretsLoaded(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("SRS_SECRET", "fake-srs-secret-current-for-tests")
	t.Setenv("SRS_PREVIOUS_SECRET", "fake-srs-secret-previous-for-tests")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SRSSecret != "fake-srs-secret-current-for-tests" || cfg.SRSPreviousSecret != "fake-srs-secret-previous-for-tests" {
		t.Fatalf("loaded %q and %q", cfg.SRSSecret, cfg.SRSPreviousSecret)
	}
}
