package config

import (
	"strings"
	"testing"
)

// The cleartext client listeners are gone: submission is 465, IMAP is 993, POP3 is 995,
// all implicit TLS. 587/143/110 are not switched off by default — they cannot be asked
// for at all.
//
// A configuration that still sets one of the old variables is the dangerous case: it
// reads as "run a cleartext listener" and would silently do nothing, leaving the operator
// believing a port is open when it is not, or believing rest-mail still offers a
// cleartext path when it refuses one. So the variable is a boot-time finding rather than
// ignored.
func TestRemovedCleartextPortsAreRefusedInProduction(t *testing.T) {
	for _, c := range []struct {
		env     string
		role    ListenerRole
		replace string
	}{
		{"SMTP_PORT_SUBMISSION", RoleSMTPGateway, "465"},
		{"IMAP_PORT", RoleIMAPGateway, "993"},
		{"POP3_PORT", RolePOP3Gateway, "995"},
	} {
		t.Run(c.env, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DB_SSLMODE", "require")
			t.Setenv(c.env, "2525")
			cfg := secureGatewayCfg("production")

			err := cfg.ValidateListenerSecurity(c.role)
			if err == nil {
				t.Fatalf("%s should refuse to boot in production", c.env)
			}
			if !strings.Contains(err.Error(), c.env) {
				t.Errorf("error should name %s, got: %v", c.env, err)
			}
			// Naming the replacement is the difference between a refusal someone can act
			// on and one they have to go and read the source about.
			if !strings.Contains(err.Error(), c.replace) {
				t.Errorf("error should point at port %s, got: %v", c.replace, err)
			}
		})
	}
}

// Warn-and-boot in development, like every other finding: the testbed can carry a stale
// variable through a rename without a hard stop, and the operator still sees it.
func TestRemovedCleartextPortsWarnInDevelopment(t *testing.T) {
	clearEnv(t)
	t.Setenv("SMTP_PORT_SUBMISSION", "587")
	cfg := secureGatewayCfg("development")

	if err := cfg.ValidateListenerSecurity(RoleSMTPGateway); err != nil {
		t.Fatalf("a stale variable must warn and boot in development, got: %v", err)
	}
}

// An empty value is not a request for anything — it is what an unset variable looks like
// once a template has interpolated nothing into it, which is common in generated env
// files and must not be treated as an attempt to open a cleartext port.
func TestRemovedCleartextPortsIgnoreEmptyValues(t *testing.T) {
	clearEnv(t)
	t.Setenv("DB_SSLMODE", "require")
	t.Setenv("SMTP_PORT_SUBMISSION", "")
	t.Setenv("IMAP_PORT", "  ")
	cfg := secureGatewayCfg("production")

	if err := cfg.ValidateListenerSecurity(RoleSMTPGateway); err != nil {
		t.Errorf("an empty SMTP_PORT_SUBMISSION must not be a finding, got: %v", err)
	}
	if err := cfg.ValidateListenerSecurity(RoleIMAPGateway); err != nil {
		t.Errorf("a blank IMAP_PORT must not be a finding, got: %v", err)
	}
}

// Zero is an explicit "no listener", which is exactly what this change enforces. Someone
// carrying SMTP_PORT_SUBMISSION=0 through a migration is already in the right state and
// should not be told off for it.
func TestRemovedCleartextPortsAcceptExplicitZero(t *testing.T) {
	clearEnv(t)
	t.Setenv("DB_SSLMODE", "require")
	t.Setenv("SMTP_PORT_SUBMISSION", "0")
	cfg := secureGatewayCfg("production")

	if err := cfg.ValidateListenerSecurity(RoleSMTPGateway); err != nil {
		t.Errorf("SMTP_PORT_SUBMISSION=0 already means no listener, got: %v", err)
	}
}
