package config

import (
	"crypto/tls"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/mtls"
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

// DefaultSMTPMinTransferRate is the anti-slowloris average transfer-rate
// floor, in bytes per second, applied to SMTP message-body transfers when
// SMTP_MIN_TRANSFER_RATE is unset: 16 KiB/s. A trickling client that keeps a
// DATA transfer below this average (after the grace period) is dropped.
const DefaultSMTPMinTransferRate = 16 * 1024

// DefaultSMTPTransferGracePeriod is the window at the start of each
// message-body transfer during which the rate floor is not enforced, so slow
// TLS handshakes and slow-start senders are unaffected.
const DefaultSMTPTransferGracePeriod = 60 * time.Second

// DefaultSMTPTransferStallTimeout is how long a message-body transfer may
// deliver zero bytes before the connection is dropped.
const DefaultSMTPTransferStallTimeout = 300 * time.Second

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
	// SMTPMinTransferRate is the anti-slowloris average transfer-rate floor in
	// bytes per second, enforced only while a message body is being
	// transferred (DATA/BDAT) and only after SMTPTransferGracePeriod. Zero
	// disables the rate floor (the stall timeout still applies); negative
	// values are a startup configuration error.
	SMTPMinTransferRate int64
	// SMTPTransferGracePeriod is the window at the start of each message-body
	// transfer during which the rate floor is not enforced. Always positive.
	SMTPTransferGracePeriod time.Duration
	// SMTPTransferStallTimeout is how long a message-body transfer may deliver
	// zero bytes before the connection is dropped. Always positive.
	SMTPTransferStallTimeout time.Duration
	SMTPPortInbound          int
	SMTPPortSubmission       int
	SMTPPortSubmissionTLS    int
	IMAPPort                 int
	IMAPTLSPort              int
	POP3Port                 int
	POP3TLSPort              int
	QueueWorkers             int
	QueuePollInterval        time.Duration
	MTASTSEnforce            bool // enforce recipient MTA-STS policies on outbound delivery (RFC 8461)

	// PROXY protocol
	ProxyProtocolTrustedCIDRs []string

	// CORS
	CORSAllowedOrigins []string

	// ACME (Let's Encrypt)
	ACMEEnabled   bool
	ACMEEmail     string
	ACMEDirectory string // ACME directory URL; defaults to Let's Encrypt production
	ACMEStaging   bool   // use Let's Encrypt staging directory

	// Internal mTLS (gateway → API machine authentication).
	//
	// The protocol gateways call a small set of API endpoints (recipient
	// existence checks + inbound delivery) with no user token — machine-to-
	// machine calls historically protected only by network isolation. When
	// InternalMTLSEnabled is true, the API serves those routes ONLY on a
	// dedicated listener (InternalMTLSPort) that requires a client certificate
	// signed by InternalMTLSCACert, and removes them from the public listener;
	// the gateways present InternalMTLSClientCert/Key to reach them. When false
	// (the default) behavior is unchanged: the routes stay on the public
	// listener, tokenless — no break for existing network-trust deployments.
	//
	// The Config is shared by every binary, so it carries both the server-side
	// material (used by cmd/api) and the client-side material (used by the
	// gateways); each binary validates and loads only the half it needs via the
	// InternalMTLSServerTLS / InternalMTLSClientTLS helpers.
	InternalMTLSEnabled    bool
	InternalMTLSPort       int    // dedicated internal listener port (API side)
	InternalMTLSCACert     string // CA that anchors the internal trust domain (both sides)
	InternalMTLSServerCert string // server cert for the internal listener (API side)
	InternalMTLSServerKey  string // server key for the internal listener (API side)
	InternalMTLSClientCert string // gateway client cert (gateway side)
	InternalMTLSClientKey  string // gateway client key (gateway side)

	// Environment
	Environment string // "development", "production", "test"
}

// DefaultInternalMTLSPort is the internal mTLS listener port used when
// INTERNAL_MTLS_PORT is unset.
const DefaultInternalMTLSPort = 8443

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

		InternalMTLSEnabled:    getEnvBool("INTERNAL_MTLS_ENABLED", false),
		InternalMTLSPort:       getEnvInt("INTERNAL_MTLS_PORT", DefaultInternalMTLSPort),
		InternalMTLSCACert:     getEnv("INTERNAL_MTLS_CA_CERT", ""),
		InternalMTLSServerCert: getEnv("INTERNAL_MTLS_SERVER_CERT", ""),
		InternalMTLSServerKey:  getEnv("INTERNAL_MTLS_SERVER_KEY", ""),
		InternalMTLSClientCert: getEnv("INTERNAL_MTLS_CLIENT_CERT", ""),
		InternalMTLSClientKey:  getEnv("INTERNAL_MTLS_CLIENT_KEY", ""),

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
	smtpMax, err := getEnvInt64Strict("SMTP_MAX_MESSAGE_SIZE", DefaultSMTPMaxMessageSize, "bytes")
	if err != nil {
		return nil, err
	}
	if smtpMax <= 0 {
		return nil, fmt.Errorf("SMTP_MAX_MESSAGE_SIZE must be a positive number of bytes (a maximum message size must always exist), got %d", smtpMax)
	}
	cfg.SMTPMaxMessageSize = smtpMax

	// Anti-slowloris message-transfer policy. Same strictness rationale as the
	// max message size: these are security knobs, so a malformed value is a
	// startup error, never a silent fallback. The rate floor may be explicitly
	// disabled with 0 (the stall timeout still applies); the grace period and
	// stall timeout must always exist.
	minRate, err := getEnvInt64Strict("SMTP_MIN_TRANSFER_RATE", DefaultSMTPMinTransferRate, "bytes per second")
	if err != nil {
		return nil, err
	}
	if minRate < 0 {
		return nil, fmt.Errorf("SMTP_MIN_TRANSFER_RATE must be a non-negative number of bytes per second (0 disables the rate floor), got %d", minRate)
	}
	cfg.SMTPMinTransferRate = minRate

	graceSecs, err := getEnvInt64Strict("SMTP_TRANSFER_GRACE_PERIOD", int64(DefaultSMTPTransferGracePeriod/time.Second), "seconds")
	if err != nil {
		return nil, err
	}
	if graceSecs <= 0 {
		return nil, fmt.Errorf("SMTP_TRANSFER_GRACE_PERIOD must be a positive number of seconds, got %d", graceSecs)
	}
	cfg.SMTPTransferGracePeriod = time.Duration(graceSecs) * time.Second

	stallSecs, err := getEnvInt64Strict("SMTP_TRANSFER_STALL_TIMEOUT", int64(DefaultSMTPTransferStallTimeout/time.Second), "seconds")
	if err != nil {
		return nil, err
	}
	if stallSecs <= 0 {
		return nil, fmt.Errorf("SMTP_TRANSFER_STALL_TIMEOUT must be a positive number of seconds, got %d", stallSecs)
	}
	cfg.SMTPTransferStallTimeout = time.Duration(stallSecs) * time.Second

	// Internal mTLS: the port must be a valid TCP port when the feature is
	// enabled. Per-role certificate-path validation happens at the point of use
	// (InternalMTLSServerTLS / InternalMTLSClientTLS) because the same Config is
	// shared by the API (needs server material) and the gateways (need client
	// material), so Load cannot know which half is required.
	if cfg.InternalMTLSEnabled && (cfg.InternalMTLSPort <= 0 || cfg.InternalMTLSPort > 65535) {
		return nil, fmt.Errorf("INTERNAL_MTLS_PORT must be a valid TCP port (1-65535) when INTERNAL_MTLS_ENABLED is true, got %d", cfg.InternalMTLSPort)
	}

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

// InternalMTLSAddr is the listen address for the dedicated internal mTLS
// listener. It reuses APIHost (the bind interface) with InternalMTLSPort.
func (c *Config) InternalMTLSAddr() string {
	return fmt.Sprintf("%s:%d", c.APIHost, c.InternalMTLSPort)
}

// InternalMTLSServerTLS builds the *tls.Config for the API's internal mTLS
// listener from the configured server-side material. It is the API's validation
// point: it errors if any of the CA cert, server cert, or server key path is
// unset or unreadable, so a half-configured deployment fails closed at startup
// rather than silently serving the internal routes without client-cert
// enforcement.
func (c *Config) InternalMTLSServerTLS() (*tls.Config, error) {
	return mtls.ServerTLSConfig(c.InternalMTLSCACert, c.InternalMTLSServerCert, c.InternalMTLSServerKey)
}

// InternalMTLSClientTLS builds the *tls.Config the gateways use to present
// their client certificate to the API's internal listener. It is the gateway's
// validation point: it errors if any of the CA cert, client cert, or client key
// path is unset or unreadable.
func (c *Config) InternalMTLSClientTLS() (*tls.Config, error) {
	return mtls.ClientTLSConfig(c.InternalMTLSCACert, c.InternalMTLSClientCert, c.InternalMTLSClientKey)
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
// unit names what the integer counts (e.g. "bytes", "seconds") in the error.
func getEnvInt64Strict(key string, fallback int64, unit string) (int64, error) {
	val, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(val) == "" {
		return fallback, nil
	}
	i, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer number of %s, got %q", key, unit, val)
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
