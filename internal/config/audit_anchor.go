package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
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

// AuditAnchorS3Config names a bucket with S3 Object Lock that anchors are
// copied to. The bucket's default retention keeps them; credentials belong in
// environment secrets.
type AuditAnchorS3Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
	Prefix          string
}

// loadAuditAnchorS3 accepts only an https:// endpoint, like the HTTPS target.
// An empty endpoint means AWS, which is HTTPS. Any setting without a bucket
// stops the API, so a half-configured target never silently stays off.
func (cfg *Config) loadAuditAnchorS3() error {
	c := AuditAnchorS3Config{
		Endpoint:        strings.TrimSpace(getEnv("RESTMAIL_AUDIT_ANCHOR_S3_ENDPOINT", "")),
		Region:          strings.TrimSpace(getEnv("RESTMAIL_AUDIT_ANCHOR_S3_REGION", "us-east-1")),
		Bucket:          strings.TrimSpace(getEnv("RESTMAIL_AUDIT_ANCHOR_S3_BUCKET", "")),
		AccessKeyID:     strings.TrimSpace(getEnv("RESTMAIL_AUDIT_ANCHOR_S3_ACCESS_KEY_ID", "")),
		SecretAccessKey: strings.TrimSpace(getEnv("RESTMAIL_AUDIT_ANCHOR_S3_SECRET_ACCESS_KEY", "")),
		Prefix:          strings.TrimSpace(getEnv("RESTMAIL_AUDIT_ANCHOR_S3_PREFIX", "audit-anchors/")),
	}
	pathStyle, err := strconv.ParseBool(getEnv("RESTMAIL_AUDIT_ANCHOR_S3_FORCE_PATH_STYLE", "false"))
	if err != nil {
		return fmt.Errorf("RESTMAIL_AUDIT_ANCHOR_S3_FORCE_PATH_STYLE must be true or false")
	}
	c.ForcePathStyle = pathStyle
	if c.Bucket == "" {
		if c.Endpoint != "" || c.AccessKeyID != "" || c.SecretAccessKey != "" {
			return fmt.Errorf("RESTMAIL_AUDIT_ANCHOR_S3_* settings require RESTMAIL_AUDIT_ANCHOR_S3_BUCKET")
		}
		return nil
	}
	if c.Region == "" || c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return fmt.Errorf("RESTMAIL_AUDIT_ANCHOR_S3_BUCKET requires region, access key ID and secret access key")
	}
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("RESTMAIL_AUDIT_ANCHOR_S3_ENDPOINT must be an https:// origin without credentials, path or query")
		}
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_-]*/$`).MatchString(c.Prefix) || strings.Contains(c.Prefix, "//") {
		return fmt.Errorf("RESTMAIL_AUDIT_ANCHOR_S3_PREFIX must be a relative object prefix ending in /, using letters, digits, /, _ or -")
	}
	cfg.AuditAnchorS3 = c
	return nil
}
