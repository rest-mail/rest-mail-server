package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	acmeclient "github.com/restmail/restmail/internal/acme"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/dns"
	"github.com/restmail/restmail/internal/grants"
	"github.com/restmail/restmail/internal/metrics"
	pipelineobs "github.com/restmail/restmail/internal/metrics/observer"
	"github.com/restmail/restmail/internal/netallow"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters" // register built-in filters via init() + DB-backed factories
	"github.com/restmail/restmail/internal/push"
	"github.com/restmail/restmail/internal/ratelimit"
	"github.com/restmail/restmail/internal/trace"
	"github.com/restmail/restmail/internal/version"
	"gorm.io/gorm"
)

// Routers bundles the HTTP handlers a running API exposes. Public is always
// present. Internal is non-nil only when internal mTLS is enabled: it serves
// the gateway-facing machine-to-machine routes (recipient checks + inbound
// delivery) and is meant to be served on a dedicated listener that requires a
// verified gateway client certificate. When internal mTLS is disabled those
// routes live on Public (tokenless), preserving pre-mTLS behavior.
type Routers struct {
	Public   http.Handler
	Internal http.Handler

	// Recorder is the async per-message trace sink shared by the delivery
	// handlers. It owns a background goroutine; callers that manage a server
	// lifecycle (cmd/api) must call Routers.Close on shutdown to flush it.
	Recorder *trace.Recorder

	// Messages is the mail handler, exposed so the server can start the
	// scheduled-send loop it owns. Not started here: route tests, which have
	// no mail to send, must not start a background sender.
	Messages *handlers.MessageHandler

	// Registry holds every route registered with endpoint.Register: the types
	// each one takes and answers, from which the API description is generated.
	// A route mounted without it is on the shrinking list the route-registry
	// test keeps (#669).
	Registry *endpoint.Registry

	// push is push notifications, when configured; its dispatcher owns a
	// goroutine that Routers.Close stops.
	push *pushService
}

// Close releases background resources owned by the routers — currently the trace
// recorder, whose buffered traces are flushed and whose goroutine is stopped.
// Safe to call on a zero/never-started recorder.
func (rt *Routers) Close() {
	if rt == nil {
		return
	}
	rt.push.close()
	rt.Recorder.Shutdown()
}

// NewRouter creates and configures the chi router with all API routes and
// returns the public handler. It is the entry point for callers that do not
// run a separate internal mTLS listener (tests, tooling).
// The acmeClient parameter is optional; pass nil when ACME is disabled.
// sessionCheck builds the check that refuses tokens from before the account's
// sessions last ended, from the router's session cache. A variable so this
// package's route tests, which run on a database that fails every query in
// order to test the capability gates alone, can stand it aside; the check
// itself is tested against a real database.
var sessionCheck = cachedSessionCheck

func cachedSessionCheck(cache *handlers.SessionCache) middleware.SessionCurrent {
	return cache.Current
}

// heldGrants builds what RequireCapability reads an account's grants from:
// the router's grants cache. A variable for the same reason as sessionCheck;
// the grants themselves are tested against a real database.
var heldGrants = cachedHeldGrants

func cachedHeldGrants(cache *grants.Cache) middleware.HeldGrants {
	return cache.Held
}

func NewRouter(db *gorm.DB, jwtService *auth.JWTService, cfg *config.Config, dnsProvider dns.Provider, acmeClient ...*acmeclient.Client) http.Handler {
	return NewRouters(db, jwtService, cfg, dnsProvider, acmeClient...).Public
}

// NewRouters builds the public handler and, when cfg.InternalMTLSEnabled is
// true, the internal mTLS handler. Both share the same handler instances — most
// importantly the same SSE broker and pipeline engine — so inbound deliveries
// arriving on the internal listener still publish real-time events to webmail
// clients subscribed on the public listener.
func NewRouters(db *gorm.DB, jwtService *auth.JWTService, cfg *config.Config, dnsProvider dns.Provider, acmeClient ...*acmeclient.Client) *Routers {
	r := chi.NewRouter()
	registry := endpoint.NewRegistry()

	// Global middleware
	//
	// A forwarded header (X-Forwarded-For) is honored only when the direct TCP peer
	// is a configured trusted proxy (PROXY_PROTOCOL_TRUSTED_CIDRS). The allowlist is
	// empty by default, so out of the box the genuine socket peer is always used and
	// a public client cannot spoof its source IP. This one allowlist governs both
	// the metrics network-gate and the RealIP rewrite below.
	trustedProxies := netallow.New("api-proxy", cfg.ProxyProtocolTrustedCIDRs)

	// Metrics network-gate (OSI-12) MUST come first — before the RealIP rewrite — so
	// it evaluates the genuine TCP peer, not a client-spoofable X-Forwarded-For. It
	// restricts /metrics to internal CIDRs (default loopback + RFC1918, so the
	// in-cluster Prometheus scrape keeps working) and 404s any other peer; every
	// non-/metrics path passes straight through.
	r.Use(middleware.MetricsAllowlist(middleware.MetricsAllowlistConfig{
		Path:           "/metrics",
		Allow:          netallow.New("api-metrics", cfg.MetricsAllowedCIDRs()),
		TrustedProxies: trustedProxies,
	}))
	r.Use(chimw.RequestID)
	// TrustedRealIP replaces chi's RealIP: it resolves the client IP from
	// X-Forwarded-For ONLY for trusted-proxy peers and writes it into RemoteAddr, so
	// the auth rate limiter, the RESTMAIL delivery-auth gate, and the negative-lookup
	// tarpit — all of which key on RemoteAddr — cannot be fooled by a spoofed header.
	r.Use(middleware.TrustedRealIP(trustedProxies))
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	// Security headers (OSI-11) — HSTS/nosniff/frame-deny/referrer-policy/CSP on
	// every response. Opt-out via SECURITY_HEADERS_ENABLED for deployments that
	// terminate these at a reverse proxy.
	if cfg.SecurityHeadersEnabled {
		r.Use(middleware.SecurityHeaders(middleware.SecurityHeadersConfig{
			HSTSMaxAgeSeconds: cfg.HSTSMaxAgeSeconds,
		}))
	}
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSAllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	r.Use(metrics.HTTPMetrics)

	// Initialize handlers
	// What each account may do is read from its grants on every request,
	// through a cache that every grant change made here empties at once
	// (#345).
	grantCache := grants.NewCache(db, grants.CacheTTL)
	grantH := handlers.NewGrantHandler(db, grantCache, cfg.MasterKey)
	// Whether a session is still current (its account active, its token from
	// the account's credential epoch) is read on every request too, through a
	// cache that every change made here that ends an account's sessions
	// empties for that account at once (#354).
	sessions := handlers.NewSessionCache(db, handlers.SessionCacheTTL)
	sessionCurrent := sessionCheck(sessions)

	healthH := handlers.NewHealthHandler(db, jwtService)
	authH := handlers.NewAuthHandler(db, jwtService, cfg.MasterKey, sessions)
	twofaH := handlers.NewTwoFactorHandler(db, cfg.MasterKey, cfg.TOTP2FAEnabled, sessions)
	domainH := handlers.NewDomainHandler(db, dnsProvider)
	mailboxH := handlers.NewMailboxHandler(db, grantCache, sessions)
	aliasH := handlers.NewAliasHandler(db)
	broker := handlers.NewSSEBroker()
	eventH := handlers.NewEventHandler(db, broker, jwtService, sessionCurrent)
	accountH := handlers.NewAccountHandler(db, grantCache, cfg.MasterKey)
	searchH := handlers.NewSearchHandler(db)
	attachmentStore := attachmentstore.NewS3(cfg.Attachments)
	attachmentH := handlers.NewAttachmentHandler(db, attachmentStore)
	contactH := handlers.NewContactHandler(db)
	vacationH := handlers.NewVacationHandler(db)
	sieveH := handlers.NewSieveHandler(db)
	senderRuleH := handlers.NewSenderRuleHandler(db)
	suppressionH := handlers.NewSuppressionHandler(db)
	queueH := handlers.NewQueueHandler(db)
	banH := handlers.NewBanHandler(db)
	logH := handlers.NewLogHandler(db)
	dkimH := handlers.NewDKIMHandler(db, cfg.MasterKey)
	certH := handlers.NewCertificateHandler(db, cfg.MasterKey)
	testH := handlers.NewTestHandler(db, cfg)
	mtastsH := handlers.NewMTASTSHandler(db)
	relayH := handlers.NewRelayHandler(db, cfg.MasterKey)
	tlsrptH := handlers.NewTLSReportHandler(db)
	statsH := handlers.NewStatsHandler(db)
	auditH := handlers.NewAuditHandler(db)

	// The audit log (#343): sign-ins and every administrative change are
	// recorded at the edge, by middleware on the routes below. Each Audit sits
	// after the JWT middleware, to see who is acting, and after the rate limit
	// of a route anyone may call, so nobody can write to the log faster than
	// that limit allows.
	auditor := middleware.NewAuditor(
		func(e *models.AuditEntry) error { return audit.Append(db, e) },
		func(kind, name string) (*uint, error) { return audit.ActorID(db, kind, name) },
		jwtService,
	)
	auditLog := auditor.Audit

	// HTTP Basic: the mail routes below also take a credential sent as
	// address:password, an app password (#344) or one of the account's
	// passwords, checked against the credentials table on every request, so a
	// revoked one stops working at once. It is how the protocol gateways sign
	// mail clients in, one path for both. Each refusal is recorded, and
	// refusals are throttled at login's rate (see middleware.BasicCredentials).
	var basicRefusals *middleware.RateLimitConfig
	if cfg.AuthRateLimitEnabled {
		basicRefusals = &middleware.RateLimitConfig{RPS: cfg.AuthRateLimitRPS, Burst: cfg.AuthRateLimitBurst}
	}
	basicCredentials := middleware.NewBasicCredentials(handlers.BasicVerifier(db), auditor.BasicRefused, basicRefusals)
	credentialH := handlers.NewCredentialHandler(db)
	apiKeys := middleware.NewAPIKeys(repositories.NewCredentialRepository(db).APIKeyVerifier(),
		func(r *http.Request, refusal *auth.CredentialRefusal) { auditor.BasicRefused(r, "", refusal) }, basicRefusals)

	// Register DB-backed filters that need a database connection.
	pipeline.DefaultRegistry.Register("extract_attachments", filters.NewExtractAttachmentsWithS3(attachmentStore))
	pipeline.DefaultRegistry.Register("greylist", filters.NewGreylist(db))
	pipeline.DefaultRegistry.Register("vacation", filters.NewVacation(db))
	pipeline.DefaultRegistry.Register("domain_allowlist", filters.NewDomainAllowlist(db))
	pipeline.DefaultRegistry.Register("contact_whitelist", filters.NewContactWhitelist(db))
	pipeline.DefaultRegistry.Register("recipient_check", filters.NewRecipientCheck(db))
	pipeline.DefaultRegistry.Register("sender_verify", filters.NewSenderVerify(db))
	// #178: bind dmarc_check to the deployment's trusted-ARC-sealer allowlist,
	// overriding the empty-allowlist default from init(). A passing ARC chain
	// overrides a DMARC failure only when its sealing domain is allowlisted; the
	// default (empty) keeps ARC informational so an untrusted sealer cannot bypass
	// the From domain's p=reject/quarantine.
	pipeline.DefaultRegistry.Register("dmarc_check", filters.NewDMARCCheckWithSealers(cfg.TrustedARCSealers()))
	// OSI-13: bind the sieve filter to the deployment's redirect allowlist,
	// overriding the deny-external default baked into the init() registration.
	sieveRedirect := cfg.SieveRedirect()
	pipeline.DefaultRegistry.Register("sieve", filters.NewSieveWithPolicy(filters.SieveRedirectPolicy{
		AllowExternal:  sieveRedirect.AllowExternal,
		AllowedDomains: sieveRedirect.AllowedDomains,
	}))
	// OSI-15: bind the external content-scanner filters to the deployment's shared
	// HMAC secret so verdicts are authenticated end-to-end (a plain-HTTP verdict
	// cannot be forged/downgraded). The adapters also default to fail-CLOSED
	// (defer) when the scanner is unreachable/errors. No scanner runs by default — these filters are active only when an
	// operator adds them to a pipeline, so the default pipeline is unaffected.
	scannerSecret := cfg.ScannerHMACSecret()
	pipeline.DefaultRegistry.Register("rspamd", filters.NewRspamdWithSecret(scannerSecret))
	pipeline.DefaultRegistry.Register("clamav", filters.NewClamAVWithSecret(scannerSecret))
	// #160: javascript filters run on the deployment's sidecar, authenticated
	// with its token; a pipeline's config cannot point them elsewhere.
	jsSidecar := filters.JSSidecar{URL: cfg.JSFilterURL(), Token: cfg.JSFilterToken()}
	pipeline.DefaultRegistry.Register("javascript", filters.NewJavaScriptWithSidecar(jsSidecar))

	// The message-processing engine carries the metrics observer so real
	// inbound/outbound message flow emits pipeline metrics. The pipeline
	// preview/test handler uses a plain engine so admin "try this pipeline"
	// runs never pollute the aggregate metrics.
	pipelineEngine := pipeline.NewEngine(pipeline.DefaultRegistry, slog.Default(), pipelineobs.New())
	previewEngine := pipeline.NewEngine(pipeline.DefaultRegistry, slog.Default())
	// OSI-18: apply the configured fail-open/closed action for unknown or errored
	// filters. The engine defaults to fail-closed (defer); this lets an operator
	// choose (e.g. "continue" to fail open).
	filterErrAction := pipeline.Action(cfg.PipelineFilterErrorAction())
	pipelineEngine.SetFilterErrorAction(filterErrAction)
	previewEngine.SetFilterErrorAction(filterErrAction)
	// Per-filter execution backstop: a hung/deadlocked filter is abandoned after
	// this and routed through the fail-closed policy above, so it can never wedge
	// delivery. Combined with the engine's panic recovery, a single misbehaving
	// filter is always contained to its own step.
	filterTimeout := cfg.PipelineFilterTimeout()
	pipelineEngine.SetFilterTimeout(filterTimeout)
	previewEngine.SetFilterTimeout(filterTimeout)

	// Async per-message trace recorder: durable MessageTrace rows are written off
	// the hot path by one background goroutine, so a slow/failed DB drops traces
	// (drop-on-full) but never blocks or fails message processing. Injected into
	// the delivery handlers; stopped/flushed via Routers.Close on shutdown.
	//
	// PR4 sampling/retention: happy-path traces are kept at cfg.TraceSampleRate
	// (anomalies always 100%); recorded traces get an expires_at horizon
	// (cfg.TraceRetention) for the pruner. Aggregate counts are unaffected — they
	// come from the always-on metrics, not these sampled rows.
	traceRecorder := trace.NewRecorder(db, trace.Config{
		SampleRate: cfg.TraceSampleRate,
		Retention:  cfg.TraceRetention(),
	})
	traceRecorder.Start()

	messageH := handlers.NewMessageHandler(db, broker, pipelineEngine, cfg.MasterKey, traceRecorder)
	// Send-path abuse limits (#184): a per-message recipient cap and a per-account
	// send rate limiter, mirroring the SMTP submission caps (#171) so the webmail/API
	// send path (and the draft-send / forward paths that delegate to it) cannot be
	// used to fan out unlimited bulk mail. The limiter is shared across all requests.
	messageH.SetSendLimits(
		cfg.APIMaxRecipientsPerMessage,
		ratelimit.NewSubmissionLimiter(cfg.APISendRateLimitPerMinute, cfg.APISendRateLimitPerHour),
	)
	// Push notifications (#363): off, with no devices routes, unless APNs is
	// configured. New mail in an INBOX goes to the phones of the sessions that
	// list the mailbox, and a logout takes its session's phones with it.
	pushSvc := newPushService(db, cfg)
	if pushSvc != nil {
		messageH.SetNewMailNotifier(pushSvc.dispatcher)
		authH.SetPushDevices(pushSvc.store)
	}
	pipelineH := handlers.NewPipelineHandler(db, previewEngine, jsSidecar)
	restmailDeliverAuth := cfg.RestmailDeliverAuth()
	restmailH := handlers.NewRestmailHandler(db, pipelineEngine, traceRecorder, handlers.RestmailTarpitConfig{
		Enabled: cfg.RestmailTarpitEnabled,
		Base:    cfg.RestmailTarpitBase,
		Max:     cfg.RestmailTarpitMax,
	}, handlers.RestmailDeliverAuthConfig{
		Strict:       restmailDeliverAuth.Strict,
		TrustedCIDRs: restmailDeliverAuth.TrustedCIDRs,
	})

	// ═══════════════════════════════════════════════════════════════
	// API root — version and discovery (no auth)
	// ═══════════════════════════════════════════════════════════════
	r.Get("/api", func(w http.ResponseWriter, r *http.Request) {
		respond.Data(w, http.StatusOK, map[string]any{
			"name":       "restmail",
			"version":    version.Version,
			"commit":     version.Commit,
			"build_date": version.BuildDate,
			"links": map[string]string{
				"health": "/api/health",
				"docs":   "/api/docs",
				"login":  "/api/v1/auth/login",
			},
		})
	})

	// ═══════════════════════════════════════════════════════════════
	// API Documentation (no auth)
	// ═══════════════════════════════════════════════════════════════
	r.Get("/api/docs", SwaggerUIHandler())
	r.Get("/api/docs/openapi.yaml", OpenAPISpecHandler())

	// ═══════════════════════════════════════════════════════════════
	// Health — public liveness probe (no auth, always 200 when up). The handler
	// additionally surfaces the certificate inventory/expiry, but ONLY to a valid
	// admin token; unauthenticated callers receive the liveness response only.
	// ═══════════════════════════════════════════════════════════════
	r.Get("/api/health", healthH.Health)

	// ═══════════════════════════════════════════════════════════════
	// Prometheus metrics (no auth)
	// ═══════════════════════════════════════════════════════════════
	// Key inventory belongs to the API, rather than each protocol gateway.
	// A router-local registry avoids retaining DB handles in the global registry.
	dkimRegistry := prometheus.NewRegistry()
	dkimRegistry.MustRegister(metrics.NewDKIMKeyAgeCollector(db))
	r.Handle("/metrics", promhttp.InstrumentMetricHandler(prometheus.DefaultRegisterer,
		promhttp.HandlerFor(prometheus.Gatherers{
			prometheus.DefaultGatherer, dkimRegistry,
		}, promhttp.HandlerOpts{})))

	// ═══════════════════════════════════════════════════════════════
	// Email client auto-configuration (no auth)
	// ═══════════════════════════════════════════════════════════════
	autoconfigH := handlers.NewAutoconfigHandler(db)
	r.Get("/mail/config-v1.1.xml", autoconfigH.MozillaAutoconfig)
	r.Get("/.well-known/autoconfig/mail/config-v1.1.xml", autoconfigH.MozillaAutoconfig)
	r.Post("/autodiscover/autodiscover.xml", autoconfigH.MicrosoftAutodiscover)

	// ═══════════════════════════════════════════════════════════════
	// MTA-STS policy (no auth — served to external MTAs per RFC 8461)
	// ═══════════════════════════════════════════════════════════════
	r.Get("/.well-known/mta-sts.txt", mtastsH.ServePolicy)

	// ═══════════════════════════════════════════════════════════════
	// TLS-RPT report ingestion (no auth — receives reports from external MTAs per RFC 8460)
	//
	// Unauthenticated, so it is throttled per client IP (issue #183) to stop it
	// being used as a storage write-amplification vector. The handler additionally
	// caps the body size and the number of policy entries, and stores reports only
	// for domains this server hosts.
	// ═══════════════════════════════════════════════════════════════
	tlsrptThrottle := func(next http.Handler) http.Handler { return next }
	if cfg.TLSRPTRateLimitEnabled {
		tlsrptThrottle = middleware.RateLimit(middleware.RateLimitConfig{
			RPS:   cfg.TLSRPTRateLimitRPS,
			Burst: cfg.TLSRPTRateLimitBurst,
		})
	}
	r.With(tlsrptThrottle).Post("/.well-known/smtp-tlsrpt", tlsrptH.ReceiveReport)

	// ═══════════════════════════════════════════════════════════════
	// ACME HTTP-01 challenge (no auth — served to ACME CA for domain validation)
	// ═══════════════════════════════════════════════════════════════
	if len(acmeClient) > 0 && acmeClient[0] != nil {
		r.Handle("/.well-known/acme-challenge/*", acmeClient[0].ChallengeHandler())
	}

	// ═══════════════════════════════════════════════════════════════
	// Auth (no JWT). Login (credential verification) and refresh (token
	// minting) are throttled per client IP to blunt brute-force /
	// credential-stuffing; logout carries no such risk. The limiter is a
	// dependency-free per-IP token bucket (see middleware.RateLimit) and is
	// bounded so ordinary interactive use is unaffected.
	// ═══════════════════════════════════════════════════════════════
	authThrottle := func(next http.Handler) http.Handler { return next }
	if cfg.AuthRateLimitEnabled {
		authThrottle = middleware.RateLimit(middleware.RateLimitConfig{
			RPS:   cfg.AuthRateLimitRPS,
			Burst: cfg.AuthRateLimitBurst,
		})
	}
	r.With(authThrottle, auditLog("auth.sign_in", middleware.AuditSignIn())).Post("/api/v1/auth/login", authH.Login)
	// Unthrottled, so only a logout that names a session is recorded.
	r.With(auditLog("auth.sign_out", middleware.AuditRefreshToken(), middleware.AuditIdentifiedOnly())).Post("/api/v1/auth/logout", authH.Logout)
	r.With(authThrottle, auditLog("auth.refresh", middleware.AuditRefreshToken(), middleware.AuditFailuresOnly())).Post("/api/v1/auth/refresh", authH.Refresh)
	// Redeem a single-use token minted by the operator command (restmail-api
	// mint-token) or for an owner key: reset an admin who has lost every way
	// in. Throttled like login: it guesses at a secret.
	r.With(authThrottle, auditLog("auth.reset", middleware.AuditStartedSession())).Post("/api/v1/auth/reset", authH.Reset)
	// Self-service password reset with the authenticator and a recovery code
	// the account set up in advance (#124). Throttled like login, and each
	// address may be refused only 5 times, then once per 15 minutes, so
	// guesses spread across many IPs still cannot add up against one account.
	if cfg.AuthRateLimitEnabled {
		authH.SetRecoverFailures(middleware.NewFailureBudget(middleware.RateLimitConfig{
			RPS:   1.0 / (15 * 60),
			Burst: 5,
			TTL:   2 * time.Hour,
		}))
	}
	r.With(authThrottle, auditLog("auth.recover", middleware.AuditStartedSession())).Post("/api/v1/auth/recover", authH.Recover)
	// Owners prove themselves with a key registered in PostgreSQL: sign a
	// challenge, get a reset-admin token to redeem above. Throttled at
	// login's rate, but in a bucket of its own: the CLI's requests must not use
	// up the budget the owner then needs to sign in. main has already refused
	// to start on keys that do not parse.
	ownerThrottle := func(next http.Handler) http.Handler { return next }
	if cfg.AuthRateLimitEnabled {
		ownerThrottle = middleware.RateLimit(middleware.RateLimitConfig{
			RPS:   cfg.AuthRateLimitRPS,
			Burst: cfg.AuthRateLimitBurst,
		})
	}
	statusKeys, _ := bootstrap.ParseStatusKeys(cfg.StatusKeys)
	ownerH := handlers.NewOwnerHandler(db, statusKeys, cfg.JWTSecret)
	r.With(ownerThrottle).Post("/api/v1/auth/owner/challenge", ownerH.Challenge)
	r.With(ownerThrottle, auditLog("auth.owner_token", middleware.AuditSignIn())).Post("/api/v1/auth/owner/token", ownerH.Token)
	// The same challenge, signed for "status" with a key in
	// RESTMAIL_STATUS_KEYS (or an owner key), reads the version, commit, build
	// and start time: a deploy confirmed from outside. Same bucket.
	r.With(ownerThrottle, auditLog("auth.owner_status")).Post("/api/v1/auth/owner/status", ownerH.Status)

	// What this server is, for anyone who can reach it: how `restmail status`
	// and the app confirm what is running. Shares the owner endpoints' bucket,
	// so a script polling it cannot use up the budget anyone needs to sign in.
	r.With(ownerThrottle).Get("/api/v1/server", handlers.DescribeServer)

	// M-14 (CWE-770): the admin pipeline/filter test endpoints each run a message
	// through the pipeline engine. They are capability-gated but were otherwise
	// unthrottled, so an authenticated admin could hammer them. Apply the same
	// per-client-IP token bucket used on the auth routes, on its own dedicated
	// limiter (separate bucket from auth) and its own config knobs.
	pipelineTestThrottle := func(next http.Handler) http.Handler { return next }
	if cfg.PipelineTestRateLimitEnabled {
		pipelineTestThrottle = middleware.RateLimit(middleware.RateLimitConfig{
			RPS:   cfg.PipelineTestRateLimitRPS,
			Burst: cfg.PipelineTestRateLimitBurst,
		})
	}

	// ═══════════════════════════════════════════════════════════════
	// Inbound delivery — machine-to-machine, called by the protocol
	// gateways with no user token.
	//
	// registerInternal wires the two gateway-facing routes onto a router.
	// When internal mTLS is DISABLED they go on the public router here, exactly
	// as before (network-trust deployments unchanged). When ENABLED they are
	// withheld from the public router and served only on the dedicated internal
	// mTLS listener built below, so an unauthenticated public caller can no
	// longer reach them.
	// ═══════════════════════════════════════════════════════════════
	// OSI-7: bound the delivery body so a runaway upload cannot buffer without
	// limit, while never capping below the configured max message size (the limit
	// is a multiple of SMTP_MAX_MESSAGE_SIZE plus headroom). The recipient-check
	// GET carries no body, so only the deliver POST is wrapped.
	deliverBodyLimit := middleware.MaxBodyBytes(cfg.InternalDeliveryBodyLimit())
	registerInternal := func(rt chi.Router) {
		rt.Get("/api/mailboxes", mailboxH.CheckAddress)
		rt.With(deliverBodyLimit).Post("/api/v1/messages/deliver", messageH.DeliverMessage)
	}
	if !cfg.InternalMTLSEnabled {
		registerInternal(r)
	}

	// ═══════════════════════════════════════════════════════════════
	// RESTMAIL server-to-server. No API key (like SMTP, any server may connect),
	// but delivery is authenticated at the handler (OSI-3): trusted-peer network
	// or DKIM alignment, refusing spoofed local-domain injection.
	// ═══════════════════════════════════════════════════════════════
	restmailDiscoveryH := handlers.NewRestmailDiscoveryHandler(db, cfg.GatewayHostname)
	r.Get("/.well-known/restmail", restmailDiscoveryH.Discover)
	r.Get("/restmail/capabilities", restmailH.Capabilities)
	r.Get("/restmail/mailboxes", restmailH.CheckMailbox)
	// OSI-7: same delivery-body bound as the machine deliver route — the RESTMAIL
	// hop must accept up to a full max-size message but not buffer without limit.
	r.With(deliverBodyLimit).Post("/restmail/messages", restmailH.Deliver)

	// ═══════════════════════════════════════════════════════════════
	// SSE — outside the JWT middleware group; the handler authenticates from the
	// restmail_access cookie (native EventSource) or an Authorization: Bearer
	// header. GET, so it is CSRF-exempt.
	// ═══════════════════════════════════════════════════════════════
	r.Get("/api/v1/accounts/{id}/events", eventH.Events)

	// Provisioning uses the same live mailboxes:write check for sessions and
	// explicitly scoped API keys. Other identity and credential changes below
	// still require a password-authenticated session.
	audited := describedAudit(auditLog)
	accountH.RegisterCreate(endpoint.Routes{Registry: registry, Router: r.With(
		apiKeys.Authenticate(middleware.JWTMiddleware(jwtService)),
		middleware.RejectStaleSessions(sessionCurrent), middleware.SetupDone,
		middleware.CSRF(), middleware.MaxBodyBytes(cfg.APIMaxBodyBytes))}.
		Describing(describedAdminSession, describedSetupDone, describedCSRF, describedBodyCap),
		audited(handlers.ActionAccountsCreate, middleware.AuditBodyFields("address"), middleware.AuditFailuresOnly()))

	// ═══════════════════════════════════════════════════════════════
	// Authenticated routes that need a person: a session from signing in
	// with a password. They take no credential over HTTP Basic, so an app
	// password can never change a password or 2FA, create or link an
	// account, or create or revoke a credential (#341, Decided 5).
	// ═══════════════════════════════════════════════════════════════
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTMiddleware(jwtService))
		// A token minted before the account's last credential change is dead.
		r.Use(middleware.RejectStaleSessions(sessionCurrent))
		// Double-submit CSRF guard for cookie-authenticated browser sessions on
		// state-changing methods; Bearer-token (gateway) callers send no cookie
		// and are transparently exempt (see middleware.CSRF).
		r.Use(middleware.CSRF())
		// #184: cap the request body across the authenticated surface (send,
		// drafts, contacts import, sieve PUT, …) so an unbounded JSON upload
		// cannot exhaust memory. cfg.APIMaxBodyBytes <= 0 makes this a no-op.
		r.Use(middleware.MaxBodyBytes(cfg.APIMaxBodyBytes))

		// Finishing an account's setup, and managing its own 2FA (OSI-19):
		// the only routes a session still in setup reaches.
		//
		// #204: the state-changing / code-verifying endpoints share the SAME
		// per-client-IP auth throttle as /auth/login (same limiter instance). Left
		// unthrottled they were bounded only by the 15-minute access-token
		// lifetime, so a holder of a live session could hammer confirm/disable to
		// brute-force TOTP codes — inconsistent with the throttled login path. The
		// status read carries no secret to guess and is left unthrottled so the SPA
		// can poll it freely.
		r.Get("/api/v1/auth/2fa", twofaH.Status)
		// An account replaces its own password; with 2FA enrollment, all a
		// session in setup can do. Throttled like login: it checks a password.
		r.With(authThrottle, auditLog("auth.password_change")).Post("/api/v1/auth/password", authH.ChangePassword)
		r.With(authThrottle, auditLog("auth.2fa_enroll")).Post("/api/v1/auth/2fa/enroll", twofaH.Enroll)
		r.With(authThrottle, auditLog("auth.2fa_confirm")).Post("/api/v1/auth/2fa/confirm", twofaH.Confirm)
		r.With(authThrottle, auditLog("auth.2fa_disable")).Post("/api/v1/auth/2fa/disable", twofaH.Disable)

		r.Group(func(r chi.Router) {
			r.Use(middleware.SetupDone)
			person := endpoint.Routes{Router: r, Registry: registry}.
				Describing(describedPersonSession, describedCSRF, describedBodyCap, describedSetupDone)
			personChecks := handlers.SessionRouteChecks{Throttle: describedThrottle(authThrottle), Audit: audited}

			// Accounts (#347). Creating one needs mailboxes:write covering its
			// domain, and makes the creator its owner; the account, its grant
			// and their entries are recorded in the change's transaction, and a
			// refused attempt here.
			// Linking proves an account's password (and its 2FA), so it is an
			// online password-guessing oracle: it shares the SAME per-client-IP
			// auth throttle as /auth/login (same limiter instance), so a wrong
			// guess consumes the same budget as a failed login and cannot be
			// used to sidestep it. Unlinking removes the caller's own grants on
			// the account.
			accountH.RegisterLinks(person, personChecks)

			// Credentials (#344): an account's app passwords, its own or one it
			// holds mailbox:manage on. The secret is in the answer to creating
			// one, once, and never in the audit log. Creating one takes the
			// session's current password, so it is throttled like login, with
			// the same limiter: a wrong one spends login's budget.
			//
			// API keys are distinct from mail-client app passwords. Their
			// permissions can only be delegated by a two-factor session, never
			// by another key; handlers audit changes in their transactions.
			credentialH.Register(person, personChecks)

			// Push devices (#363): the phones this session registers, only when
			// push is configured; otherwise there are no such routes, and the
			// 404 tells the app push is off. Never the token in the audit log.
			if pushSvc != nil {
				r.Get("/api/v1/devices", pushSvc.devices.List)
				r.With(auditLog(push.ActionDeviceRegister, middleware.AuditBodyFields("platform", "environment"),
					middleware.AuditResponseField("id", "device_id"))).Post("/api/v1/devices", pushSvc.devices.Register)
				r.With(auditLog(push.ActionDeviceRemove)).Delete("/api/v1/devices/{device_id}", pushSvc.devices.Remove)
			}
		})
	})

	// ═══════════════════════════════════════════════════════════════
	// Authenticated routes (mail server operations): a session, or a
	// credential sent with HTTP Basic (an app password, whose scope is its own
	// mailbox with read, write and send, or one of the account's passwords).
	// The protocol gateways reach them with Basic.
	// ═══════════════════════════════════════════════════════════════
	r.Group(func(r chi.Router) {
		r.Use(apiKeys.Authenticate(basicCredentials.Authenticate(jwtService)))
		r.Use(middleware.RejectStaleSessions(sessionCurrent))
		r.Use(middleware.SetupDone)
		r.Use(middleware.CSRF())
		r.Use(middleware.MaxBodyBytes(cfg.APIMaxBodyBytes))

		// A protocol gateway signs a mail client in with what it gave as its
		// password, sent with HTTP Basic. Throttled like login; a refused
		// credential is recorded by basicCredentials, a successful sign-in
		// here.
		r.With(authThrottle, auditLog("auth.protocol_sign_in", middleware.AuditBodyFields("protocol"))).
			Post("/api/v1/auth/protocol-sign-in", credentialH.ProtocolSignIn)

		accountH.RegisterReads(endpoint.Routes{Router: r, Registry: registry}.
			Describing(describedMailSession, describedSetupDone, describedCSRF, describedBodyCap))

		// Folders
		r.Get("/api/v1/accounts/{id}/folders", messageH.ListFolders)
		r.Post("/api/v1/accounts/{id}/folders", messageH.CreateFolder)
		r.Patch("/api/v1/accounts/{id}/folders/{folder}", messageH.RenameFolder)
		r.Delete("/api/v1/accounts/{id}/folders/{folder}", messageH.DeleteFolder)

		// Quota
		r.Get("/api/v1/accounts/{id}/quota", messageH.GetQuota)

		// Messages
		r.Get("/api/v1/accounts/{id}/folders/{folder}/messages", messageH.ListMessages)
		r.Get("/api/v1/messages/{id}", messageH.GetMessage)
		r.Patch("/api/v1/messages/{id}", messageH.UpdateMessage)
		r.Delete("/api/v1/messages/{id}", messageH.DeleteMessage)
		r.Post("/api/v1/messages/send", messageH.SendMessage)
		r.Get("/api/v1/messages/scheduled", messageH.ListScheduledSends)
		r.Delete("/api/v1/messages/scheduled/{id}", messageH.CancelScheduledSend)
		r.Get("/api/v1/messages/{id}/raw", messageH.GetRawMessage)
		r.Post("/api/v1/messages/{id}/forward", messageH.ForwardMessage)
		r.Post("/api/v1/messages/{id}/calendar-reply", messageH.RespondToCalendar)
		r.Get("/api/v1/accounts/{id}/calendar-events", messageH.ListCalendarEvents)

		// Drafts
		r.Post("/api/v1/messages/draft", messageH.SaveDraft)
		r.Put("/api/v1/messages/draft/{id}", messageH.UpdateDraft)
		r.Post("/api/v1/messages/draft/{id}/send", messageH.SendDraft)

		// Threads
		r.Get("/api/v1/accounts/{id}/threads/{threadID}", messageH.GetThread)

		// Attachments
		r.Get("/api/v1/attachments/{id}", attachmentH.GetAttachment)
		r.Get("/api/v1/messages/{id}/attachments", attachmentH.ListAttachments)

		// Contacts
		r.Get("/api/v1/accounts/{id}/contacts", contactH.ListContacts)
		r.Post("/api/v1/accounts/{id}/contacts", contactH.CreateContact)
		r.Patch("/api/v1/accounts/{id}/contacts/{cid}", contactH.UpdateContact)
		r.Delete("/api/v1/accounts/{id}/contacts/{cid}", contactH.DeleteContact)
		r.Post("/api/v1/accounts/{id}/contacts/block", contactH.BlockSender)
		r.Post("/api/v1/accounts/{id}/contacts/import", contactH.ImportContacts)

		// Vacation
		r.Get("/api/v1/accounts/{id}/vacation", vacationH.GetVacation)
		r.Put("/api/v1/accounts/{id}/vacation", vacationH.SetVacation)
		r.Delete("/api/v1/accounts/{id}/vacation", vacationH.DisableVacation)

		// Sieve scripts
		r.Get("/api/v1/accounts/{id}/sieve", sieveH.GetScript)
		r.Put("/api/v1/accounts/{id}/sieve", sieveH.PutScript)
		r.Delete("/api/v1/accounts/{id}/sieve", sieveH.DeleteScript)
		r.Post("/api/v1/accounts/{id}/sieve/validate", sieveH.ValidateScript)

		// Contacts suggest (autocomplete)
		r.Get("/api/v1/accounts/{id}/contacts/suggest", contactH.SuggestContacts)

		// Search
		r.Get("/api/v1/accounts/{id}/search", searchH.Search)

		// Quarantine
		r.Get("/api/v1/accounts/{id}/quarantine", pipelineH.ListQuarantine)
		r.Post("/api/v1/accounts/{id}/quarantine/{mid}/release", pipelineH.ReleaseQuarantine)
		r.Delete("/api/v1/accounts/{id}/quarantine/{mid}", pipelineH.DeleteQuarantine)
	})

	// ═══════════════════════════════════════════════════════════════
	// Admin routes
	//
	// A session only (no credential over HTTP Basic), whose account is set
	// up; then each route requires the capability for its resource/action,
	// read from the caller's grants. Capability names are the seeded
	// capabilities (see middleware/capabilities.go for the taxonomy);
	// superadmin's "*" wildcard satisfies all of them.
	//
	// needs wants the capability at "*". needsOnDomain and needsOnMailbox are
	// for routes acting on the domain or mailbox their {id} names: a grant on
	// that domain (or mailbox) is enough, so a domain admin reaches its own
	// domain and is refused on every other.
	// ═══════════════════════════════════════════════════════════════
	r.Group(func(r chi.Router) {
		r.Use(apiKeys.Authenticate(middleware.JWTMiddleware(jwtService)))
		// Every change through the admin API is recorded, including those the
		// checks below refuse; reads are not, except of the audit log itself.
		r.Use(auditLog("admin.change", middleware.AuditWritesOnly()))
		// A token minted before the account's last credential change is dead.
		r.Use(middleware.RejectStaleSessions(sessionCurrent))
		r.Use(middleware.SetupDone)
		// Same double-submit CSRF guard as the mailbox surface (cookie sessions
		// only; Bearer callers exempt).
		r.Use(middleware.CSRF())
		// #184: same request-body cap as the mailbox surface, covering the admin
		// JSON endpoints (pipeline create/test, custom filters, …).
		r.Use(middleware.MaxBodyBytes(cfg.APIMaxBodyBytes))
		held := heldGrants(grantCache)
		needs := func(capability string) func(http.Handler) http.Handler {
			return middleware.RequireCapability(held, capability, nil)
		}
		onDomain, onMailbox := handlers.DomainScopeOf(db), handlers.MailboxScopeOf(db)
		needsOnDomain := func(capability string) func(http.Handler) http.Handler {
			return middleware.RequireCapability(held, capability, onDomain)
		}
		needsOnMailbox := func(capability string) func(http.Handler) http.Handler {
			return middleware.RequireCapability(held, capability, onMailbox)
		}
		needsOnList := func(capability string) func(http.Handler) http.Handler {
			return middleware.RequireCapabilityOnList(held, capability)
		}
		needsOnAlias := func(capability string) func(http.Handler) http.Handler {
			return middleware.RequireCapability(held, capability, handlers.AliasScopeOf(db))
		}

		// The routes registered with their types, and what this group's middleware
		// adds to each of them (#669).
		admin := endpoint.Routes{Router: r, Registry: registry}.
			Describing(describedAdminSession, describedSetupDone, describedCSRF, describedBodyCap)

		// Roles, capabilities, grants and escalation (#644).
		grantH.Register(admin, handlers.GrantRouteChecks{
			At:           describedCapability(needs),
			SessionOnly:  endpoint.Middleware{Wrap: middleware.SessionOnly, Describes: describedSessionOnly},
			AuthThrottle: endpoint.Middleware{Wrap: authThrottle, Describes: describedAuthThrottle},
		})

		// Dashboard stats — every admin role holds domains:read
		statsH.Register(admin, describedCapability(needs))

		// Domains
		domainH.Register(admin, handlers.DomainRouteChecks{
			At:       describedCapability(needs),
			OnList:   describedCapability(needsOnList),
			OnDomain: describedCapability(needsOnDomain),
		})

		// Accounts (mailboxes). Creating one is POST /api/v1/accounts, the same
		// for everyone; deleting one removes its grants under the grants rules.
		mailboxH.Register(admin, handlers.MailboxRouteChecks{
			OnList: describedCapability(needsOnList), OnMailbox: describedMailboxCapability(needsOnMailbox),
		})
		aliasH.Register(admin, handlers.AliasRouteChecks{
			OnList: describedCapability(needsOnList), OnAlias: describedAliasCapability(needsOnAlias),
		})

		// Pipelines
		pipelineH.Register(admin, handlers.PipelineRouteChecks{At: describedCapability(needs), TestThrottle: endpoint.Middleware{Wrap: pipelineTestThrottle, Describes: describedPipelineTestThrottle}})

		// Pipeline observability (PR5) — analytics funnel + per-message trace.
		// Gated by the dedicated observability:read capability (seeded into the
		// admin and readonly roles; satisfied by superadmin "*"). The existing
		// pipelines/logs read above keeps its pipelines:read gate — its authz is
		// unchanged, only its data source was repointed to message_traces.
		statsH.RegisterPipelineAnalytics(admin, describedCapability(needs)(middleware.CapObservabilityRead))

		// Queue management
		queueH.Register(admin, handlers.QueueRouteChecks{At: describedCapability(needs)})

		// Sender allowlist/blocklist (domain-scoped → domains:* capabilities)
		r.With(needsOnDomain(middleware.CapDomainsRead)).Get("/api/v1/admin/domains/{id}/allowlist", senderRuleH.ListAllowlist)
		r.With(needsOnDomain(middleware.CapDomainsWrite)).Post("/api/v1/admin/domains/{id}/allowlist", senderRuleH.AddToAllowlist)
		r.With(needsOnDomain(middleware.CapDomainsWrite)).Delete("/api/v1/admin/domains/{id}/allowlist/{eid}", senderRuleH.RemoveFromAllowlist)
		r.With(needsOnDomain(middleware.CapDomainsRead)).Get("/api/v1/admin/domains/{id}/blocklist", senderRuleH.ListBlocklist)
		r.With(needsOnDomain(middleware.CapDomainsWrite)).Post("/api/v1/admin/domains/{id}/blocklist", senderRuleH.AddToBlocklist)
		r.With(needsOnDomain(middleware.CapDomainsWrite)).Delete("/api/v1/admin/domains/{id}/blocklist/{eid}", senderRuleH.RemoveFromBlocklist)

		// Recipients the domain's mail is no longer sent to after a hard bounce (#141)
		r.With(needsOnDomain(middleware.CapDomainsRead)).Get("/api/v1/admin/domains/{id}/suppressions", suppressionH.List)
		r.With(needsOnDomain(middleware.CapDomainsWrite)).Delete("/api/v1/admin/domains/{id}/suppressions/{sid}", suppressionH.Delete)

		// MTA-STS policy management (domain-scoped → domains:* capabilities)
		r.With(needsOnDomain(middleware.CapDomainsRead)).Get("/api/v1/admin/domains/{id}/mta-sts", mtastsH.GetPolicy)
		r.With(needsOnDomain(middleware.CapDomainsWrite)).Put("/api/v1/admin/domains/{id}/mta-sts", mtastsH.SetPolicy)
		r.With(needsOnDomain(middleware.CapDomainsWrite)).Delete("/api/v1/admin/domains/{id}/mta-sts", mtastsH.DeletePolicy)

		// Outbound transport: direct MX or an SMTP relay (#143). Setting it takes
		// domains:write at "*": the relay host is dialled from inside the
		// installation and may be private, so only the operator chooses it, never
		// a domain-scoped admin.
		r.With(needsOnDomain(middleware.CapDomainsRead)).Get("/api/v1/admin/domains/{id}/relay", relayH.Get)
		r.With(needs(middleware.CapDomainsWrite)).Put("/api/v1/admin/domains/{id}/relay", relayH.Set)

		// TLS-RPT reports (domain deliverability telemetry → domains:read)
		r.With(needs(middleware.CapDomainsRead)).Get("/api/v1/admin/tls-reports", tlsrptH.ListReports)

		// DKIM and certificates retain their existing capability scopes.
		dkimH.Register(admin, handlers.DKIMRouteChecks{
			At: describedCapability(needs), OnDomain: describedCapability(needsOnDomain),
		})
		certH.Register(admin, handlers.CertificateRouteChecks{At: describedCapability(needs)})

		// Ban management
		banH.Register(admin, describedCapability(needs))

		// Logs
		logH.Register(admin, describedCapability(needs))

		// Audit log (#343): reading it is itself audited, including a read the
		// capability check refuses.
		auditH.Register(admin, handlers.AdminLogRouteChecks{
			At:        describedCapability(needs),
			AuditRead: describedAuditRead(auditLog("audit.read")),
		})

		// Test endpoints (non-production only): "*" itself
		r.With(needs(grants.Wildcard)).Post("/api/v1/admin/test/send", testH.SendTestEmail)
		r.With(needs(grants.Wildcard)).Get("/api/v1/admin/test/verify", testH.VerifyDelivery)
		r.With(needs(grants.Wildcard)).Post("/api/v1/admin/test/probe", testH.ProbeServices)
		r.With(needs(grants.Wildcard)).Post("/api/v1/admin/test/reset", testH.ResetTestData)
		r.With(needs(grants.Wildcard)).Post("/api/v1/admin/test/seed", testH.SeedTestData)
		r.With(needs(grants.Wildcard)).Post("/api/v1/admin/test/snapshot", testH.Snapshot)
		r.With(needs(grants.Wildcard)).Post("/api/v1/admin/test/snapshot/restore", testH.RestoreSnapshot)
	})

	routers := &Routers{Public: r, Recorder: traceRecorder, push: pushSvc, Messages: messageH, Registry: registry}

	// Dedicated internal mTLS handler. Only built when enabled; served on a
	// separate listener whose TLS layer requires a verified gateway client
	// certificate (see cmd/api). RequireClientCert is defense-in-depth on top of
	// that TLS enforcement. It reuses the same handler instances as the public
	// router, so delivery-side SSE/pipeline behavior is identical.
	if cfg.InternalMTLSEnabled {
		ir := chi.NewRouter()
		ir.Use(chimw.RequestID)
		// Same trusted-proxy-aware RealIP as the public listener: never trust a
		// forwarded header from an untrusted peer (see the public middleware above).
		ir.Use(middleware.TrustedRealIP(trustedProxies))
		ir.Use(chimw.Logger)
		ir.Use(chimw.Recoverer)
		ir.Use(metrics.HTTPMetrics)
		ir.Use(middleware.RequireClientCert)
		registerInternal(ir)
		routers.Internal = ir
	}

	return routers
}
