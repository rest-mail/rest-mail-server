package config

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/mtls"
	"github.com/restmail/restmail/internal/netallow"
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

	// Pipeline/filter test-endpoint rate limiting (M-14, CWE-770): a per-client-IP
	// token bucket applied to the admin "try this pipeline/filter" endpoints
	// (pipelines/test, pipelines/test-filter, custom-filters/{id}/test). Each runs
	// a message through the pipeline engine, so an authenticated admin could
	// otherwise hammer them without throttle. Bounded so ordinary interactive
	// preview use is unaffected.
	PipelineTestRateLimitEnabled bool
	PipelineTestRateLimitRPS     float64 // sustained requests/sec per client IP
	PipelineTestRateLimitBurst   int     // bucket capacity (max short burst) per client IP

	// Authenticated-API request-body cap (#184): the maximum request body, in
	// bytes, accepted on the authenticated API surface (send, drafts, contacts
	// import, sieve PUT, pipeline create/test, …). Enforced with
	// http.MaxBytesReader plus a Content-Length fast-fail so an unbounded JSON
	// upload cannot exhaust memory. 0 disables the cap; a negative value is a
	// startup configuration error. The default scales with SMTPMaxMessageSize so
	// a legitimate max-size compose is never rejected.
	APIMaxBodyBytes int64

	// Webmail/API send-path limits (#184): a per-message recipient cap and a
	// per-account send rate limit on POST /messages/send (and the draft-send /
	// forward paths that delegate to it). They mirror the SMTP submission caps
	// (#171) so a single compromised webmail credential cannot fan out unlimited
	// bulk mail — one queue row per recipient, each a full copy of the message.
	// APIMaxRecipientsPerMessage <= 0 disables the recipient cap; a per-tier send
	// rate <= 0 disables that tier.
	APIMaxRecipientsPerMessage int
	APISendRateLimitPerMinute  int
	APISendRateLimitPerHour    int

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

	// OutboundAllowPrivateDestinations opts outbound delivery in to dialing
	// non-public addresses (loopback/link-local/private/metadata) for the MX host
	// or the RESTMAIL endpoint (#167). It exists only for a dev/testbed that
	// delivers between containers on a private bridge network. Default false so
	// production denies these SSRF-class destinations. It is also implied by
	// ENVIRONMENT=development (see cmd/smtp-gateway/main.go), mirroring how
	// QUEUE_TLS_INSECURE is auto-enabled there.
	OutboundAllowPrivateDestinations bool

	// Trusted front-proxy networks. Governs the PROXY protocol on the gateway
	// listeners AND the HTTP API's trusted-proxy-aware client-IP derivation: an
	// X-Forwarded-For header is honored only when the direct TCP peer is one of
	// these CIDRs. Empty by default — no proxy is trusted, so the genuine socket
	// peer is always used and a client cannot spoof its source IP.
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

	// ── OSI-25: bounce/DSN anti-mailbomb (appended block — keep contiguous) ──
	// BounceDSNMaxPerRecipient caps how many bounce (DSN) messages the queue
	// worker will deliver into any single recipient mailbox within
	// BounceDSNRateWindow. <= 0 disables the cap. These bound a spoofed-sender
	// mail-bombing amplification without dropping legitimate low-volume bounces.
	BounceDSNMaxPerRecipient int
	BounceDSNRateWindow      time.Duration
	// ════════════════════════════════════════════════════════════════════
	// Auth-hardening additions (appended additively — keep contiguous to ease
	// rebasing alongside other in-flight config.go work).
	// ════════════════════════════════════════════════════════════════════
	//
	// SecurityHeadersEnabled toggles the API security-headers middleware
	// (HSTS / X-Content-Type-Options / X-Frame-Options / Referrer-Policy /
	// a JSON-API CSP) — OSI-11. Default on. A reverse proxy is the other natural
	// place for these; setting them at the app too keeps responses secure even
	// behind an absent/misconfigured proxy, and the headers are idempotent.
	SecurityHeadersEnabled bool
	// HSTSMaxAgeSeconds is the Strict-Transport-Security max-age in seconds.
	// Default DefaultHSTSMaxAgeSeconds (2 years). 0 omits the HSTS header (e.g.
	// plain-HTTP local dev where pinning HTTPS would be wrong).
	HSTSMaxAgeSeconds int
	// ════════════════════════════════════════════════════════════════════
	// OSI-19: optional TOTP two-factor auth (appended additively — keep
	// contiguous to ease rebasing alongside other in-flight config.go work).
	// ════════════════════════════════════════════════════════════════════
	//
	// TOTP2FAEnabled permits accounts to ENROLL in TOTP 2FA (RFC 6238). Default
	// true — allow-but-not-required: any account may opt in, none is forced, and
	// accounts that never enrol behave exactly as before. Setting it false blocks
	// new enrollment/confirmation but deliberately does NOT disable verification
	// for already-active enrollments: turning the feature off must not silently
	// downgrade the security of users who already turned it on. 2FA enrollment
	// additionally requires MASTER_KEY (the secret is stored encrypted at rest).
	TOTP2FAEnabled bool

	// ════════════════════════════════════════════════════════════════════
	// PR6 (G7): internal-mTLS provisioning seam — appended additively; keep
	// contiguous to ease rebasing alongside other in-flight config.go work.
	// ════════════════════════════════════════════════════════════════════
	//
	// TrustedCACertPath is a PEM file of one or more extra CA certificates the
	// API adds to its outbound HTTP client trust store (on top of the system
	// roots) at startup — so it trusts TLS peers (e.g. MTA-STS policy fetches)
	// issued by an operator/testbed CA. On the testbed this is the shared testbed
	// root CA. It de-hardcodes cmd/api's former literal "/certs/ca.crt": the
	// DEFAULT is that exact path, so the testbed is byte-for-byte unchanged, but a
	// deployer can point it elsewhere, and an EMPTY value skips the extra trust
	// (system roots only). A missing/unreadable file is not an error — the API
	// falls back to the system roots (unchanged legacy behavior). This is NOT the
	// internal-mTLS CA (that is INTERNAL_MTLS_CA_CERT); it is the general outbound
	// trust anchor.
	TrustedCACertPath string

	// ════════════════════════════════════════════════════════════════════
	// TLS-RPT ingestion rate limit (issue #183) — appended additively; keep
	// contiguous to ease rebasing alongside other in-flight config.go work.
	// ════════════════════════════════════════════════════════════════════
	//
	// The unauthenticated TLS-RPT ingestion endpoint (POST /.well-known/smtp-tlsrpt)
	// is throttled per client IP so it cannot be used to flood storage with reports.
	// Same dependency-free per-IP token bucket as the auth/pipeline-test limiters.
	TLSRPTRateLimitEnabled bool
	TLSRPTRateLimitRPS     float64 // sustained requests/sec per client IP
	TLSRPTRateLimitBurst   int     // bucket capacity (max short burst) per client IP
}

// DefaultHSTSMaxAgeSeconds is the Strict-Transport-Security max-age used when
// HSTS_MAX_AGE_SECONDS is unset: two years, the common HSTS-preload-eligible
// value.
const DefaultHSTSMaxAgeSeconds = 63072000

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

// DefaultPipelineTestRateLimitRPS / DefaultPipelineTestRateLimitBurst are the
// per-client-IP throttle defaults for the admin pipeline/filter test endpoints
// (M-14) when PIPELINE_TEST_RATE_LIMIT_RPS / PIPELINE_TEST_RATE_LIMIT_BURST are
// unset: 2 sustained requests/sec with a burst of 10 leaves an admin ample
// headroom to iterate interactively while capping an automated hammer to a
// trickle.
const (
	DefaultPipelineTestRateLimitRPS   = 2.0
	DefaultPipelineTestRateLimitBurst = 10
)

// API send-path limit defaults (#184) for the webmail/API send surface. They
// mirror the shared SMTP submission caps (ratelimit.DefaultPerMinute /
// ratelimit.DefaultPerHour) so submission (587/465) and the webmail/API send
// path cap a sender's outbound volume the same way: 100 recipients per message,
// 20 messages/minute and 100 messages/hour per account. Overridden by
// API_MAX_RECIPIENTS_PER_MESSAGE / API_SEND_RATE_LIMIT_PER_MINUTE /
// API_SEND_RATE_LIMIT_PER_HOUR.
const (
	DefaultAPIMaxRecipientsPerMessage = 100
	DefaultAPISendRateLimitPerMinute  = 20
	DefaultAPISendRateLimitPerHour    = 100
)

// DefaultTLSRPTRateLimitRPS / DefaultTLSRPTRateLimitBurst are the per-client-IP
// throttle defaults for the unauthenticated TLS-RPT ingestion endpoint (issue
// #183) when TLSRPT_RATE_LIMIT_RPS / TLSRPT_RATE_LIMIT_BURST are unset. TLS-RPT
// reports arrive at most a few times a day per sender, so 1 sustained request/sec
// with a burst of 10 leaves legitimate reporters ample headroom while capping an
// automated flood to a trickle.
const (
	DefaultTLSRPTRateLimitRPS   = 1.0
	DefaultTLSRPTRateLimitBurst = 10
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

		PipelineTestRateLimitEnabled: getEnvBool("PIPELINE_TEST_RATE_LIMIT_ENABLED", true),
		PipelineTestRateLimitBurst:   getEnvInt("PIPELINE_TEST_RATE_LIMIT_BURST", DefaultPipelineTestRateLimitBurst),

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

		OutboundAllowPrivateDestinations: getEnvBool("OUTBOUND_ALLOW_PRIVATE_DESTINATIONS", false),

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

	// Authenticated-API request-body cap (#184). Same strictness rationale as the
	// SMTP max message size: a malformed value is a hard startup error, never a
	// silent fallback. 0 explicitly disables the cap. The default scales with the
	// configured max message size (2× plus 1 MiB scaffolding headroom, mirroring
	// InternalDeliveryBodyLimit) so a legitimate max-size compose is accepted while
	// an unbounded upload cannot buffer without limit.
	defaultAPIBody := smtpMax*2 + 1*1024*1024
	apiBody, err := getEnvInt64Strict("API_MAX_BODY_BYTES", defaultAPIBody, "bytes")
	if err != nil {
		return nil, err
	}
	if apiBody < 0 {
		return nil, fmt.Errorf("API_MAX_BODY_BYTES must be a non-negative number of bytes (0 disables the cap), got %d", apiBody)
	}
	cfg.APIMaxBodyBytes = apiBody

	// Webmail/API send-path limits (#184): recipient cap + per-account send rate.
	// These are lenient getEnvInt knobs (a per-tier value of 0 simply disables that
	// tier) rather than hard startup errors — they are volume caps, not a
	// must-always-exist security invariant like the max message size.
	cfg.APIMaxRecipientsPerMessage = getEnvInt("API_MAX_RECIPIENTS_PER_MESSAGE", DefaultAPIMaxRecipientsPerMessage)
	cfg.APISendRateLimitPerMinute = getEnvInt("API_SEND_RATE_LIMIT_PER_MINUTE", DefaultAPISendRateLimitPerMinute)
	cfg.APISendRateLimitPerHour = getEnvInt("API_SEND_RATE_LIMIT_PER_HOUR", DefaultAPISendRateLimitPerHour)

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

	// Pipeline/filter test-endpoint rate limit (M-14): same strict-parse and
	// enabled-only positivity rules as the auth limiter above.
	pipelineTestRPS, err := getEnvFloatStrict("PIPELINE_TEST_RATE_LIMIT_RPS", DefaultPipelineTestRateLimitRPS)
	if err != nil {
		return nil, err
	}
	if cfg.PipelineTestRateLimitEnabled {
		if pipelineTestRPS <= 0 {
			return nil, fmt.Errorf("PIPELINE_TEST_RATE_LIMIT_RPS must be positive, got %v", pipelineTestRPS)
		}
		if cfg.PipelineTestRateLimitBurst <= 0 {
			return nil, fmt.Errorf("PIPELINE_TEST_RATE_LIMIT_BURST must be positive, got %d", cfg.PipelineTestRateLimitBurst)
		}
	}
	cfg.PipelineTestRateLimitRPS = pipelineTestRPS

	// TLS-RPT ingestion rate limit (issue #183): same strict-parse + enabled-only
	// positivity rules as the auth/pipeline-test limiters above.
	cfg.TLSRPTRateLimitEnabled = getEnvBool("TLSRPT_RATE_LIMIT_ENABLED", true)
	cfg.TLSRPTRateLimitBurst = getEnvInt("TLSRPT_RATE_LIMIT_BURST", DefaultTLSRPTRateLimitBurst)
	tlsRPTRPS, err := getEnvFloatStrict("TLSRPT_RATE_LIMIT_RPS", DefaultTLSRPTRateLimitRPS)
	if err != nil {
		return nil, err
	}
	if cfg.TLSRPTRateLimitEnabled {
		if tlsRPTRPS <= 0 {
			return nil, fmt.Errorf("TLSRPT_RATE_LIMIT_RPS must be positive, got %v", tlsRPTRPS)
		}
		if cfg.TLSRPTRateLimitBurst <= 0 {
			return nil, fmt.Errorf("TLSRPT_RATE_LIMIT_BURST must be positive, got %d", cfg.TLSRPTRateLimitBurst)
		}
	}
	cfg.TLSRPTRateLimitRPS = tlsRPTRPS

	// ── OSI-25: bounce/DSN anti-mailbomb (appended block — keep contiguous) ──
	cfg.BounceDSNMaxPerRecipient = getEnvInt("BOUNCE_DSN_MAX_PER_RECIPIENT", 20)
	cfg.BounceDSNRateWindow = getEnvDuration("BOUNCE_DSN_RATE_WINDOW", time.Hour)
	// ════════════════════════════════════════════════════════════════════
	// Auth-hardening additions (OSI-11) — appended additively; keep contiguous.
	// ════════════════════════════════════════════════════════════════════
	cfg.SecurityHeadersEnabled = getEnvBool("SECURITY_HEADERS_ENABLED", true)
	hstsMaxAge, err := getEnvInt64Strict("HSTS_MAX_AGE_SECONDS", DefaultHSTSMaxAgeSeconds, "seconds")
	if err != nil {
		return nil, err
	}
	if hstsMaxAge < 0 {
		return nil, fmt.Errorf("HSTS_MAX_AGE_SECONDS must be a non-negative number of seconds (0 disables the HSTS header), got %d", hstsMaxAge)
	}
	cfg.HSTSMaxAgeSeconds = int(hstsMaxAge)

	// ── OSI-19: optional TOTP 2FA (appended block — keep contiguous) ──
	cfg.TOTP2FAEnabled = getEnvBool("TOTP_2FA_ENABLED", true)

	// ── PR6 (G7): internal-mTLS provisioning seam (appended block — keep contiguous) ──
	// De-hardcodes cmd/api's former literal "/certs/ca.crt"; the default is that
	// exact path so the testbed's rendered/provisioned setup is unchanged.
	cfg.TrustedCACertPath = getEnv("TRUSTED_CA_CERT", "/certs/ca.crt")

	// ── OSI-4: boot-time security config validation (single append-only call).
	// Production (ENVIRONMENT=production) refuses to boot on an insecure value;
	// development/test only warn. See the OSI-4 block at the end of this file. ──
	if err := cfg.validateSecurityConfig(); err != nil {
		return nil, err
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
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.DBHost, c.DBPort, c.DBUser, c.DBPass, c.DBName, c.DBSSLMode(),
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

// ══════════════════════════════════════════════════════════════════════════
// BEGIN inbound-path security hardening (OSI-3 / OSI-13 / OSI-18)
//
// One contiguous, append-only block so it merges cleanly alongside concurrent
// config.go edits. It deliberately touches neither the Config struct, Load(),
// nor the import list — every setting is read lazily through a *Config accessor
// using the accessors already defined above. Malformed values fall back to the
// SECURE default (fail-closed) rather than hard-failing startup, which is the
// safe posture for each of these knobs (deny external redirect / defer on
// filter error / require delivery auth).
// ══════════════════════════════════════════════════════════════════════════

// DefaultPipelineFilterErrorAction is the pipeline's action when a filter fails
// to instantiate or execute (OSI-18). "defer" is fail-closed: the message is
// temp-failed and the sender retries, so a renamed/broken security filter can
// never silently pass mail. Override with PIPELINE_FILTER_ERROR_ACTION.
const DefaultPipelineFilterErrorAction = "defer"

// DefaultPipelineFilterTimeoutSeconds is the per-filter execution backstop: a
// single filter may run this long before the engine abandons it and routes the
// step through the fail-closed policy above. It is a safety net against a hung
// or deadlocked filter wedging delivery, deliberately set above every built-in
// filter's own I/O timeout (the slowest being clamav/rspamd at 30s) so it never
// preempts a legitimately slow scan. Override with PIPELINE_FILTER_TIMEOUT_SECONDS.
const DefaultPipelineFilterTimeoutSeconds = 60

// RestmailDeliverAuthSettings configures server-to-server authentication on the
// RESTMAIL inbound-delivery endpoint POST /restmail/messages (OSI-3). Without
// it any host can inject a spoofed-From message into a local mailbox
// (BEC/CEO-fraud). See RestmailDeliverAuth for the field semantics.
type RestmailDeliverAuthSettings struct {
	// Enabled turns the delivery-auth gate on (default true). When false the
	// endpoint reverts to legacy behavior (accept any well-formed delivery).
	Enabled bool
	// Strict requires EVERY delivery to originate from a trusted peer OR carry a
	// DKIM signature that verifies and aligns with the From domain. Default false
	// (open federation): only deliveries claiming a LOCALLY-hosted From domain
	// must authenticate — which is exactly the internal-spoofing / BEC vector —
	// while ordinary external inbound is untouched.
	Strict bool
	// TrustedCIDRs are source networks whose deliveries bypass the DKIM check
	// (known federated peers, or a front proxy on the internal network). The
	// client IP is taken from RemoteAddr as resolved by the RealIP middleware.
	TrustedCIDRs []string
}

// RestmailDeliverAuth returns the OSI-3 delivery-authentication settings.
// RESTMAIL_DELIVER_AUTH_ENABLED (default true), RESTMAIL_DELIVER_STRICT
// (default false), RESTMAIL_DELIVER_TRUSTED_CIDRS (comma-separated, default
// none).
func (c *Config) RestmailDeliverAuth() RestmailDeliverAuthSettings {
	return RestmailDeliverAuthSettings{
		Enabled:      getEnvBool("RESTMAIL_DELIVER_AUTH_ENABLED", true),
		Strict:       getEnvBool("RESTMAIL_DELIVER_STRICT", false),
		TrustedCIDRs: getEnvSlice("RESTMAIL_DELIVER_TRUSTED_CIDRS", nil),
	}
}

// SieveRedirectSettings configures where a Sieve `redirect` action may send
// mail (OSI-13). Redirect to an arbitrary external domain is a mail-exfiltration
// vector, so the secure default denies it.
type SieveRedirectSettings struct {
	// AllowExternal, when true, permits redirect to ANY domain (legacy behavior);
	// the redirect is still logged. Default false: a redirect target must be the
	// recipient's own domain or an explicitly allowlisted domain.
	AllowExternal bool
	// AllowedDomains are external domains explicitly permitted as redirect targets
	// even when AllowExternal is false.
	AllowedDomains []string
}

// SieveRedirect returns the OSI-13 sieve-redirect allowlist settings.
// SIEVE_REDIRECT_ALLOW_EXTERNAL (default false — deny external redirects) and
// SIEVE_REDIRECT_ALLOWED_DOMAINS (comma-separated, default none).
func (c *Config) SieveRedirect() SieveRedirectSettings {
	return SieveRedirectSettings{
		AllowExternal:  getEnvBool("SIEVE_REDIRECT_ALLOW_EXTERNAL", false),
		AllowedDomains: getEnvSlice("SIEVE_REDIRECT_ALLOWED_DOMAINS", nil),
	}
}

// PipelineFilterErrorAction returns the OSI-18 fail-closed action applied when a
// pipeline filter cannot be instantiated or errors during execution. One of
// "continue" (legacy fail-open), "defer" (default, fail-closed temp-fail), or
// "reject". A malformed value falls back to the secure default rather than
// failing startup — an unparseable knob must never weaken the fail-closed
// posture. Set with PIPELINE_FILTER_ERROR_ACTION.
func (c *Config) PipelineFilterErrorAction() string {
	switch strings.ToLower(strings.TrimSpace(getEnv("PIPELINE_FILTER_ERROR_ACTION", DefaultPipelineFilterErrorAction))) {
	case "continue":
		return "continue"
	case "reject":
		return "reject"
	case "defer":
		return "defer"
	default:
		return DefaultPipelineFilterErrorAction
	}
}

// PipelineFilterTimeout returns the per-filter execution backstop applied by the
// pipeline engine. A single filter that runs longer than this is abandoned and
// its step routed through PipelineFilterErrorAction (fail-closed by default), so
// a hung filter cannot wedge delivery. Set with PIPELINE_FILTER_TIMEOUT_SECONDS
// (a whole number of seconds). A malformed or non-positive value falls back to
// the secure default rather than disabling the backstop — an unparseable knob
// must never remove the protection.
func (c *Config) PipelineFilterTimeout() time.Duration {
	secs := getEnvInt("PIPELINE_FILTER_TIMEOUT_SECONDS", DefaultPipelineFilterTimeoutSeconds)
	if secs <= 0 {
		secs = DefaultPipelineFilterTimeoutSeconds
	}
	return time.Duration(secs) * time.Second
}

// END inbound-path security hardening
// ══════════════════════════════════════════════════════════════════════════

// ══════════════════════════════════════════════════════════════════════════
// BEGIN scanner-verdict hardening (OSI-15)
//
// One contiguous, append-only block so it merges cleanly alongside concurrent
// config.go edits. The setting is read lazily through a *Config accessor using
// the getEnv helper above; the Config struct and Load() are deliberately
// untouched. An unset secret disables only the extra verdict-signature check —
// never the fail-closed fallback baked into the scanner adapters.
// ══════════════════════════════════════════════════════════════════════════

// ScannerHMACSecret returns the shared secret used to authenticate external
// content-scanner (rspamd / ClamAV) verdicts (OSI-15). Verdicts otherwise travel
// over plain HTTP with no integrity protection, so a MITM or rogue scanner could
// downgrade a "reject"/"infected" verdict to "clean". When set, the scanner
// filters verify an HMAC-SHA256 signature (X-Scanner-Signature) over every
// verdict body and fail closed on a missing/forged signature.
//
// Empty (the default) is safe because NO external scanner is wired into the
// default pipeline: the rspamd/clamav filters are optional and run only when an
// operator explicitly adds them to a pipeline. When a scanner IS deployed, set
// SCANNER_HMAC_SECRET on both the server and the scanner sidecar.
func (c *Config) ScannerHMACSecret() string {
	return getEnv("SCANNER_HMAC_SECRET", "")
}

// END scanner-verdict hardening
// ══════════════════════════════════════════════════════════════════════════

// ══════════════════════════════════════════════════════════════════════════
// BEGIN metrics network-gate (OSI-12)
//
// One contiguous, append-only block so it merges cleanly alongside concurrent
// config.go edits. The setting is read lazily through a *Config accessor using
// the getEnvSlice helper above; the Config struct and Load() are untouched. An
// unset allowlist defaults to internal-only CIDRs — never open to the public.
// ══════════════════════════════════════════════════════════════════════════

// MetricsAllowedCIDRs returns the source-network allowlist for the Prometheus
// /metrics endpoints (OSI-12), applied to both the API route and the gateway
// metrics servers. Prometheus scraping is a network-level control (JWT-gating
// would break scrapers), so the endpoint is restricted to trusted CIDRs and a
// non-allowlisted peer is denied. Unset defaults to loopback + RFC1918
// (netallow.DefaultInternalCIDRs) — exactly how the in-cluster Prometheus reaches
// the endpoint — so the default keeps scraping working while closing the
// endpoint to the public internet. Override with METRICS_ALLOWED_CIDRS
// (comma-separated).
//
// The trusted-proxy set for safe real-client-IP derivation is the existing
// PROXY_PROTOCOL_TRUSTED_CIDRS (ProxyProtocolTrustedCIDRs): a forwarded header is
// honored only when the direct TCP peer is one of those proxies, so a public
// client cannot spoof an internal source.
func (c *Config) MetricsAllowedCIDRs() []string {
	return getEnvSlice("METRICS_ALLOWED_CIDRS", netallow.DefaultInternalCIDRs)
}

// END metrics network-gate
// BEGIN internal large-message path (OSI-7)
//
// One contiguous, append-only block so it merges cleanly alongside concurrent
// config.go edits. Like the inbound-path block above it touches neither the
// Config struct, Load(), nor the import list — every setting is read lazily
// through a *Config accessor over the helpers already defined above.
//
// SMTP_MAX_MESSAGE_SIZE is admin-configurable and can be large (e.g. 128 MiB).
// Several internal hops that carry a full message body (gateway→API delivery,
// queue→MX outbound send, the RESTMAIL HTTPS hop) historically had SMALLER fixed
// ceilings — a 30 s HTTP-client timeout, a 15 s API read/write timeout, a fixed
// 30 s per-send deadline — that silently strand a large-but-permitted message
// SMTP already accepted. These accessors derive a TIME budget from the message
// size and a floor throughput so each hop permits a full max-size body, while
// staying BOUNDED (a finite function of the configured max size and a positive
// floor rate — never infinite, so the internal path keeps its anti-slowloris
// bound).
// ══════════════════════════════════════════════════════════════════════════

// DefaultInternalDeliveryFloorRate is the assumed MINIMUM throughput, in bytes
// per second, of an internal message-carrying hop when INTERNAL_DELIVERY_FLOOR_RATE
// is unset: 1 MiB/s. It is deliberately far above the public
// SMTP_MIN_TRANSFER_RATE anti-slowloris floor (default 16 KiB/s): the derived
// deadlines govern hops between trusted components over a fast local/internal
// network (and our own outbound send to a peer MX), so a max-size body's time
// budget stays modest (128 MiB ≈ 128 s) instead of the hours a 16 KiB/s floor
// would imply. An operator whose internal API link is genuinely constrained can
// LOWER it to widen every derived budget. Must be positive.
const DefaultInternalDeliveryFloorRate int64 = 1 * 1024 * 1024

// internalDeliveryGrace is the fixed additive headroom on every derived internal
// deadline: connection setup, the TLS handshake, the API-side pipeline run and DB
// write — so a budget is never just the raw byte-transfer time.
const internalDeliveryGrace = 30 * time.Second

// minInternalDeliveryDeadline floors any derived internal deadline, so a small
// configured max size still yields a sane budget and the derivation never
// regresses the historical fixed 30 s internal-client timeout for normal mail.
const minInternalDeliveryDeadline = 30 * time.Second

// defaultStaleDeliveringReclaim is the baseline interval after which an
// outbound-queue row stuck in "delivering" is treated as orphaned by a crashed
// worker and reclaimed. The effective reclaim (StaleDeliveringReclaim) only grows
// above this when a max-size send could legitimately take longer.
const defaultStaleDeliveringReclaim = 15 * time.Minute

// staleDeliveringReclaimMargin is the slack added on top of the worst-case
// single-send budget when the queue reclaim interval is derived, so reclaim
// strictly exceeds any one send attempt: an in-flight legitimately-slow large
// send can never be reclaimed out from under itself (which would double-send).
const staleDeliveringReclaimMargin = 5 * time.Minute

// InternalDeliveryFloorRate returns the internal-hop floor throughput in bytes
// per second (INTERNAL_DELIVERY_FLOOR_RATE, default DefaultInternalDeliveryFloorRate).
// A malformed or non-positive value falls back to the default rather than failing
// startup: this is a liveness/performance budget, not a security gate, and a
// zero/negative rate would make the derived deadline infinite (the one thing this
// block exists to prevent).
func (c *Config) InternalDeliveryFloorRate() int64 {
	v, err := getEnvInt64Strict("INTERNAL_DELIVERY_FLOOR_RATE", DefaultInternalDeliveryFloorRate, "bytes per second")
	if err != nil || v <= 0 {
		return DefaultInternalDeliveryFloorRate
	}
	return v
}

// InternalDeliveryDeadline returns the maximum time one internal hop should be
// allowed to carry a message of sizeBytes: grace + ceil(sizeBytes/floorRate),
// floored at minInternalDeliveryDeadline. BOUNDED by construction — a finite
// function of the size and a positive floor rate, never infinite — so it caps
// slowloris on the internal path while giving a legitimately large message enough
// time to transfer. Pass c.SMTPMaxMessageSize for the worst-case (advertised-max)
// budget.
func (c *Config) InternalDeliveryDeadline(sizeBytes int64) time.Duration {
	if sizeBytes < 0 {
		sizeBytes = 0
	}
	rate := c.InternalDeliveryFloorRate() // guaranteed positive
	secs := sizeBytes / rate
	if sizeBytes%rate != 0 {
		secs++ // round the truncated division up so we never under-budget
	}
	d := internalDeliveryGrace + time.Duration(secs)*time.Second
	if d < minInternalDeliveryDeadline {
		return minInternalDeliveryDeadline
	}
	return d
}

// InternalDeliveryBodyLimit returns the byte ceiling an internal delivery-body
// reader (http.MaxBytesReader on POST /api/v1/messages/deliver and
// POST /restmail/messages) should enforce. It must NEVER cap below the configured
// SMTP_MAX_MESSAGE_SIZE, so it is 3× that max plus 1 MiB of fixed scaffolding
// headroom: the gateway→API delivery JSON carries the full raw message AND its
// extracted text/html body (content duplication ≈ 2×) plus JSON string-escaping
// of CRLFs and field names. The 3× multiple clears that worst case comfortably
// while staying a finite, bounded multiple of an already-bounded maximum — so an
// oversized body is still rejected instead of buffered without limit.
func (c *Config) InternalDeliveryBodyLimit() int64 {
	return c.SMTPMaxMessageSize*3 + 1*1024*1024
}

// StaleDeliveringReclaim returns how long an outbound-queue row may sit in
// "delivering" before the worker reclaims it as orphaned. It is the larger of the
// baseline (defaultStaleDeliveringReclaim) and the worst-case single-send budget
// (InternalDeliveryDeadline(max size) + margin), guaranteeing reclaim strictly
// exceeds any one send attempt so a slow-but-legitimate max-size send completes
// before it can be reclaimed and duplicated. For a normal deployment (10 MiB max)
// the derived value stays below the baseline, so reclaim is unchanged at 15 min.
func (c *Config) StaleDeliveringReclaim() time.Duration {
	derived := c.InternalDeliveryDeadline(c.SMTPMaxMessageSize) + staleDeliveringReclaimMargin
	if derived > defaultStaleDeliveringReclaim {
		return derived
	}
	return defaultStaleDeliveringReclaim
}

// END internal large-message path (OSI-7)
// ══════════════════════════════════════════════════════════════════════════

// ══════════════════════════════════════════════════════════════════════════
// BEGIN boot-time security config validation (OSI-4)
//
// One contiguous, append-only block so it merges cleanly alongside concurrent
// config.go edits. It adds fail-closed boot validation of the security-critical
// configuration — weak/blank/default secrets, a half-configured or unparseable
// TLS keypair, and malformed CIDR allowlists — via the single entry point
// validateSecurityConfig, which is called once from Load() (see the clearly
// marked call there, just before it returns).
//
// Enforcement split: in PRODUCTION (ENVIRONMENT=="production", the exact value
// the two pre-existing JWT/MASTER_KEY checks in Load already key on) an insecure
// value is a hard startup error — the process refuses to boot rather than run
// insecurely. In every OTHER environment (development/test — the testbed/e2e
// default) the identical finding is logged as a warning and boot proceeds, so
// local development and the e2e stack are unaffected. Validation only INSPECTS
// existing settings: it changes no default value and consumes no new required
// knob, so it composes with (rather than duplicates) the earlier checks in Load.
// ══════════════════════════════════════════════════════════════════════════

// MinJWTSecretLength is the minimum accepted JWT_SECRET length in bytes (OSI-4).
// 32 bytes (256 bits) matches the HMAC-SHA256 width used to sign tokens, so a
// shorter secret adds no strength over a full-width one while a much shorter one
// is brute-forceable. instance:new generates a 64-char key, which clears this.
const MinJWTSecretLength = 32

// MinMasterKeyLength is the minimum accepted MASTER_KEY length in bytes (OSI-4).
// MASTER_KEY is a passphrase run through SHA-256 to derive the AES-256
// data-encryption key, so its real strength is bounded by the passphrase's
// guessability; 16 bytes is a conservative floor (instance:new generates 64).
const MinMasterKeyLength = 16

// legacyDefaultJWTSecret is the compiled-in development JWT secret. Shipping it
// outside development is equivalent to having no secret at all (it is public in
// the source tree), so it is rejected the same as a blank secret. It matches the
// default used by getEnv("JWT_SECRET", ...) in Load.
const legacyDefaultJWTSecret = "dev-secret-change-in-production"

// isProductionEnv reports whether enforcement is active. It mirrors the exact
// string comparison the two pre-existing secret checks in Load use, so the whole
// package treats "production" identically (and development/test only warn).
func (c *Config) isProductionEnv() bool { return c.Environment == "production" }

// validateSecurityConfig performs boot-time validation of the security-critical
// configuration and applies the production-enforce / development-warn split. It
// returns a non-nil error ONLY in production and ONLY when at least one insecure
// value is found; in every other environment it logs each finding as a warning
// and returns nil, so the testbed/e2e (which run in development) still boot.
//
// It also emits an advisory warning (production only) for an insecure setting
// OSI-4 names but whose remediation is a behavior/default change deferred to the
// separate secure-by-construction pass (the DB sslmode=disable in DSN()): it is
// surfaced here so it is not silently forgotten, without being a boot-blocker
// that a production operator has no in-scope knob to satisfy.
func (c *Config) validateSecurityConfig() error {
	var findings []string

	// ── Secrets ──
	// JWT_SECRET: reject blank, the built-in dev default, and anything shorter
	// than the entropy floor. (In production the exact-default and blank cases can
	// also be caught earlier in Load; these checks are order-independent and add
	// the missing min-length floor, so they compose rather than duplicate.)
	switch {
	case c.JWTSecret == "":
		findings = append(findings, fmt.Sprintf("JWT_SECRET is empty; set a random secret of at least %d bytes", MinJWTSecretLength))
	case c.JWTSecret == legacyDefaultJWTSecret:
		findings = append(findings, fmt.Sprintf("JWT_SECRET is the built-in development default; set a unique random secret of at least %d bytes", MinJWTSecretLength))
	case len(c.JWTSecret) < MinJWTSecretLength:
		findings = append(findings, fmt.Sprintf("JWT_SECRET is too short (%d bytes); use at least %d bytes of randomness", len(c.JWTSecret), MinJWTSecretLength))
	}

	// MASTER_KEY: reject blank and anything below the length floor. Private keys
	// (DKIM, ACME) are encrypted at rest with a key derived from it.
	switch {
	case c.MasterKey == "":
		findings = append(findings, fmt.Sprintf("MASTER_KEY is empty; set a random key of at least %d bytes (private keys are encrypted at rest with it)", MinMasterKeyLength))
	case len(c.MasterKey) < MinMasterKeyLength:
		findings = append(findings, fmt.Sprintf("MASTER_KEY is too short (%d bytes); use at least %d bytes of randomness", len(c.MasterKey), MinMasterKeyLength))
	}

	// ── TLS ──
	// The primary cert/key pair: when either path is configured, both must be, and
	// they must exist and parse as a valid keypair at boot (fail early, not on the
	// first TLS handshake). Internal-mTLS material is deliberately NOT validated
	// here: the same Config is shared by the API (server material) and the gateways
	// (client material), so each role validates only its half at point of use via
	// InternalMTLSServerTLS / InternalMTLSClientTLS.
	findings = append(findings, validateTLSKeypair("TLS_CERT_PATH", c.TLSCertPath, "TLS_KEY_PATH", c.TLSKeyPath)...)
	// The per-domain SNI cert directory, when configured, must exist and be a dir.
	if strings.TrimSpace(c.TLSCertDir) != "" {
		if info, err := os.Stat(c.TLSCertDir); err != nil {
			findings = append(findings, fmt.Sprintf("TLS_CERT_DIR %q cannot be read: %v", c.TLSCertDir, err))
		} else if !info.IsDir() {
			findings = append(findings, fmt.Sprintf("TLS_CERT_DIR %q is not a directory", c.TLSCertDir))
		}
	}

	// ── CIDR / allowlist knobs ──
	// Each entry must parse as a CIDR block or a bare IP; a malformed entry is a
	// boot error in production rather than being silently dropped at use (which
	// could unexpectedly widen or narrow an allowlist). METRICS_ALLOWED_CIDRS and
	// RESTMAIL_DELIVER_TRUSTED_CIDRS are read lazily through accessors elsewhere,
	// so they are read directly here (with a nil default) to validate only what the
	// operator actually set.
	findings = append(findings, validateCIDRList("PROXY_PROTOCOL_TRUSTED_CIDRS", c.ProxyProtocolTrustedCIDRs)...)
	findings = append(findings, validateCIDRList("METRICS_ALLOWED_CIDRS", getEnvSlice("METRICS_ALLOWED_CIDRS", nil))...)
	findings = append(findings, validateCIDRList("RESTMAIL_DELIVER_TRUSTED_CIDRS", getEnvSlice("RESTMAIL_DELIVER_TRUSTED_CIDRS", nil))...)

	// ── Advisory (production only; not a boot-blocker) ──
	// DSN() hardcodes sslmode=disable, so DB traffic to Postgres is cleartext. The
	// fix (a configurable sslmode defaulting to require in production) is a
	// behavior/default change deferred to the secure-by-construction pass; surface
	// it so it is not forgotten, but do not block boot on a knob that does not yet
	// exist.
	if c.isProductionEnv() {
		slog.Warn("security-config advisory: database connections use sslmode=disable (cleartext to Postgres); enable TLS to the database in production — deferred to secure-by-construction (OSI-4)")
	}

	if len(findings) == 0 {
		return nil
	}
	if c.isProductionEnv() {
		return fmt.Errorf("insecure configuration refused in production (ENVIRONMENT=production): %s", strings.Join(findings, "; "))
	}
	for _, f := range findings {
		slog.Warn("security-config warning (would refuse boot in production)", "finding", f, "environment", c.Environment)
	}
	return nil
}

// validateTLSKeypair validates a certificate/private-key path pair. It returns no
// finding when NEITHER path is set (TLS not configured this way — nothing to
// check); a half-configuration finding when exactly one is set (a cert needs its
// key and vice versa); a per-file finding when a configured file cannot be read;
// and a keypair finding when both files exist but do not form a valid
// certificate/key pair. certKey/keyKey are the env-var names used in messages.
func validateTLSKeypair(certKey, certPath, keyKey, keyPath string) []string {
	certSet := strings.TrimSpace(certPath) != ""
	keySet := strings.TrimSpace(keyPath) != ""
	switch {
	case !certSet && !keySet:
		return nil
	case certSet && !keySet:
		return []string{fmt.Sprintf("%s is set but %s is not; a TLS certificate needs its matching private key", certKey, keyKey)}
	case !certSet && keySet:
		return []string{fmt.Sprintf("%s is set but %s is not; a TLS private key needs its matching certificate", keyKey, certKey)}
	}
	var findings []string
	if _, err := os.Stat(certPath); err != nil {
		findings = append(findings, fmt.Sprintf("%s %q cannot be read: %v", certKey, certPath, err))
	}
	if _, err := os.Stat(keyPath); err != nil {
		findings = append(findings, fmt.Sprintf("%s %q cannot be read: %v", keyKey, keyPath, err))
	}
	if len(findings) > 0 {
		return findings
	}
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		return []string{fmt.Sprintf("%s / %s are not a valid certificate/key pair: %v", certKey, keyKey, err)}
	}
	return nil
}

// validateCIDRList returns a finding for each entry of cidrs that is neither a
// valid CIDR block nor a bare IP address. Blank entries are ignored (the same
// tolerance getEnvSlice/netallow apply). key is the env-var name used in the
// message.
func validateCIDRList(key string, cidrs []string) []string {
	var findings []string
	for _, entry := range cidrs {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(entry); err == nil {
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			continue
		}
		findings = append(findings, fmt.Sprintf("%s contains an entry that is not a valid CIDR block or IP address: %q", key, entry))
	}
	return findings
}

// END boot-time security config validation (OSI-4)
// Multi-resolution rollup downsampling (appended additively — keep contiguous
// so it rebases trivially alongside other in-flight config.go work).
//
// One contiguous, append-only block: like the OSI-7 block above it touches
// neither the Config struct, Load(), nor the import list — every knob is read
// lazily through a *Config accessor over the getEnv* helpers already defined
// above, and read exactly once when the rollup worker is constructed.
//
// The rollup worker keeps fine-grained rollups (pipeline_rollups, one row per
// ROLLUP_INTERVAL bucket) for a recent DETAILED-retention window, then condenses
// older fine rows into a coarser resolution (pipeline_rollups_coarse) and deletes
// the superseded fine rows — bounding storage (~288 five-minute rows/day collapse
// to one daily row) without losing the aggregate signal. These are volume/cost
// dials, NOT security controls (aggregate accuracy is unaffected — the values
// derive from the always-on, never-sampled counters), so a malformed value falls
// back to the secure default rather than failing startup, matching the OSI-7
// block's rationale.
// ══════════════════════════════════════════════════════════════════════════

// DefaultRollupDetailedRetention is how long fine-grained (ROLLUP_INTERVAL)
// rollups are kept at fine resolution before being downsampled, when
// ROLLUP_DETAILED_RETENTION is unset: 7 days, matching the design doc's recent
// hot window. Fine rows in coarse periods that have aged fully past this window
// are condensed to coarse rows and removed. Must be positive.
const DefaultRollupDetailedRetention = 7 * 24 * time.Hour

// DefaultRollupCoarseResolution is the coarse bucket width fine rollups are
// downsampled to when ROLLUP_COARSE_RESOLUTION is unset: 24h (daily), the
// design doc's "fine recent, daily older" model. Should be a whole multiple of
// ROLLUP_INTERVAL so fine buckets tile coarse periods exactly. Must be positive.
const DefaultRollupCoarseResolution = 24 * time.Hour

// DefaultRollupCoarseRetention is the optional hard cap on coarse-row age when
// ROLLUP_COARSE_RETENTION is unset: 0 = disabled (coarse aggregates kept
// indefinitely — the design doc's "aggregate rollups long-retention"). The
// primary storage bound is the resolution reduction itself; set a positive
// duration to also cap the coarse table's absolute growth. Must be non-negative
// (0 disables).
const DefaultRollupCoarseRetention time.Duration = 0

// DefaultRollupDownsampleInterval is how often the downsampling pass runs when
// ROLLUP_DOWNSAMPLE_INTERVAL is unset: hourly (matching the trace pruner
// cadence). Downsampling only ever touches complete coarse periods already past
// the detailed-retention window, so the cadence governs latency-to-condense, not
// correctness. Must be positive.
const DefaultRollupDownsampleInterval = time.Hour

// RollupDetailedRetention returns the fine-resolution retention window
// (ROLLUP_DETAILED_RETENTION, default DefaultRollupDetailedRetention). A
// malformed or non-positive value falls back to the default.
func (c *Config) RollupDetailedRetention() time.Duration {
	v, err := getEnvDurationStrict("ROLLUP_DETAILED_RETENTION", DefaultRollupDetailedRetention)
	if err != nil || v <= 0 {
		return DefaultRollupDetailedRetention
	}
	return v
}

// RollupCoarseResolution returns the coarse (downsampled) bucket width
// (ROLLUP_COARSE_RESOLUTION, default DefaultRollupCoarseResolution). A malformed
// or non-positive value falls back to the default.
func (c *Config) RollupCoarseResolution() time.Duration {
	v, err := getEnvDurationStrict("ROLLUP_COARSE_RESOLUTION", DefaultRollupCoarseResolution)
	if err != nil || v <= 0 {
		return DefaultRollupCoarseResolution
	}
	return v
}

// RollupCoarseRetention returns the optional coarse-row retention cap
// (ROLLUP_COARSE_RETENTION, default DefaultRollupCoarseRetention = 0 disabled). A
// malformed or negative value falls back to the default. 0 keeps coarse
// aggregates indefinitely.
func (c *Config) RollupCoarseRetention() time.Duration {
	v, err := getEnvDurationStrict("ROLLUP_COARSE_RETENTION", DefaultRollupCoarseRetention)
	if err != nil || v < 0 {
		return DefaultRollupCoarseRetention
	}
	return v
}

// RollupDownsampleInterval returns how often the downsampling pass runs
// (ROLLUP_DOWNSAMPLE_INTERVAL, default DefaultRollupDownsampleInterval). A
// malformed or non-positive value falls back to the default.
func (c *Config) RollupDownsampleInterval() time.Duration {
	v, err := getEnvDurationStrict("ROLLUP_DOWNSAMPLE_INTERVAL", DefaultRollupDownsampleInterval)
	if err != nil || v <= 0 {
		return DefaultRollupDownsampleInterval
	}
	return v
}

// END multi-resolution rollup downsampling
// ══════════════════════════════════════════════════════════════════════════

// ══════════════════════════════════════════════════════════════════════════
// BEGIN listener secure-by-construction (secure-by-construction epic)
//
// One contiguous, append-only block so it merges cleanly alongside concurrent
// config.go edits. It EXTENDS the OSI-4 boot-validation pattern above with the
// per-listener TLS / plaintext-AUTH policy and the insecure-knob refusals, and
// reuses the exact production-enforce / development-warn split OSI-4 established
// (via the shared isProductionEnv method). Load() is deliberately NOT changed:
// enforcement is process-specific (each protocol gateway runs different
// listeners), so each cmd/*/main.go calls ValidateListenerSecurity with its own
// role right after config.Load(). In PRODUCTION (ENVIRONMENT=="production") an
// insecure listener/knob is a hard boot error; in development/test (the testbed
// & e2e default) the identical finding is a slog.Warn and boot proceeds
// unchanged — no new boot error can fire outside production.
//
// Per-listener policy (production):
//   - SMTP 25 inbound: plaintext is required by protocol, so it is NOT refused;
//     but a TLS keypair MUST be present so STARTTLS can be advertised and AUTH is
//     never taken before STARTTLS (go-smtp sets AllowInsecureAuth=false whenever
//     a TLS config is present — see internal/gateway/smtp/server.go).
//   - SMTP 587 submission: STARTTLS + keypair required; AUTH only after STARTTLS.
//   - SMTP 465 / IMAP 993 / POP3 995: implicit TLS — a keypair MUST be present
//     (without it the listener binds plaintext instead of TLS).
//   - IMAP 143 / POP3 110: STARTTLS/STLS + keypair required; pre-TLS AUTH refused.
//   - API: never serves TLS itself (TLS is terminated by a front proxy), so the
//     operator must acknowledge that proxy with API_TLS_TERMINATED_BY_PROXY=true;
//     otherwise it would serve auth/token traffic in cleartext.
//
// Insecure-knob refusals (production): QUEUE_TLS_INSECURE=true (disables outbound
// TLS verification / MTA-STS enforcement) and a cleartext DB sslmode
// (DB_SSLMODE=disable/allow/prefer) unless acknowledged with DB_ALLOW_INSECURE=true.
//
// For all three protocol gateways the single switch is the TLS keypair: go-smtp,
// go-imap and go-pop3 each advertise STARTTLS / bind implicit-TLS / refuse
// pre-TLS AUTH only when a non-nil TLS config is present, and degrade to
// plaintext-AUTH-capable listeners when it is absent. So "a valid keypair is
// configured" is exactly the condition that makes every listener secure, and the
// testbed (which mounts /certs/<host>.crt|.key for every gateway) satisfies it.
// ══════════════════════════════════════════════════════════════════════════

// DefaultDBSSLMode is the libpq sslmode used when DB_SSLMODE is unset. It stays
// "disable" so the development testbed's DSN is byte-for-byte unchanged; a
// production deployment sets a secure mode (require/verify-ca/verify-full) or
// explicitly acknowledges the cleartext link with DB_ALLOW_INSECURE=true.
const DefaultDBSSLMode = "disable"

// ListenerRole identifies which process's listeners ValidateListenerSecurity is
// checking, so enforcement covers only the listeners that process actually runs.
type ListenerRole int

const (
	RoleSMTPGateway ListenerRole = iota
	RoleIMAPGateway
	RolePOP3Gateway
	RoleAPI
)

// String names the role for log/error messages.
func (r ListenerRole) String() string {
	switch r {
	case RoleSMTPGateway:
		return "smtp-gateway"
	case RoleIMAPGateway:
		return "imap-gateway"
	case RolePOP3Gateway:
		return "pop3-gateway"
	case RoleAPI:
		return "api"
	default:
		return "unknown"
	}
}

// DBSSLMode returns the libpq sslmode used to build the DSN (DB_SSLMODE, default
// DefaultDBSSLMode). Lower-cased and trimmed; an empty value falls back to the
// default. DSN() reads it, so a deployment sets DB_SSLMODE=require (or a verify-*
// mode) to encrypt DB traffic.
func (c *Config) DBSSLMode() string {
	m := strings.ToLower(strings.TrimSpace(getEnv("DB_SSLMODE", DefaultDBSSLMode)))
	if m == "" {
		return DefaultDBSSLMode
	}
	return m
}

// ValidateListenerSecurity performs the production secure-by-construction boot
// checks for the given process role and applies the OSI-4 production-enforce /
// development-warn split: it returns a non-nil error ONLY in production and ONLY
// when at least one insecure listener/knob is found; in every other environment
// it logs each finding as a warning and returns nil, so the testbed/e2e (which
// run in development) boot exactly as before. Call it once from the process's
// main() right after config.Load().
func (c *Config) ValidateListenerSecurity(role ListenerRole) error {
	var findings []string
	// Shared: every process wired to this validator opens a Postgres connection,
	// so the DB-transport check applies to all roles.
	findings = append(findings, c.dbTransportFindings()...)
	switch role {
	case RoleSMTPGateway:
		findings = append(findings, c.smtpListenerFindings()...)
		findings = append(findings, queueTLSInsecureFindings()...)
	case RoleIMAPGateway:
		findings = append(findings, c.imapListenerFindings()...)
	case RolePOP3Gateway:
		findings = append(findings, c.pop3ListenerFindings()...)
	case RoleAPI:
		findings = append(findings, c.apiListenerFindings()...)
	}
	return c.enforceSecurityFindings(role.String(), findings)
}

// tlsKeypairConfigured reports whether a primary TLS certificate/key pair is
// configured. This is the switch that makes every protocol gateway secure: with
// it, go-smtp/go-imap/go-pop3 advertise STARTTLS, bind implicit-TLS listeners and
// refuse pre-TLS AUTH; without it they degrade to plaintext-AUTH-capable
// listeners. When both paths are set, OSI-4's validateTLSKeypair (run earlier in
// Load) has already confirmed the pair exists and parses, so presence is
// sufficient here.
func (c *Config) tlsKeypairConfigured() bool {
	return strings.TrimSpace(c.TLSCertPath) != "" && strings.TrimSpace(c.TLSKeyPath) != ""
}

// smtpListenerFindings flags an SMTP gateway with any listener enabled but no TLS
// keypair: 25 could not advertise STARTTLS, 587 would run plaintext submission,
// 465 would bind plaintext instead of implicit TLS, and all three would accept
// AUTH before TLS.
func (c *Config) smtpListenerFindings() []string {
	if c.tlsKeypairConfigured() {
		return nil
	}
	if c.SMTPPortInbound <= 0 && c.SMTPPortSubmission <= 0 && c.SMTPPortSubmissionTLS <= 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"SMTP gateway has no TLS keypair (TLS_CERT_PATH/TLS_KEY_PATH): STARTTLS cannot be advertised on port %d (inbound) or %d (submission), implicit TLS cannot run on port %d, and AUTH would be accepted before TLS — provide a certificate/key so STARTTLS is offered and pre-TLS AUTH is refused",
		c.SMTPPortInbound, c.SMTPPortSubmission, c.SMTPPortSubmissionTLS)}
}

// imapListenerFindings flags an IMAP gateway with any listener enabled but no TLS
// keypair: 143 could not advertise STARTTLS, 993 would bind plaintext instead of
// implicit TLS, and both would accept LOGIN/AUTHENTICATE before TLS.
func (c *Config) imapListenerFindings() []string {
	if c.tlsKeypairConfigured() {
		return nil
	}
	if c.IMAPPort <= 0 && c.IMAPTLSPort <= 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"IMAP gateway has no TLS keypair (TLS_CERT_PATH/TLS_KEY_PATH): STARTTLS cannot be advertised on port %d, implicit TLS cannot run on port %d, and LOGIN/AUTHENTICATE would be accepted before TLS — provide a certificate/key so STARTTLS is offered and pre-TLS auth is refused",
		c.IMAPPort, c.IMAPTLSPort)}
}

// pop3ListenerFindings flags a POP3 gateway with any listener enabled but no TLS
// keypair: 110 could not advertise STLS, 995 would bind plaintext instead of
// implicit TLS, and both would accept USER/PASS before TLS.
func (c *Config) pop3ListenerFindings() []string {
	if c.tlsKeypairConfigured() {
		return nil
	}
	if c.POP3Port <= 0 && c.POP3TLSPort <= 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"POP3 gateway has no TLS keypair (TLS_CERT_PATH/TLS_KEY_PATH): STLS cannot be advertised on port %d, implicit TLS cannot run on port %d, and USER/PASS would be accepted before TLS — provide a certificate/key so STLS is offered and pre-TLS auth is refused",
		c.POP3Port, c.POP3TLSPort)}
}

// apiListenerFindings flags the API serving plaintext HTTP with no TLS. The API
// process never terminates TLS itself (cmd/api always ListenAndServe on APIAddr;
// a front proxy does TLS), so the only secure production posture is a
// TLS-terminating reverse proxy, which the operator acknowledges with
// API_TLS_TERMINATED_BY_PROXY=true. Without that acknowledgement, refuse — the
// security headers/HSTS assume HTTPS and auth tokens would otherwise travel in
// cleartext.
func (c *Config) apiListenerFindings() []string {
	if apiTLSTerminatedByProxy() {
		return nil
	}
	return []string{fmt.Sprintf(
		"API would serve plaintext HTTP on %s with no TLS: HSTS/security headers assume HTTPS and auth tokens would travel in cleartext — front it with a TLS-terminating reverse proxy and set API_TLS_TERMINATED_BY_PROXY=true to acknowledge it",
		c.APIAddr())}
}

// dbTransportFindings flags a cleartext DB link. sslmode disable/allow/prefer all
// permit (or silently fall back to) an unencrypted connection to Postgres. In
// production that is refused unless explicitly acknowledged with
// DB_ALLOW_INSECURE=true (e.g. a private, already-encrypted network segment).
func (c *Config) dbTransportFindings() []string {
	if !isInsecureDBSSLMode(c.DBSSLMode()) || dbInsecureAcknowledged() {
		return nil
	}
	return []string{fmt.Sprintf(
		"DB_SSLMODE=%q sends database traffic in cleartext to Postgres — set DB_SSLMODE=require (or verify-ca/verify-full), or acknowledge an already-secured link with DB_ALLOW_INSECURE=true",
		c.DBSSLMode())}
}

// queueTLSInsecureFindings flags QUEUE_TLS_INSECURE=true, which makes the SMTP
// gateway's outbound delivery skip TLS certificate verification (and forces
// MTA-STS enforcement off). It is honored in development for test delivery, but
// in production it silently downgrades every outbound TLS connection, so it is
// refused there. The exact-"true" match mirrors the check in
// cmd/smtp-gateway/main.go so the refusal fires on precisely the value that
// enables the insecure behavior.
func queueTLSInsecureFindings() []string {
	if os.Getenv("QUEUE_TLS_INSECURE") != "true" {
		return nil
	}
	return []string{"QUEUE_TLS_INSECURE=true disables TLS certificate verification for outbound delivery (and turns MTA-STS enforcement off) — unset it so outbound TLS is verified"}
}

// apiTLSTerminatedByProxy reports the operator's acknowledgement that a
// TLS-terminating reverse proxy fronts the plaintext API listener.
func apiTLSTerminatedByProxy() bool { return getEnvBool("API_TLS_TERMINATED_BY_PROXY", false) }

// dbInsecureAcknowledged reports the operator's acknowledgement of a cleartext DB
// link (an already-encrypted/private network segment).
func dbInsecureAcknowledged() bool { return getEnvBool("DB_ALLOW_INSECURE", false) }

// isInsecureDBSSLMode reports whether a libpq sslmode permits or silently falls
// back to a cleartext connection. "disable" never uses TLS; "allow" and "prefer"
// try cleartext-or-TLS and fall back to cleartext, so none of them guarantee
// encryption. "require"/"verify-ca"/"verify-full" always encrypt.
func isInsecureDBSSLMode(mode string) bool {
	switch mode {
	case "disable", "allow", "prefer":
		return true
	default:
		return false
	}
}

// enforceSecurityFindings applies the production-enforce / development-warn split
// (identical to OSI-4's validateSecurityConfig): a non-nil error in production
// when there is at least one finding, otherwise a per-finding slog.Warn and nil.
func (c *Config) enforceSecurityFindings(scope string, findings []string) error {
	if len(findings) == 0 {
		return nil
	}
	if c.isProductionEnv() {
		return fmt.Errorf("insecure %s configuration refused in production (ENVIRONMENT=production): %s", scope, strings.Join(findings, "; "))
	}
	for _, f := range findings {
		slog.Warn("listener security warning (would refuse boot in production)", "scope", scope, "finding", f, "environment", c.Environment)
	}
	return nil
}

// END listener secure-by-construction (secure-by-construction epic)
// ══════════════════════════════════════════════════════════════════════════

// ══════════════════════════════════════════════════════════════════════════
// BEGIN trusted-ARC-sealer allowlist (#178)
//
// One contiguous, append-only block so it merges cleanly alongside concurrent
// config.go edits. The setting is read lazily through a *Config accessor over
// the getEnvSlice helper above; the Config struct and Load() are untouched.
// ══════════════════════════════════════════════════════════════════════════

// TrustedARCSealers returns the allowlist of ARC sealing domains (the d= of the
// most recent ARC-Seal) whose passing ARC chain may override a DMARC failure
// (#178). RFC 8617 makes an ARC "pass" meaningful only when it comes from a
// sealer you trust: without this gate an attacker running their own ARC sealer
// could seal spoofed mail and launder it past the From domain's
// p=reject/quarantine. The default is EMPTY — no sealer is trusted, so ARC stays
// purely informational and never overrides DMARC unless an operator explicitly
// lists a sealer. Set with TRUSTED_ARC_SEALERS (comma-separated domains).
func (c *Config) TrustedARCSealers() []string {
	return getEnvSlice("TRUSTED_ARC_SEALERS", nil)
}

// END trusted-ARC-sealer allowlist
// ══════════════════════════════════════════════════════════════════════════
