package config

import (
	"fmt"
	"strconv"
	"strings"

	rmail "github.com/restmail/restmail/internal/mail"
)

func (cfg *Config) loadAuditAnchorEmail() error {
	enabled, err := strconv.ParseBool(getEnv("RESTMAIL_AUDIT_ANCHOR_EMAIL_ENABLED", "true"))
	if err != nil {
		return fmt.Errorf("RESTMAIL_AUDIT_ANCHOR_EMAIL_ENABLED must be a boolean: %w", err)
	}
	cfg.AuditAnchorEmailEnabled = enabled
	spec := strings.TrimSpace(getEnv("RESTMAIL_AUDIT_ANCHOR_EMAIL_TO", ""))
	if spec == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, item := range strings.Split(spec, ",") {
		address, err := rmail.BareAddress(item)
		if err != nil {
			return fmt.Errorf("RESTMAIL_AUDIT_ANCHOR_EMAIL_TO must be a comma-separated list of mailbox addresses: %w", err)
		}
		if !seen[address] {
			cfg.AuditAnchorEmailTo = append(cfg.AuditAnchorEmailTo, address)
			seen[address] = true
		}
	}
	return nil
}
