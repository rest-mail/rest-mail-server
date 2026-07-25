package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	acmeclient "github.com/restmail/restmail/internal/acme"
	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/dns"
	"github.com/restmail/restmail/internal/metrics"
	pipelineobs "github.com/restmail/restmail/internal/metrics/observer"
	"github.com/restmail/restmail/internal/netallow"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters" // register built-in filters via init() + DB-backed factories
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
}

// Close releases background resources owned by the routers — currently the trace
// recorder, whose buffered traces are flushed and whose goroutine is stopped.
// Safe to call on a zero/never-started recorder.
func (rt *Routers) Close() {
	if rt == nil {
		return
	}
	rt.Recorder.Shutdown()
}

// NewRouter creates and configures the chi router with all API routes and
// returns the public handler. It is the backward-compatible entry point for
// callers that do not run a separate internal mTLS listener (tests, tooling).
// The acmeClient parameter is optional; pass nil when ACME is disabled.
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
	healthH := handlers.NewHealthHandler(db, jwtService)
	authH := handlers.NewAuthHandler(db, jwtService, cfg.MasterKey)
	twofaH := handlers.NewTwoFactorHandler(db, cfg.MasterKey, cfg.TOTP2FAEnabled)
	domainH := handlers.NewDomainHandler(db, dnsProvider)
	mailboxH := handlers.NewMailboxHandler(db)
	aliasH := handlers.NewAliasHandler(db)
	broker := handlers.NewSSEBroker()
	eventH := handlers.NewEventHandler(db, broker, jwtService)
	accountH := handlers.NewAccountHandler(db)
	searchH := handlers.NewSearchHandler(db)
	webmailH := handlers.NewWebmailAccountHandler(db)
	attachmentH := handlers.NewAttachmentHandler(db)
	contactH := handlers.NewContactHandler(db)
	vacationH := handlers.NewVacationHandler(db)
	sieveH := handlers.NewSieveHandler(db)
	senderRuleH := handlers.NewSenderRuleHandler(db)
	queueH := handlers.NewQueueHandler(db)
	banH := handlers.NewBanHandler(db)
	logH := handlers.NewLogHandler(db)
	dkimH := handlers.NewDKIMHandler(db, cfg.MasterKey)
	certH := handlers.NewCertificateHandler(db, cfg.MasterKey)
	testH := handlers.NewTestHandler(db, cfg)
	mtastsH := handlers.NewMTASTSHandler(db)
	tlsrptH := handlers.NewTLSReportHandler(db)
	statsH := handlers.NewStatsHandler(db)

	// Register DB-backed filters that need a database connection.
	pipeline.DefaultRegistry.Register("greylist", filters.NewGreylist(db))
	pipeline.DefaultRegistry.Register("vacation", filters.NewVacation(db))
	pipeline.DefaultRegistry.Register("domain_allowlist", filters.NewDomainAllowlist(db))
	pipeline.DefaultRegistry.Register("contact_whitelist", filters.NewContactWhitelist(db))
	pipeline.DefaultRegistry.Register("recipient_check", filters.NewRecipientCheck(db))
	pipeline.DefaultRegistry.Register("sender_verify", filters.NewSenderVerify(db))
	pipeline.DefaultRegistry.Register("dkim_sign", filters.NewDKIMSign(db, cfg.MasterKey))
	pipeline.DefaultRegistry.Register("arc_seal", filters.NewARCSeal(db, cfg.MasterKey))
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
	// (defer) when the scanner is unreachable/errors, overriding the legacy
	// fail-open. No scanner runs by default — these filters are active only when an
	// operator adds them to a pipeline, so the default pipeline is unaffected.
	scannerSecret := cfg.ScannerHMACSecret()
	pipeline.DefaultRegistry.Register("rspamd", filters.NewRspamdWithSecret(scannerSecret))
	pipeline.DefaultRegistry.Register("clamav", filters.NewClamAVWithSecret(scannerSecret))

	// The message-processing engine carries the metrics observer so real
	// inbound/outbound message flow emits pipeline metrics. The pipeline
	// preview/test handler uses a plain engine so admin "try this pipeline"
	// runs never pollute the aggregate metrics.
	pipelineEngine := pipeline.NewEngine(pipeline.DefaultRegistry, slog.Default(), pipelineobs.New())
	previewEngine := pipeline.NewEngine(pipeline.DefaultRegistry, slog.Default())
	// OSI-18: apply the configured fail-open/closed action for unknown or errored
	// filters. The engine defaults to fail-closed (defer); this lets an operator
	// override (e.g. "continue" to restore legacy fail-open).
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
	pipelineH := handlers.NewPipelineHandler(db, previewEngine)
	restmailDeliverAuth := cfg.RestmailDeliverAuth()
	restmailH := handlers.NewRestmailHandler(db, pipelineEngine, traceRecorder, handlers.RestmailTarpitConfig{
		Enabled: cfg.RestmailTarpitEnabled,
		Base:    cfg.RestmailTarpitBase,
		Max:     cfg.RestmailTarpitMax,
	}, handlers.RestmailDeliverAuthConfig{
		Enabled:      restmailDeliverAuth.Enabled,
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
	r.Handle("/metrics", promhttp.Handler())

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
	r.With(authThrottle).Post("/api/v1/auth/login", authH.Login)
	r.Post("/api/v1/auth/logout", authH.Logout)
	r.With(authThrottle).Post("/api/v1/auth/refresh", authH.Refresh)

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

	// ═══════════════════════════════════════════════════════════════
	// Authenticated routes (mail server operations)
	// ═══════════════════════════════════════════════════════════════
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTMiddleware(jwtService))
		// Double-submit CSRF guard for cookie-authenticated browser sessions on
		// state-changing methods; Bearer-token (gateway) callers send no cookie
		// and are transparently exempt (see middleware.CSRF).
		r.Use(middleware.CSRF())
		// #184: cap the request body across the authenticated surface (send,
		// drafts, contacts import, sieve PUT, …) so an unbounded JSON upload
		// cannot exhaust memory. cfg.APIMaxBodyBytes <= 0 makes this a no-op.
		r.Use(middleware.MaxBodyBytes(cfg.APIMaxBodyBytes))

		// Two-factor auth (OSI-19) — an authenticated account manages its own
		// TOTP enrollment. Available to both mailbox and admin tokens (no
		// AdminOnly gate); each endpoint keys on the caller's own claims.
		//
		// #204: the state-changing / code-verifying endpoints share the SAME
		// per-client-IP auth throttle as /auth/login (same limiter instance). Left
		// unthrottled they were bounded only by the 15-minute access-token
		// lifetime, so a holder of a live session could hammer confirm/disable to
		// brute-force TOTP codes — inconsistent with the throttled login path. The
		// status read carries no secret to guess and is left unthrottled so the SPA
		// can poll it freely.
		r.Get("/api/v1/auth/2fa", twofaH.Status)
		r.With(authThrottle).Post("/api/v1/auth/2fa/enroll", twofaH.Enroll)
		r.With(authThrottle).Post("/api/v1/auth/2fa/confirm", twofaH.Confirm)
		r.With(authThrottle).Post("/api/v1/auth/2fa/disable", twofaH.Disable)

		// Linked accounts.
		//
		// LinkAccount and TestConnection each verify a supplied address+password
		// against a mailbox and reveal correctness (200 vs 401). Left unthrottled
		// they are online password-guessing oracles a holder of any valid
		// low-privilege token could hammer to brute-force arbitrary credentials,
		// bypassing the login rate limit entirely. They share the SAME per-client-IP
		// auth throttle as /auth/login and /auth/refresh (same limiter instance), so
		// a wrong guess consumes the same budget as a failed login and cannot be used
		// to sidestep it. The read/list/unlink routes carry no such risk.
		r.Get("/api/v1/accounts", accountH.ListAccounts)
		r.Get("/api/v1/accounts/{id}", accountH.GetAccount)
		r.With(authThrottle).Post("/api/v1/accounts", accountH.LinkAccount)
		r.Delete("/api/v1/accounts/{id}", accountH.UnlinkAccount)
		r.With(authThrottle).Post("/api/v1/accounts/test-connection", accountH.TestConnection)

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
	// AdminOnly gates the whole group (mailbox tokens → 403), then each
	// route requires the capability for its resource/action. Capability
	// names are the seeded admin_capabilities names (see
	// middleware/capabilities.go for the taxonomy); superadmin's "*"
	// wildcard satisfies all of them.
	// ═══════════════════════════════════════════════════════════════
	r.Group(func(r chi.Router) {
		r.Use(middleware.JWTMiddleware(jwtService))
		r.Use(middleware.AdminOnly)
		// Same double-submit CSRF guard as the mailbox surface (cookie sessions
		// only; Bearer callers exempt).
		r.Use(middleware.CSRF())
		// #184: same request-body cap as the mailbox surface, covering the admin
		// JSON endpoints (pipeline create/test, custom filters, …).
		r.Use(middleware.MaxBodyBytes(cfg.APIMaxBodyBytes))
		needs := middleware.RequireCapability

		// Dashboard stats — visible to every admin role
		r.Get("/api/v1/admin/stats", statsH.GetDashboardStats)

		// Admin user management
		adminUserH := handlers.NewAdminUserHandler(db)
		r.With(needs(middleware.CapUsersRead)).Get("/api/v1/admin/admin-users", adminUserH.ListAdminUsers)
		r.With(needs(middleware.CapUsersWrite)).Post("/api/v1/admin/admin-users", adminUserH.CreateAdminUser)
		r.With(needs(middleware.CapUsersRead)).Get("/api/v1/admin/admin-users/{id}", adminUserH.GetAdminUser)
		r.With(needs(middleware.CapUsersWrite)).Put("/api/v1/admin/admin-users/{id}", adminUserH.UpdateAdminUser)
		r.With(needs(middleware.CapUsersDelete)).Delete("/api/v1/admin/admin-users/{id}", adminUserH.DeleteAdminUser)

		// Role and capability management
		r.With(needs(middleware.CapUsersRead)).Get("/api/v1/admin/roles", adminUserH.ListRoles)
		r.With(needs(middleware.CapUsersRead)).Get("/api/v1/admin/capabilities", adminUserH.ListCapabilities)

		// Domains
		r.With(needs(middleware.CapDomainsRead)).Get("/api/v1/admin/domains", domainH.List)
		r.With(needs(middleware.CapDomainsWrite)).Post("/api/v1/admin/domains", domainH.Create)
		r.With(needs(middleware.CapDomainsRead)).Get("/api/v1/admin/domains/{id}", domainH.Get)
		r.With(needs(middleware.CapDomainsWrite)).Patch("/api/v1/admin/domains/{id}", domainH.Update)
		r.With(needs(middleware.CapDomainsDelete)).Delete("/api/v1/admin/domains/{id}", domainH.Delete)
		r.With(needs(middleware.CapDomainsRead)).Get("/api/v1/admin/domains/{id}/dns", domainH.DNSCheck)

		// Mailboxes
		r.With(needs(middleware.CapMailboxesRead)).Get("/api/v1/admin/mailboxes", mailboxH.List)
		r.With(needs(middleware.CapMailboxesWrite)).Post("/api/v1/admin/mailboxes", mailboxH.Create)
		r.With(needs(middleware.CapMailboxesRead)).Get("/api/v1/admin/mailboxes/{id}", mailboxH.Get)
		r.With(needs(middleware.CapMailboxesWrite)).Patch("/api/v1/admin/mailboxes/{id}", mailboxH.Update)
		r.With(needs(middleware.CapMailboxesDel)).Delete("/api/v1/admin/mailboxes/{id}", mailboxH.Delete)

		// Aliases (mailbox routing config → mailboxes:* capabilities)
		r.With(needs(middleware.CapMailboxesRead)).Get("/api/v1/admin/aliases", aliasH.List)
		r.With(needs(middleware.CapMailboxesWrite)).Post("/api/v1/admin/aliases", aliasH.Create)
		r.With(needs(middleware.CapMailboxesRead)).Get("/api/v1/admin/aliases/{id}", aliasH.Get)
		r.With(needs(middleware.CapMailboxesWrite)).Patch("/api/v1/admin/aliases/{id}", aliasH.Update)
		r.With(needs(middleware.CapMailboxesDel)).Delete("/api/v1/admin/aliases/{id}", aliasH.Delete)

		// Webmail accounts (mailbox-scoped → mailboxes:* capabilities)
		r.With(needs(middleware.CapMailboxesRead)).Get("/api/v1/admin/webmail-accounts", webmailH.List)
		r.With(needs(middleware.CapMailboxesWrite)).Post("/api/v1/admin/webmail-accounts", webmailH.Create)
		r.With(needs(middleware.CapMailboxesRead)).Get("/api/v1/admin/webmail-accounts/{id}", webmailH.Get)
		r.With(needs(middleware.CapMailboxesDel)).Delete("/api/v1/admin/webmail-accounts/{id}", webmailH.Delete)

		// Pipelines
		r.With(needs(middleware.CapPipelinesRead)).Get("/api/v1/admin/pipelines", pipelineH.ListPipelines)
		r.With(needs(middleware.CapPipelinesWrite)).Post("/api/v1/admin/pipelines", pipelineH.CreatePipeline)
		r.With(needs(middleware.CapPipelinesWrite)).Patch("/api/v1/admin/pipelines/{id}", pipelineH.UpdatePipeline)
		r.With(needs(middleware.CapPipelinesDel)).Delete("/api/v1/admin/pipelines/{id}", pipelineH.DeletePipeline)
		r.With(needs(middleware.CapPipelinesWrite), pipelineTestThrottle).Post("/api/v1/admin/pipelines/test", pipelineH.TestPipeline)
		r.With(needs(middleware.CapPipelinesWrite), pipelineTestThrottle).Post("/api/v1/admin/pipelines/test-filter", pipelineH.TestFilter)
		r.With(needs(middleware.CapPipelinesRead)).Get("/api/v1/admin/pipelines/logs", pipelineH.ListPipelineLogs)

		// Pipeline observability (PR5) — analytics funnel + per-message trace.
		// Gated by the dedicated observability:read capability (seeded into the
		// admin and readonly roles; satisfied by superadmin "*"). The existing
		// pipelines/logs read above keeps its pipelines:read gate — its authz is
		// unchanged, only its data source was repointed to message_traces.
		r.With(needs(middleware.CapObservabilityRead)).Get("/api/v1/admin/pipelines/analytics", statsH.GetPipelineAnalytics)
		r.With(needs(middleware.CapObservabilityRead)).Get("/api/v1/admin/messages/{id}/trace", pipelineH.GetMessageTrace)

		// Custom filters (pipeline building blocks → pipelines:* capabilities)
		r.With(needs(middleware.CapPipelinesRead)).Get("/api/v1/admin/custom-filters", pipelineH.ListCustomFilters)
		r.With(needs(middleware.CapPipelinesWrite)).Post("/api/v1/admin/custom-filters", pipelineH.CreateCustomFilter)
		r.With(needs(middleware.CapPipelinesWrite)).Post("/api/v1/admin/custom-filters/validate", pipelineH.ValidateCustomFilter)
		r.With(needs(middleware.CapPipelinesRead)).Get("/api/v1/admin/custom-filters/{id}", pipelineH.GetCustomFilter)
		r.With(needs(middleware.CapPipelinesWrite)).Patch("/api/v1/admin/custom-filters/{id}", pipelineH.UpdateCustomFilter)
		r.With(needs(middleware.CapPipelinesDel)).Delete("/api/v1/admin/custom-filters/{id}", pipelineH.DeleteCustomFilter)
		r.With(needs(middleware.CapPipelinesWrite), pipelineTestThrottle).Post("/api/v1/admin/custom-filters/{id}/test", pipelineH.TestCustomFilter)

		// Queue management
		r.With(needs(middleware.CapQueueRead)).Get("/api/v1/admin/queue", queueH.ListQueue)
		r.With(needs(middleware.CapQueueRead)).Get("/api/v1/admin/queue/stats", queueH.QueueStats)
		r.With(needs(middleware.CapQueueManage)).Post("/api/v1/admin/queue/bulk-retry", queueH.BulkRetry)
		r.With(needs(middleware.CapQueueManage)).Post("/api/v1/admin/queue/bulk-bounce", queueH.BulkBounce)
		r.With(needs(middleware.CapQueueManage)).Delete("/api/v1/admin/queue/bulk-delete", queueH.BulkDelete)
		r.With(needs(middleware.CapQueueRead)).Get("/api/v1/admin/queue/{id}", queueH.GetQueueEntry)
		r.With(needs(middleware.CapQueueManage)).Post("/api/v1/admin/queue/{id}/retry", queueH.RetryQueueEntry)
		r.With(needs(middleware.CapQueueManage)).Post("/api/v1/admin/queue/{id}/bounce", queueH.BounceQueueEntry)
		r.With(needs(middleware.CapQueueManage)).Delete("/api/v1/admin/queue/{id}", queueH.DeleteQueueEntry)

		// Sender allowlist/blocklist (domain-scoped → domains:* capabilities)
		r.With(needs(middleware.CapDomainsRead)).Get("/api/v1/admin/domains/{id}/allowlist", senderRuleH.ListAllowlist)
		r.With(needs(middleware.CapDomainsWrite)).Post("/api/v1/admin/domains/{id}/allowlist", senderRuleH.AddToAllowlist)
		r.With(needs(middleware.CapDomainsWrite)).Delete("/api/v1/admin/domains/{id}/allowlist/{eid}", senderRuleH.RemoveFromAllowlist)
		r.With(needs(middleware.CapDomainsRead)).Get("/api/v1/admin/domains/{id}/blocklist", senderRuleH.ListBlocklist)
		r.With(needs(middleware.CapDomainsWrite)).Post("/api/v1/admin/domains/{id}/blocklist", senderRuleH.AddToBlocklist)
		r.With(needs(middleware.CapDomainsWrite)).Delete("/api/v1/admin/domains/{id}/blocklist/{eid}", senderRuleH.RemoveFromBlocklist)

		// MTA-STS policy management (domain-scoped → domains:* capabilities)
		r.With(needs(middleware.CapDomainsRead)).Get("/api/v1/admin/domains/{id}/mta-sts", mtastsH.GetPolicy)
		r.With(needs(middleware.CapDomainsWrite)).Put("/api/v1/admin/domains/{id}/mta-sts", mtastsH.SetPolicy)
		r.With(needs(middleware.CapDomainsWrite)).Delete("/api/v1/admin/domains/{id}/mta-sts", mtastsH.DeletePolicy)

		// TLS-RPT reports (domain deliverability telemetry → domains:read)
		r.With(needs(middleware.CapDomainsRead)).Get("/api/v1/admin/tls-reports", tlsrptH.ListReports)

		// DKIM key management (domain signing material → domains:* capabilities)
		r.With(needs(middleware.CapDomainsRead)).Get("/api/v1/admin/dkim", dkimH.ListKeys)
		r.With(needs(middleware.CapDomainsWrite)).Put("/api/v1/admin/dkim/{id}", dkimH.SetKey)
		r.With(needs(middleware.CapDomainsWrite)).Delete("/api/v1/admin/dkim/{id}", dkimH.DeleteKey)

		// Certificate management (domain TLS material → domains:* capabilities)
		r.With(needs(middleware.CapDomainsRead)).Get("/api/v1/admin/certificates", certH.ListCertificates)
		r.With(needs(middleware.CapDomainsRead)).Get("/api/v1/admin/certificates/{id}", certH.GetCertificate)
		r.With(needs(middleware.CapDomainsWrite)).Post("/api/v1/admin/certificates", certH.CreateCertificate)
		r.With(needs(middleware.CapDomainsWrite)).Delete("/api/v1/admin/certificates/{id}", certH.DeleteCertificate)

		// Ban management
		r.With(needs(middleware.CapBansRead)).Get("/api/v1/admin/bans", banH.ListBans)
		r.With(needs(middleware.CapBansWrite)).Post("/api/v1/admin/bans", banH.CreateBan)
		r.With(needs(middleware.CapBansDelete)).Delete("/api/v1/admin/bans/{id}", banH.DeleteBan)
		r.With(needs(middleware.CapBansDelete)).Delete("/api/v1/admin/bans/ip/{ip}", banH.UnbanIP)

		// Logs
		r.With(needs(middleware.CapMessagesRead)).Get("/api/v1/admin/logs/delivery", logH.DeliveryLog)
		r.With(needs(middleware.CapUsersRead)).Get("/api/v1/admin/logs/activity", logH.ActivityLog)

		// Test endpoints (non-production only)
		r.Post("/api/v1/admin/test/send", testH.SendTestEmail)
		r.Get("/api/v1/admin/test/verify", testH.VerifyDelivery)
		r.Post("/api/v1/admin/test/probe", testH.ProbeServices)
		r.Post("/api/v1/admin/test/reset", testH.ResetTestData)
		r.Post("/api/v1/admin/test/seed", testH.SeedTestData)
		r.Post("/api/v1/admin/test/snapshot", testH.Snapshot)
		r.Post("/api/v1/admin/test/snapshot/restore", testH.RestoreSnapshot)
	})

	routers := &Routers{Public: r, Recorder: traceRecorder}

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
