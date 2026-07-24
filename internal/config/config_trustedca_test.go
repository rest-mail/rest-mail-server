package config

import "testing"

// TestLoad_TrustedCACertDefault pins the de-hardcoded outbound-trust CA path
// (PR6 / G7): with TRUSTED_CA_CERT unset the default is exactly the historical
// literal "/certs/ca.crt", so the testbed is byte-for-byte unchanged.
func TestLoad_TrustedCACertDefault(t *testing.T) {
	// Development env so validateSecurityConfig only warns (no MASTER_KEY needed).
	t.Setenv("ENVIRONMENT", "development")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TrustedCACertPath != "/certs/ca.crt" {
		t.Errorf("TrustedCACertPath = %q, want /certs/ca.crt (unchanged testbed default)", cfg.TrustedCACertPath)
	}
}

// TestLoad_TrustedCACertOverride proves a deployer can relocate the trust anchor,
// and that an explicit empty value disables the extra trust (system roots only).
func TestLoad_TrustedCACertOverride(t *testing.T) {
	t.Setenv("ENVIRONMENT", "development")

	t.Setenv("TRUSTED_CA_CERT", "/etc/pki/custom-ca.pem")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TrustedCACertPath != "/etc/pki/custom-ca.pem" {
		t.Errorf("TrustedCACertPath = %q, want /etc/pki/custom-ca.pem", cfg.TrustedCACertPath)
	}

	t.Setenv("TRUSTED_CA_CERT", "")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load (empty): %v", err)
	}
	if cfg.TrustedCACertPath != "" {
		t.Errorf("TrustedCACertPath = %q, want empty (extra trust disabled)", cfg.TrustedCACertPath)
	}
}
