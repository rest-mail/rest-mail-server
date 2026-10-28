package config

import (
	"fmt"
	"net/url"
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

// loadAuditAnchorHTTPS accepts only https:// so an anchor cannot travel in the
// clear or be altered on the way; a plain http:// endpoint stops the API.
func (cfg *Config) loadAuditAnchorHTTPS() error {
	endpoint := strings.TrimSpace(getEnv("RESTMAIL_AUDIT_ANCHOR_HTTPS_URL", ""))
	token := strings.TrimSpace(getEnv("RESTMAIL_AUDIT_ANCHOR_HTTPS_TOKEN", ""))
	if endpoint == "" {
		if token != "" {
			return fmt.Errorf("RESTMAIL_AUDIT_ANCHOR_HTTPS_TOKEN is set without RESTMAIL_AUDIT_ANCHOR_HTTPS_URL")
		}
		return nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("RESTMAIL_AUDIT_ANCHOR_HTTPS_URL must be an https:// URL with a host and no credentials")
	}
	if strings.ContainsAny(token, "\r\n") {
		return fmt.Errorf("RESTMAIL_AUDIT_ANCHOR_HTTPS_TOKEN must be a single line")
	}
	cfg.AuditAnchorHTTPSURL = endpoint
	cfg.AuditAnchorHTTPSToken = token
	return nil
}
