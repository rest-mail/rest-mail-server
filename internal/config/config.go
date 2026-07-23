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

// SMTP anti-abuse tarpit defaults (SMTP_TARPIT_*). Tarpitting is ON by default:
// past DefaultSMTPTarpitSoftLimit rejections on a connection, each further one
// sleeps DefaultSMTPTarpitBase*(errors-soft), capped at DefaultSMTPTarpitMax.
const (
	DefaultSMTPTarpitBase      = 1 * time.Second
	DefaultSMTPTarpitSoftLimit = 2
	DefaultSMTPTarpitMax       = 15 * time.Second
)

// RESTMAIL negative-lookup tarpit defaults (RESTMAIL_TARPIT_*). Small values:
// this is a lightweight defense-in-depth throttle on an already-authenticated
// (DKIM/SPF) path. base doubles as the uniform floor on the first miss.
const (
	DefaultRestmailTarpitBase = 75 * time.Millisecond
	DefaultRestmailTarpitMax  = 2 * time.Second
)

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
	JWTSecret        string
	JWTAccessExpiry  time.Duration
	JWTRefreshExpiry time.Duration

	// Auth rate limiting: a per-client-IP token bucket applied to the
	// auth-sensitive HTTP routes (login/refresh) to blunt brute-force and
	// credential-stuffing. Bounded so legitimate interactive use is unaffected.
	AuthRateLimitEnabled bool
	AuthRateLimitRPS     float64 // sustained requests/sec per client IP
	AuthRateLimitBurst   int     // bucket capacity (max short burst) per client IP

	// Master key for encrypting private keys at rest
	MasterKey string

	// DNS Provider
	DNSProvider string

	// Gateway
	GatewayHostname string
	APIBaseURL      string // public API URL for the gateway (token/credential routes)
	// APIInternalBaseURL is the URL of the API's dedicated internal mTLS
	// listener. Used ONLY for the two tokenless machine routes (recipient check
	// + inbound delivery) when internal mTLS is enabled; every other gateway→API
	// call keeps using APIBaseURL (the public listener). Empty when internal
	// mTLS is off.
	APIInternalBaseURL string
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

	// SMTP anti-abuse tarpit: a per-connection escalating delay imposed at the
	// SMTP rejection points (an invalid inbound RCPT rejected 550, or an AUTH
	// failure), to slow dictionary/enumeration/brute-force sessions without
	// touching legitimate senders. Below the soft limit there is no delay; past
	// it each further rejection sleeps SMTPTarpitBase*(errors-soft), capped at
	// SMTPTarpitMax. The cap is what bounds the self-DoS surface (a sleep holds
	// a connection slot). When SMTPTarpitEnabled is false the feature is off and
	// the base/max/soft-limit values are not range-validated.
	SMTPTarpitEnabled   bool
	SMTPTarpitBase      time.Duration
	SMTPTarpitSoftLimit int
	SMTPTarpitMax       time.Duration

	// RESTMAIL negative-lookup tarpit: a per-source escalating delay on the
	// server-to-server recipient-existence check (GET /restmail/mailboxes) when
	// the mailbox does NOT exist, to throttle recipient enumeration (OSI-1) on
	// that DKIM/SPF-gated path. Positive lookups return promptly. Bounded by
	// RestmailTarpitMax. Off when RestmailTarpitEnabled is false.
	RestmailTarpitEnabled bool
	RestmailTarpitBase    time.Duration
	RestmailTarpitMax     time.Duration

	SMTPPortInbound       int
	SMTPPortSubmission    int
	SMTPPortSubmissionTLS int
	IMAPPort              int
	IMAPTLSPort           int
	POP3Port              int
	POP3TLSPort           int

	// Per-gateway Prometheus metrics endpoints. Each gateway runs as its own
	// process with its own registry, so each exposes /metrics on its own port
	// (default DefaultGatewayMetricsPort). 0 disables the endpoint. Meant to be
	// scraped over the internal network — bound inside the container, not
	// host-published.
	SMTPMetricsPort int
	IMAPMetricsPort int
	POP3MetricsPort int

	QueueWorkers      int
	QueuePollInterval time.Duration
	MTASTSEnforce     bool // enforce recipient MTA-STS policies on outbound delivery (RFC 8461)

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

	// Observability retention/rollup (PR4). These are volume/cost knobs, NOT
	// security controls: they bound how much per-message trace detail is stored
	// and how often aggregate rollups are snapshotted. Aggregate accuracy is
	// unaffected by any of them — rollups snapshot the always-on Prometheus
	// counters (100% accurate), so sampling and pruning never lose aggregate
	// history.
	//
	// TraceRetentionDays  is the per-message trace hot window; a trace's
	//   expires_at is stamped created_at + this many days and the pruner deletes
	//   past it. Always positive.
	// TraceSampleRate     is the probability a happy-path (delivered/queued) trace
	//   is persisted; non-continue outcomes (rejected/quarantined/discarded/
	//   deferred) are always kept 100%. Must be in [0.0, 1.0]. Small deployments
	//   set 1.0 to keep every trace.
	// TraceMaxRows        is the hard row-count backstop on message_traces; when
	//   exceeded the pruner deletes the oldest rows beyond it. 0 disables the cap.
	// RollupInterval      is the rollup worker's snapshot cadence (also the rollup
	//   bucket width). Always positive.
	TraceRetentionDays int
	TraceSampleRate    float64
	TraceMaxRows       int64
	RollupInterval     time.Duration

	// Environment
	Environment string // "development", "production", "test"
}

// DefaultTraceRetentionDays is the per-message trace hot window when
// TRACE_RETENTION_DAYS is unset.
const DefaultTraceRetentionDays = 7

// DefaultTraceSampleRate is the happy-path trace sampling probability when
// TRACE_SAMPLE_RATE is unset.
const DefaultTraceSampleRate = 0.1

// DefaultTraceMaxRows is the message_traces hard row-count backstop when
// TRACE_MAX_ROWS is unset. Sized for the 100k–1M/day target: at ~2 GB/7d the
// row count sits well under this ceiling, so the backstop only engages if
// sampling is disabled or volume spikes far above target.
const DefaultTraceMaxRows = 2_000_000

// DefaultRollupInterval is the rollup worker snapshot cadence when
// ROLLUP_INTERVAL is unset.
const DefaultRollupInterval = 5 * time.Minute

// DefaultAuthRateLimitRPS / DefaultAuthRateLimitBurst are the per-client-IP
// auth-route throttle defaults when AUTH_RATE_LIMIT_RPS / AUTH_RATE_LIMIT_BURST
// are unset: 1 sustained request/sec with a burst of 15 gives ordinary
// interactive login/refresh ample headroom while capping automated
// brute-force/credential-stuffing to a trickle.
const (
	DefaultAuthRateLimitRPS   = 1.0
	DefaultAuthRateLimitBurst = 15
)

// TraceRetention returns the per-message trace retention window as a Duration
// (TraceRetentionDays × 24h) — what the recorder stamps as each trace's
// expires_at horizon.
func (c *Config) TraceRetention() time.Duration {
	return time.Duration(c.TraceRetentionDays) * 24 * time.Hour
}

// DefaultInternalMTLSPort is the internal mTLS listener port used when
// INTERNAL_MTLS_PORT is unset.
const DefaultInternalMTLSPort = 8443

// DefaultGatewayMetricsPort is the port each protocol gateway exposes its
// Prometheus /metrics endpoint on when <GATEWAY>_METRICS_PORT is unset. It
// matches the target the shipped monitoring/prometheus.yml scrapes.
const DefaultGatewayMetricsPort = 9090

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

		AuthRateLimitEnabled: getEnvBool("AUTH_RATE_LIMIT_ENABLED", true),
		AuthRateLimitBurst:   getEnvInt("AUTH_RATE_LIMIT_BURST", DefaultAuthRateLimitBurst),

		MasterKey: getEnv("MASTER_KEY", ""),

		DNSProvider: getEnv("DNS_PROVIDER", "dnsmasq"),

		GatewayHostname:       getEnv("GATEWAY_HOSTNAME", "localhost"),
		APIBaseURL:            getEnv("API_BASE_URL", "http://localhost:8080"),
		APIInternalBaseURL:    getEnv("API_INTERNAL_BASE_URL", ""),
		SMTPPortInbound:       getEnvInt("SMTP_PORT_INBOUND", 25),
		SMTPPortSubmission:    getEnvInt("SMTP_PORT_SUBMISSION", 587),
		SMTPPortSubmissionTLS: getEnvInt("SMTP_PORT_SUBMISSION_TLS", 465),
		IMAPPort:              getEnvInt("IMAP_PORT", 143),
		IMAPTLSPort:           getEnvInt("IMAP_TLS_PORT", 993),
		POP3Port:              getEnvInt("POP3_PORT", 110),
		POP3TLSPort:           getEnvInt("POP3_TLS_PORT", 995),
		SMTPMetricsPort:       getEnvInt("SMTP_METRICS_PORT", DefaultGatewayMetricsPort),
		IMAPMetricsPort:       getEnvInt("IMAP_METRICS_PORT", DefaultGatewayMetricsPort),
		POP3MetricsPort:       getEnvInt("POP3_METRICS_PORT", DefaultGatewayMetricsPort),
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

		RollupInterval: getEnvDuration("ROLLUP_INTERVAL", DefaultRollupInterval),

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

	// SMTP anti-abuse tarpit. Same strictness rationale as the transfer knobs: a
	// malformed value is a startup error, never a silent fallback. Range checks
	// apply only when enabled, so a deployment that turns the tarpit off need not
	// tune the delays (0/disabled is honored).
	cfg.SMTPTarpitEnabled = getEnvBool("SMTP_TARPIT_ENABLED", true)
	tarpitBase, err := getEnvDurationStrict("SMTP_TARPIT_BASE", DefaultSMTPTarpitBase)
	if err != nil {
		return nil, err
	}
	tarpitMax, err := getEnvDurationStrict("SMTP_TARPIT_MAX", DefaultSMTPTarpitMax)
	if err != nil {
		return nil, err
	}
	tarpitSoft, err := getEnvInt64Strict("SMTP_TARPIT_SOFT_LIMIT", DefaultSMTPTarpitSoftLimit, "errors")
	if err != nil {
		return nil, err
	}
	if cfg.SMTPTarpitEnabled {
		if tarpitBase <= 0 {
			return nil, fmt.Errorf("SMTP_TARPIT_BASE must be a positive duration when SMTP_TARPIT_ENABLED is true, got %v", tarpitBase)
		}
		if tarpitMax <= 0 {
			return nil, fmt.Errorf("SMTP_TARPIT_MAX must be a positive duration when SMTP_TARPIT_ENABLED is true, got %v", tarpitMax)
		}
		if tarpitMax < tarpitBase {
			return nil, fmt.Errorf("SMTP_TARPIT_MAX (%v) must be >= SMTP_TARPIT_BASE (%v)", tarpitMax, tarpitBase)
		}
		if tarpitSoft < 0 {
			return nil, fmt.Errorf("SMTP_TARPIT_SOFT_LIMIT must be non-negative, got %d", tarpitSoft)
		}
	}
	cfg.SMTPTarpitBase = tarpitBase
	cfg.SMTPTarpitMax = tarpitMax
	cfg.SMTPTarpitSoftLimit = int(tarpitSoft)

	// RESTMAIL negative-lookup tarpit. Same strict parse + enabled-gated range
	// validation as the SMTP tarpit above.
	cfg.RestmailTarpitEnabled = getEnvBool("RESTMAIL_TARPIT_ENABLED", true)
	restTarpitBase, err := getEnvDurationStrict("RESTMAIL_TARPIT_BASE", DefaultRestmailTarpitBase)
	if err != nil {
		return nil, err
	}
	restTarpitMax, err := getEnvDurationStrict("RESTMAIL_TARPIT_MAX", DefaultRestmailTarpitMax)
	if err != nil {
		return nil, err
	}
	if cfg.RestmailTarpitEnabled {
		if restTarpitBase <= 0 {
			return nil, fmt.Errorf("RESTMAIL_TARPIT_BASE must be a positive duration when RESTMAIL_TARPIT_ENABLED is true, got %v", restTarpitBase)
		}
		if restTarpitMax <= 0 {
			return nil, fmt.Errorf("RESTMAIL_TARPIT_MAX must be a positive duration when RESTMAIL_TARPIT_ENABLED is true, got %v", restTarpitMax)
		}
		if restTarpitMax < restTarpitBase {
			return nil, fmt.Errorf("RESTMAIL_TARPIT_MAX (%v) must be >= RESTMAIL_TARPIT_BASE (%v)", restTarpitMax, restTarpitBase)
		}
	}
	cfg.RestmailTarpitBase = restTarpitBase
	cfg.RestmailTarpitMax = restTarpitMax

	// Internal mTLS: the port must be a valid TCP port when the feature is
	// enabled. Per-role certificate-path validation happens at the point of use
	// (InternalMTLSServerTLS / InternalMTLSClientTLS) because the same Config is
	// shared by the API (needs server material) and the gateways (need client
	// material), so Load cannot know which half is required.
	if cfg.InternalMTLSEnabled && (cfg.InternalMTLSPort <= 0 || cfg.InternalMTLSPort > 65535) {
		return nil, fmt.Errorf("INTERNAL_MTLS_PORT must be a valid TCP port (1-65535) when INTERNAL_MTLS_ENABLED is true, got %d", cfg.InternalMTLSPort)
	}

	// Gateway metrics ports: 0 disables the endpoint; any other value must be a
	// valid TCP port. A malformed value is a hard startup error rather than a
	// silent fallback, matching the strictness of the other port knobs.
	for _, mp := range []struct {
		key  string
		port int
	}{
		{"SMTP_METRICS_PORT", cfg.SMTPMetricsPort},
		{"IMAP_METRICS_PORT", cfg.IMAPMetricsPort},
		{"POP3_METRICS_PORT", cfg.POP3MetricsPort},
	} {
		if mp.port < 0 || mp.port > 65535 {
			return nil, fmt.Errorf("%s must be a valid TCP port (0 to disable, 1-65535 to enable), got %d", mp.key, mp.port)
		}
	}

	// Observability retention/rollup knobs (PR4). These are volume/cost dials,
	// but a malformed value is still a hard startup error rather than a silent
	// fallback: an admin who set a value meant it, and quietly ignoring it would
	// make the knob a lie (same rationale as the SMTP size/rate strictness).
	retentionDays, err := getEnvInt64Strict("TRACE_RETENTION_DAYS", DefaultTraceRetentionDays, "days")
	if err != nil {
		return nil, err
	}
	if retentionDays <= 0 {
		return nil, fmt.Errorf("TRACE_RETENTION_DAYS must be a positive number of days, got %d", retentionDays)
	}
	cfg.TraceRetentionDays = int(retentionDays)

	sampleRate, err := getEnvFloatStrict("TRACE_SAMPLE_RATE", DefaultTraceSampleRate)
	if err != nil {
		return nil, err
	}
	if sampleRate < 0 || sampleRate > 1 {
		return nil, fmt.Errorf("TRACE_SAMPLE_RATE must be between 0.0 and 1.0, got %v", sampleRate)
	}
	cfg.TraceSampleRate = sampleRate

	maxRows, err := getEnvInt64Strict("TRACE_MAX_ROWS", DefaultTraceMaxRows, "rows")
	if err != nil {
		return nil, err
	}
	if maxRows < 0 {
		return nil, fmt.Errorf("TRACE_MAX_ROWS must be a non-negative number of rows (0 disables the backstop), got %d", maxRows)
	}
	cfg.TraceMaxRows = maxRows

	if cfg.RollupInterval <= 0 {
		return nil, fmt.Errorf("ROLLUP_INTERVAL must be a positive duration, got %v", cfg.RollupInterval)
	}

	// Auth rate limit: strict parse so a malformed RPS is a hard startup error
	// rather than a silently-ignored knob. Positivity is enforced only when the
	// limiter is enabled, so a disabled deployment need not tune the values.
	authRPS, err := getEnvFloatStrict("AUTH_RATE_LIMIT_RPS", DefaultAuthRateLimitRPS)
	if err != nil {
		return nil, err
	}
	if cfg.AuthRateLimitEnabled {
		if authRPS <= 0 {
			return nil, fmt.Errorf("AUTH_RATE_LIMIT_RPS must be positive, got %v", authRPS)
		}
		if cfg.AuthRateLimitBurst <= 0 {
			return nil, fmt.Errorf("AUTH_RATE_LIMIT_BURST must be positive, got %d", cfg.AuthRateLimitBurst)
		}
	}
	cfg.AuthRateLimitRPS = authRPS

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

// getEnvFloatStrict returns fallback when key is unset or empty, and errors on a
// malformed value instead of silently falling back — used for settings (e.g. a
// sampling probability) where ignoring an explicitly configured value would be
// worse than failing startup. Range validation is the caller's responsibility.
func getEnvFloatStrict(key string, fallback float64) (float64, error) {
	val, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(val) == "" {
		return fallback, nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a decimal number, got %q", key, val)
	}
	return f, nil
}

// getEnvDurationStrict returns fallback when key is unset or empty, and errors
// on a malformed value instead of silently falling back — used for security
// knobs (e.g. the tarpit delays) where ignoring an explicitly configured value
// would be worse than failing startup. Accepts any Go duration string ("1s",
// "500ms", "15s"). Range validation is the caller's responsibility.
func getEnvDurationStrict(key string, fallback time.Duration) (time.Duration, error) {
	val, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(val) == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(val))
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration (e.g. \"1s\", \"500ms\"), got %q", key, val)
	}
	return d, nil
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
