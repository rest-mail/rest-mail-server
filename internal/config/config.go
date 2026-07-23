package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultSMTPMaxMessageSize is the SMTP maximum message size applied when
// SMTP_MAX_MESSAGE_SIZE is unset: 10 MiB, the previously compiled-in value.
const DefaultSMTPMaxMessageSize = 10 * 1024 * 1024

// SMTPMaxMessageSizeWarnThreshold is the advisory ceiling above which a
// configured SMTP max message size triggers a startup warning (not an error):
// the gateway buffers each message fully in memory during DATA and the
// delivery pipeline parses it in memory, so very large limits multiply
// per-connection memory and parse cost.
const SMTPMaxMessageSizeWarnThreshold = 100 * 1024 * 1024

type Config struct {
	// Database
	DBHost            string
	DBPort            int
	DBName            string
	DBUser            string
	DBPass            string
	DBMaxOpenConns    int
	DBMaxIdleConns    int
	DBConnMaxLifetime time.Duration

	// API Server
	APIPort  int
	APIHost  string
	LogLevel string

	// TLS
	TLSCertPath string
	TLSKeyPath  string
	TLSCertDir  string // directory containing per-domain cert/key pairs for SNI

	// JWT
	JWTSecret            string
	JWTAccessExpiry      time.Duration
	JWTRefreshExpiry     time.Duration

	// Master key for encrypting private keys at rest
	MasterKey string

	// DNS Provider
	DNSProvider string

	// Gateway
	GatewayHostname string
	APIBaseURL      string // internal API URL for the gateway
	// SMTPMaxMessageSize is the maximum accepted SMTP message size in bytes.
	// It drives both the EHLO SIZE advertisement and DATA enforcement. Always
	// positive: unset falls back to DefaultSMTPMaxMessageSize, and zero or
	// negative values are a startup configuration error (a maximum must
	// always exist; "unlimited" is not an option).
	SMTPMaxMessageSize int64
	SMTPPortInbound    int
	SMTPPortSubmission int
	SMTPPortSubmissionTLS int
	IMAPPort        int
	IMAPTLSPort     int
	POP3Port        int
	POP3TLSPort     int
	QueueWorkers    int
	QueuePollInterval time.Duration
	MTASTSEnforce bool // enforce recipient MTA-STS policies on outbound delivery (RFC 8461)

	// PROXY protocol
	ProxyProtocolTrustedCIDRs []string

	// CORS
	CORSAllowedOrigins []string

	// ACME (Let's Encrypt)
	ACMEEnabled   bool
	ACMEEmail     string
	ACMEDirectory string // ACME directory URL; defaults to Let's Encrypt production
	ACMEStaging   bool   // use Let's Encrypt staging directory

	// Environment
	Environment string // "development", "production", "test"
}

func Load() (*Config, error) {
	cfg := &Config{
		DBHost:            getEnv("DB_HOST", "localhost"),
		DBPort:            getEnvInt("DB_PORT", 5432),
		DBName:            getEnv("DB_NAME", "restmail"),
		DBUser:            getEnv("DB_USER", "restmail"),
		DBPass:            getEnv("DB_PASS", "restmail"),
		DBMaxOpenConns:    getEnvInt("DB_MAX_OPEN_CONNS", 25),
		DBMaxIdleConns:    getEnvInt("DB_MAX_IDLE_CONNS", 10),
		DBConnMaxLifetime: getEnvDuration("DB_CONN_MAX_LIFETIME", 5*time.Minute),

		APIPort:  getEnvInt("API_PORT", 8080),
		APIHost:  getEnv("API_HOST", "0.0.0.0"),
		LogLevel: getEnv("LOG_LEVEL", "info"),

		TLSCertPath: getEnv("TLS_CERT_PATH", ""),
		TLSKeyPath:  getEnv("TLS_KEY_PATH", ""),
		TLSCertDir:  getEnv("TLS_CERT_DIR", ""),

		JWTSecret:        getEnv("JWT_SECRET", "dev-secret-change-in-production"),
		JWTAccessExpiry:  getEnvDuration("JWT_ACCESS_EXPIRY", 15*time.Minute),
		JWTRefreshExpiry: getEnvDuration("JWT_REFRESH_EXPIRY", 7*24*time.Hour),

		MasterKey: getEnv("MASTER_KEY", ""),

		DNSProvider: getEnv("DNS_PROVIDER", "dnsmasq"),

		GatewayHostname:       getEnv("GATEWAY_HOSTNAME", "localhost"),
		APIBaseURL:            getEnv("API_BASE_URL", "http://localhost:8080"),
		SMTPPortInbound:       getEnvInt("SMTP_PORT_INBOUND", 25),
		SMTPPortSubmission:    getEnvInt("SMTP_PORT_SUBMISSION", 587),
		SMTPPortSubmissionTLS: getEnvInt("SMTP_PORT_SUBMISSION_TLS", 465),
		IMAPPort:              getEnvInt("IMAP_PORT", 143),
		IMAPTLSPort:           getEnvInt("IMAP_TLS_PORT", 993),
		POP3Port:              getEnvInt("POP3_PORT", 110),
		POP3TLSPort:           getEnvInt("POP3_TLS_PORT", 995),
		QueueWorkers:          getEnvInt("QUEUE_WORKERS", 4),
		QueuePollInterval:     getEnvDuration("QUEUE_POLL_INTERVAL", 5*time.Second),
		MTASTSEnforce:         getEnvBool("MTASTS_ENFORCE", true),

		ProxyProtocolTrustedCIDRs: getEnvSlice("PROXY_PROTOCOL_TRUSTED_CIDRS", nil),

		CORSAllowedOrigins: getEnvSlice("CORS_ALLOWED_ORIGINS", []string{"http://localhost:3000"}),

		ACMEEnabled:   getEnvBool("ACME_ENABLED", false),
		ACMEEmail:     getEnv("ACME_EMAIL", ""),
		ACMEDirectory: getEnv("ACME_DIRECTORY", "https://acme-v02.api.letsencrypt.org/directory"),
		ACMEStaging:   getEnvBool("ACME_STAGING", false),

		Environment: getEnv("ENVIRONMENT", "development"),
	}

	if cfg.JWTSecret == "dev-secret-change-in-production" && cfg.Environment == "production" {
		return nil, fmt.Errorf("JWT_SECRET must be set in production")
	}

	if cfg.MasterKey == "" && cfg.Environment == "production" {
		return nil, fmt.Errorf("MASTER_KEY must be set in production")
	}

	// SMTP max message size: unset (or empty) keeps the 10 MiB default; a set
	// value must be a positive integer byte count. Unlike the lenient getEnvInt
	// pattern, a malformed or non-positive value here is a hard startup error —
	// silently falling back on a size limit the admin explicitly set would make
	// the knob a lie.
	smtpMax, err := getEnvInt64Strict("SMTP_MAX_MESSAGE_SIZE", DefaultSMTPMaxMessageSize)
	if err != nil {
		return nil, err
	}
	if smtpMax <= 0 {
		return nil, fmt.Errorf("SMTP_MAX_MESSAGE_SIZE must be a positive number of bytes (a maximum message size must always exist), got %d", smtpMax)
	}
	cfg.SMTPMaxMessageSize = smtpMax

	return cfg, nil
}

// SMTPMaxMessageSizeWarning returns a non-empty operator warning when the
// configured SMTP max message size exceeds SMTPMaxMessageSizeWarnThreshold.
// The value is still respected — the admin is trusted, just informed of the
// operational implications.
func (c *Config) SMTPMaxMessageSizeWarning() string {
	if c.SMTPMaxMessageSize <= SMTPMaxMessageSizeWarnThreshold {
		return ""
	}
	return fmt.Sprintf(
		"SMTP_MAX_MESSAGE_SIZE is %d bytes (> %d): each active DATA transaction buffers the full message in memory and the delivery pipeline parses it in memory, so expect up to several times that much memory per concurrent delivery and proportionally higher parse cost",
		c.SMTPMaxMessageSize, int64(SMTPMaxMessageSizeWarnThreshold))
}

func (c *Config) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		c.DBHost, c.DBPort, c.DBUser, c.DBPass, c.DBName,
	)
}

func (c *Config) APIAddr() string {
	return fmt.Sprintf("%s:%d", c.APIHost, c.APIPort)
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return fallback
}

// getEnvInt64Strict returns fallback when key is unset or empty, and errors on
// a malformed value instead of silently falling back. Used for settings where
// ignoring an explicitly configured value would be worse than failing startup.
func getEnvInt64Strict(key string, fallback int64) (int64, error) {
	val, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(val) == "" {
		return fallback, nil
	}
	i, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer number of bytes, got %q", key, val)
	}
	return i, nil
}

func getEnvSlice(key string, fallback []string) []string {
	if val, ok := os.LookupEnv(key); ok {
		var result []string
		for _, s := range strings.Split(val, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				result = append(result, s)
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if val, ok := os.LookupEnv(key); ok {
		switch strings.ToLower(val) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no":
			return false
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if val, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return fallback
}
