package config

import (
	"strings"
	"testing"
)

// secureGatewayCfg returns a Config with all protocol ports enabled and a TLS
// keypair configured, in the given environment. Individual tests blank the
// keypair or flip env knobs to exercise a single finding.
func secureGatewayCfg(env string) *Config {
	return &Config{
		Environment:           env,
		TLSCertPath:           "/certs/mail.test.crt",
		TLSKeyPath:            "/certs/mail.test.key",
		SMTPPortInbound:       25,
		SMTPPortSubmission:    587,
		SMTPPortSubmissionTLS: 465,
		IMAPPort:              143,
		IMAPTLSPort:           993,
		POP3Port:              110,
		POP3TLSPort:           995,
		APIHost:               "0.0.0.0",
		APIPort:               8080,
	}
}

// gatewayRoles is the set of roles whose secure posture is "a TLS keypair must
// be present"; API is handled separately (it never terminates TLS itself).
var gatewayRoles = []struct {
	name string
	role ListenerRole
}{
	{"smtp", RoleSMTPGateway},
	{"imap", RoleIMAPGateway},
	{"pop3", RolePOP3Gateway},
}

// TestValidateListenerSecurity_GatewayNoCert_Production: a protocol gateway with
// listeners enabled but no TLS keypair must refuse to boot in production.
func TestValidateListenerSecurity_GatewayNoCert_Production(t *testing.T) {
	for _, gr := range gatewayRoles {
		t.Run(gr.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DB_SSLMODE", "require") // isolate the listener finding
			cfg := secureGatewayCfg("production")
			cfg.TLSCertPath = ""
			cfg.TLSKeyPath = ""

			err := cfg.ValidateListenerSecurity(gr.role)
			if err == nil {
				t.Fatalf("%s gateway without TLS keypair should refuse to boot in production", gr.name)
			}
			if !strings.Contains(err.Error(), "TLS keypair") {
				t.Errorf("error should name the missing TLS keypair, got: %v", err)
			}
		})
	}
}

// TestValidateListenerSecurity_GatewayNoCert_Development: the identical insecure
// config only warns and boots in development (the testbed/e2e default).
func TestValidateListenerSecurity_GatewayNoCert_Development(t *testing.T) {
	for _, gr := range gatewayRoles {
		t.Run(gr.name, func(t *testing.T) {
			clearEnv(t)
			cfg := secureGatewayCfg("development")
			cfg.TLSCertPath = ""
			cfg.TLSKeyPath = ""

			if err := cfg.ValidateListenerSecurity(gr.role); err != nil {
				t.Fatalf("%s gateway without TLS keypair must warn+boot (not error) in development, got: %v", gr.name, err)
			}
		})
	}
}

// TestValidateListenerSecurity_GatewayWithCert_Production: a fully-valid
// production gateway config (keypair present, secure DB mode) boots clean.
func TestValidateListenerSecurity_GatewayWithCert_Production(t *testing.T) {
	for _, gr := range gatewayRoles {
		t.Run(gr.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DB_SSLMODE", "require")
			cfg := secureGatewayCfg("production")

			if err := cfg.ValidateListenerSecurity(gr.role); err != nil {
				t.Fatalf("%s gateway with TLS keypair + secure DB should boot clean in production, got: %v", gr.name, err)
			}
		})
	}
}

// TestValidateListenerSecurity_GatewayNoPortsNoCert: with every listener port
// disabled there is nothing to protect, so a missing keypair is not a finding
// even in production.
func TestValidateListenerSecurity_GatewayNoPortsNoCert(t *testing.T) {
	clearEnv(t)
	t.Setenv("DB_SSLMODE", "require")
	cfg := &Config{Environment: "production"} // all ports zero, no cert

	for _, gr := range gatewayRoles {
		if err := cfg.ValidateListenerSecurity(gr.role); err != nil {
			t.Fatalf("%s: no ports enabled should not require a keypair, got: %v", gr.name, err)
		}
	}
}

// TestValidateListenerSecurity_API_Production: the API serves plaintext HTTP, so
// production requires the reverse-proxy acknowledgement; without it, refuse.
func TestValidateListenerSecurity_API_Production(t *testing.T) {
	clearEnv(t)
	t.Setenv("DB_SSLMODE", "require") // isolate the API/proxy finding
	cfg := secureGatewayCfg("production")

	err := cfg.ValidateListenerSecurity(RoleAPI)
	if err == nil {
		t.Fatal("API in production without API_TLS_TERMINATED_BY_PROXY should refuse to boot")
	}
	if !strings.Contains(err.Error(), "API_TLS_TERMINATED_BY_PROXY") {
		t.Errorf("error should name the proxy ack knob, got: %v", err)
	}
}

// TestValidateListenerSecurity_API_ProxyAck: acknowledging the TLS-terminating
// proxy boots clean in production.
func TestValidateListenerSecurity_API_ProxyAck(t *testing.T) {
	clearEnv(t)
	t.Setenv("DB_SSLMODE", "require")
	t.Setenv("API_TLS_TERMINATED_BY_PROXY", "true")
	cfg := secureGatewayCfg("production")

	if err := cfg.ValidateListenerSecurity(RoleAPI); err != nil {
		t.Fatalf("API with proxy ack + secure DB should boot clean in production, got: %v", err)
	}
}

// TestValidateListenerSecurity_API_Development: without the ack the API only
// warns and boots in development.
func TestValidateListenerSecurity_API_Development(t *testing.T) {
	clearEnv(t)
	cfg := secureGatewayCfg("development")

	if err := cfg.ValidateListenerSecurity(RoleAPI); err != nil {
		t.Fatalf("API in development without proxy ack must warn+boot (not error), got: %v", err)
	}
}

// TestValidateListenerSecurity_QueueTLSInsecure_Production: QUEUE_TLS_INSECURE=true
// disables outbound TLS verification and must refuse to boot in production even
// with a valid keypair.
func TestValidateListenerSecurity_QueueTLSInsecure_Production(t *testing.T) {
	clearEnv(t)
	t.Setenv("DB_SSLMODE", "require")
	t.Setenv("QUEUE_TLS_INSECURE", "true")
	cfg := secureGatewayCfg("production")

	err := cfg.ValidateListenerSecurity(RoleSMTPGateway)
	if err == nil {
		t.Fatal("QUEUE_TLS_INSECURE=true should refuse to boot in production")
	}
	if !strings.Contains(err.Error(), "QUEUE_TLS_INSECURE") {
		t.Errorf("error should name QUEUE_TLS_INSECURE, got: %v", err)
	}
}

// TestValidateListenerSecurity_QueueTLSInsecure_Development: the same knob is
// honored (warn only) in development so the testbed's outbound delivery works.
func TestValidateListenerSecurity_QueueTLSInsecure_Development(t *testing.T) {
	clearEnv(t)
	t.Setenv("QUEUE_TLS_INSECURE", "true")
	cfg := secureGatewayCfg("development")

	if err := cfg.ValidateListenerSecurity(RoleSMTPGateway); err != nil {
		t.Fatalf("QUEUE_TLS_INSECURE=true must warn+boot (not error) in development, got: %v", err)
	}
}

// TestValidateListenerSecurity_DBSSLModeDisable_Production: a cleartext DB link is
// refused in production unless acknowledged. Exercised through RoleAPI with the
// proxy ack set so the only remaining finding is the DB one.
func TestValidateListenerSecurity_DBSSLModeDisable_Production(t *testing.T) {
	clearEnv(t)
	t.Setenv("API_TLS_TERMINATED_BY_PROXY", "true")
	// DB_SSLMODE unset → defaults to "disable" (insecure).
	cfg := secureGatewayCfg("production")

	err := cfg.ValidateListenerSecurity(RoleAPI)
	if err == nil {
		t.Fatal("DB_SSLMODE=disable should refuse to boot in production")
	}
	if !strings.Contains(err.Error(), "DB_SSLMODE") {
		t.Errorf("error should name DB_SSLMODE, got: %v", err)
	}
}

// TestValidateListenerSecurity_DBAllowInsecureAck: acknowledging the cleartext DB
// link clears the finding in production.
func TestValidateListenerSecurity_DBAllowInsecureAck(t *testing.T) {
	clearEnv(t)
	t.Setenv("API_TLS_TERMINATED_BY_PROXY", "true")
	t.Setenv("DB_ALLOW_INSECURE", "true")
	cfg := secureGatewayCfg("production")

	if err := cfg.ValidateListenerSecurity(RoleAPI); err != nil {
		t.Fatalf("DB_ALLOW_INSECURE=true should clear the DB finding in production, got: %v", err)
	}
}

// TestValidateListenerSecurity_DBSSLModeSecure: a secure sslmode boots clean.
func TestValidateListenerSecurity_DBSSLModeSecure(t *testing.T) {
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		t.Run(mode, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("API_TLS_TERMINATED_BY_PROXY", "true")
			t.Setenv("DB_SSLMODE", mode)
			cfg := secureGatewayCfg("production")

			if err := cfg.ValidateListenerSecurity(RoleAPI); err != nil {
				t.Fatalf("DB_SSLMODE=%s should boot clean in production, got: %v", mode, err)
			}
		})
	}
}

// TestValidateListenerSecurity_DBSSLModeDisable_Development: cleartext DB only
// warns in development (the testbed runs sslmode=disable).
func TestValidateListenerSecurity_DBSSLModeDisable_Development(t *testing.T) {
	clearEnv(t)
	cfg := secureGatewayCfg("development")

	if err := cfg.ValidateListenerSecurity(RoleAPI); err != nil {
		t.Fatalf("DB_SSLMODE=disable must warn+boot (not error) in development, got: %v", err)
	}
}

// TestDBSSLMode reflects DB_SSLMODE into the accessor and DSN.
func TestDBSSLMode(t *testing.T) {
	clearEnv(t)
	cfg := &Config{DBHost: "h", DBPort: 5432, DBUser: "u", DBPass: "p", DBName: "d"}

	// Default: disable (testbed byte-for-byte unchanged).
	if got := cfg.DBSSLMode(); got != "disable" {
		t.Errorf("default DBSSLMode = %q, want disable", got)
	}
	if !strings.Contains(cfg.DSN(), "sslmode=disable") {
		t.Errorf("default DSN should contain sslmode=disable, got: %q", cfg.DSN())
	}

	t.Setenv("DB_SSLMODE", "require")
	if got := cfg.DBSSLMode(); got != "require" {
		t.Errorf("DBSSLMode = %q, want require", got)
	}
	if !strings.Contains(cfg.DSN(), "sslmode=require") {
		t.Errorf("DSN should contain sslmode=require, got: %q", cfg.DSN())
	}
}

// TestLoad_ProductionDoesNotEnforceListeners proves the listener/DB enforcement
// is NOT wired into Load(): a bare production config (valid secrets, no cert, no
// proxy ack, sslmode=disable) still loads — the e2e/testbed path and every
// non-listener binary keep booting from Load() unchanged. Enforcement fires only
// when a listener binary calls ValidateListenerSecurity.
func TestLoad_ProductionDoesNotEnforceListeners(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("JWT_SECRET", "a-strong-production-secret-32-bytes-long")
	t.Setenv("MASTER_KEY", "a-strong-master-key-16plus")
	// No TLS cert, no API_TLS_TERMINATED_BY_PROXY, no DB_SSLMODE → all insecure.

	if _, err := Load(); err != nil {
		t.Fatalf("Load() must not enforce listener/DB security (that is the cmd boot path's job), got: %v", err)
	}
}

// TestIsInsecureDBSSLMode enumerates the mode classification.
func TestIsInsecureDBSSLMode(t *testing.T) {
	insecure := []string{"disable", "allow", "prefer"}
	secure := []string{"require", "verify-ca", "verify-full"}
	for _, m := range insecure {
		if !isInsecureDBSSLMode(m) {
			t.Errorf("sslmode %q should be classified insecure", m)
		}
	}
	for _, m := range secure {
		if isInsecureDBSSLMode(m) {
			t.Errorf("sslmode %q should be classified secure", m)
		}
	}
}
