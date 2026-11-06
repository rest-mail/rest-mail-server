package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/dane"
	"github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/bancheck"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	"github.com/rest-mail/rest-mail-server/internal/gateway/metricsrv"
	"github.com/rest-mail/rest-mail-server/internal/gateway/queue"
	smtpgw "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"github.com/rest-mail/rest-mail-server/internal/gateway/tlsutil"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/metrics"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/requestid"
	"github.com/rest-mail/rest-mail-server/internal/sendlimits"
	"github.com/rest-mail/rest-mail-server/internal/srs"
)

// loadCACert adds the PEM CA bundle at path to the gateway's outbound HTTP
// client trust store (the default transport's RootCAs), on top of the system
// roots, so the gateway trusts TLS peers issued by an operator/testbed CA. The
// path is config-driven (config.TrustedCACertPath / TRUSTED_CA_CERT) and defaults
// to the historical "/certs/ca.crt", so existing deployments are byte-for-byte
// unchanged. An empty path skips the extra trust; a missing/unreadable file is
// not fatal — the gateway falls back to the system roots. The returned pool
// also configures the queue's guarded RESTMAIL transports.
func loadCACert(path string) *x509.CertPool {
	if path == "" {
		return nil
	}
	caCert, err := os.ReadFile(path)
	if err != nil {
		slog.Info("no custom CA cert found, using system defaults", "path", path)
		return nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	pool.AppendCertsFromPEM(caCert)
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
		RootCAs: pool,
	}
	slog.Info("loaded custom CA certificate", "path", path)
	return pool
}

func main() {
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(requestid.NewHandler(logHandler)))

	slog.Info("starting SMTP gateway")

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	// Every message this process parses is held to the inbound limits (#1647, #1641).
	rmime.SetLimits(cfg.MIMELimits())

	// Trust the operator/testbed CA for the gateway's outbound HTTP client. The
	// path is config-driven and defaults to the historical /certs/ca.crt, so the
	// testbed is unchanged; empty or missing → system roots only.
	restmailRoots := loadCACert(cfg.TrustedCACertPath)

	switch cfg.LogLevel {
	case "debug":
		logHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
		slog.SetDefault(slog.New(requestid.NewHandler(logHandler)))
	case "warn":
		logHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})
		slog.SetDefault(slog.New(requestid.NewHandler(logHandler)))
	case "error":
		logHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})
		slog.SetDefault(slog.New(requestid.NewHandler(logHandler)))
	}

	// Secure-by-construction (production-only): refuse to boot when a listener
	// would serve plaintext where it shouldn't — no TLS keypair to advertise
	// STARTTLS on 25 or bind implicit TLS on 465 (which would also accept AUTH
	// before TLS) — or when an insecure knob is set (QUEUE_TLS_INSECURE). In
	// development/test (the testbed & e2e default) each finding only warns and boot
	// proceeds unchanged.
	if err := cfg.ValidateListenerSecurity(config.RoleSMTPGateway); err != nil {
		slog.Error("insecure listener configuration refused", "error", err)
		os.Exit(1)
	}

	// Prometheus /metrics endpoint for this gateway process. Serves the process
	// registry the outbound queue worker and connection limiter increment into.
	metricsServer := metricsrv.New(cfg.SMTPMetricsPort, cfg.MetricsAllowedCIDRs(), cfg.ProxyProtocolTrustedCIDRs)
	metricsServer.Start()

	var tlsConfig *tls.Config
	// Kept so the database, once open, can tell the file loader which names this
	// server is responsible for.
	var sniLoader *tlsutil.SNICertLoader
	var primaryLoader *tlsutil.FileCertLoader
	if cfg.TLSCertPath != "" && cfg.TLSKeyPath != "" {
		primaryLoader, err = tlsutil.NewFileCertLoader(cfg.TLSCertPath, cfg.TLSKeyPath)
		if err != nil {
			slog.Error("failed to load TLS certificate", "error", err)
			os.Exit(1)
		}
		tlsConfig = &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: primaryLoader.GetCertificate,
		}
		// Enable SNI-based cert selection if a cert directory is configured
		if cfg.TLSCertDir != "" {
			loader := tlsutil.NewSNICertLoader(cfg.TLSCertDir, primaryLoader.Current())
			loader.SetFallbackCurrent(primaryLoader.Current)
			loader.SetPrimaryPaths(cfg.TLSCertPath, cfg.TLSKeyPath)
			sniLoader = loader
			tlsConfig.GetCertificate = loader.GetCertificate
			slog.Info("TLS configured with SNI", "cert", cfg.TLSCertPath, "cert_dir", cfg.TLSCertDir)
			if err := loader.StartWatching(); err != nil {
				slog.Warn("SNI file watcher failed to start", "error", err)
			} else {
				defer loader.Stop()
			}
		} else {
			slog.Info("TLS configured", "cert", cfg.TLSCertPath)
		}
	} else {
		slog.Warn("no TLS certificate configured — running without TLS")
	}

	// Internal mTLS (gateway → API service identity, #1586): the SMTP gateway
	// is two services. Inbound delivery (recipient check, delivery) presents
	// the rest-mail-smtp-delivery certificate (INTERNAL_MTLS_CLIENT_*); a mail
	// client signing in at submission gets a connection session opened with
	// the rest-mail-smtp-submission certificate (INTERNAL_MTLS_SUBMISSION_*).
	// Both go to the API's internal listener (API_INTERNAL_BASE_URL).
	if !cfg.InternalMTLSEnabled {
		slog.Error("the SMTP gateway needs internal mTLS (INTERNAL_MTLS_ENABLED=true, with its delivery and submission client certificates): mail clients' sessions with the API are opened on the internal listener")
		os.Exit(1)
	}
	var apiOpts, submissionOpts []apiclient.Option
	if cfg.InternalMTLSEnabled {
		if cfg.APIInternalBaseURL == "" {
			slog.Error("internal mTLS enabled but API_INTERNAL_BASE_URL is not set (must point at the API's internal mTLS listener, e.g. https://api:8443)")
			os.Exit(1)
		}
		clientTLS, err := cfg.InternalMTLSClientTLS()
		if err != nil {
			slog.Error("internal mTLS enabled but client TLS config is invalid", "error", err)
			os.Exit(1)
		}
		apiOpts = append(apiOpts, apiclient.WithInternalMTLS(cfg.APIInternalBaseURL, clientTLS))
		submissionTLS, err := cfg.InternalMTLSSubmissionTLS()
		if err != nil {
			slog.Error("internal mTLS enabled but the submission client TLS config is invalid (INTERNAL_MTLS_SUBMISSION_CERT/KEY)", "error", err)
			os.Exit(1)
		}
		submissionOpts = append(submissionOpts, apiclient.WithInternalMTLS(cfg.APIInternalBaseURL, submissionTLS))
		slog.Info("internal mTLS enabled — machine routes use the internal listener with the gateway client certificate", "internal_base_url", cfg.APIInternalBaseURL)
	}
	// OSI-7: size-aware deadline for message-carrying API calls (DeliverMessage
	// uploads a full inbound message) so a large-but-permitted message is not
	// stranded by a fixed 30 s timeout. Bounded — derived from the configured max
	// size and the internal floor rate.
	msgDeadline := cfg.InternalDeliveryDeadline(cfg.SMTPMaxMessageSize)
	apiOpts = append(apiOpts, apiclient.WithMessageDeadline(msgDeadline))
	api := smtpgw.Clients{
		Delivery:   apiclient.New(cfg.APIBaseURL, apiOpts...),
		Submission: apiclient.New(cfg.APIBaseURL, submissionOpts...),
	}
	slog.Info("API client configured", "base_url", cfg.APIBaseURL, "message_deadline", msgDeadline.String())

	// SMTP gateway needs DB access for the outbound queue worker
	database, err := db.WaitForDB(cfg, 60*time.Second)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	// Pool and connection-error series on this process's /metrics (#1579).
	if err := metrics.InstrumentDB(database); err != nil {
		slog.Warn("database metrics not registered", "error", err)
	}

	// The operator override replaces the stored server ceiling here as it does
	// in the API: SenderAuthorized reads grants in this process. It is checked
	// against the capability vocabulary before mail listeners open, so a typo
	// stops the gateway rather than widening the ceiling.
	serverCapabilities, serverCapabilitiesSet := os.LookupEnv(grants.ServerCapabilitiesEnv)
	if err := grants.ConfigureServerOverride(database, serverCapabilities, serverCapabilitiesSet); err != nil {
		slog.Error("invalid server capability override", "error", err)
		os.Exit(1)
	}

	// Wire DB-backed certificate loading for SNI
	if tlsConfig != nil && database != nil {
		dbCertLoader := tlsutil.NewDBCertLoader(database, cfg.MasterKey, primaryLoader.Current())
		dbCertLoader.SetFallbackCurrent(primaryLoader.Current)
		if sniLoader != nil {
			sniLoader.SetHostedNames(tlsutil.HostedInDatabase(database))
		}
		// The database is asked first, then the certificate directory. A name
		// neither of them serves is refused; a domain served here whose certificate
		// is missing or unusable ends the handshake rather than falling through to
		// a certificate issued for a different name (issue #290).
		tlsConfig.GetCertificate = tlsutil.Chain(dbCertLoader.GetCertificate, tlsConfig.GetCertificate)
		slog.Info("DB-backed SNI certificate loading enabled")
	}

	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 20, MaxGlobal: 1000})
	limiter.SetProtocol("smtp")
	bancheck.Wire(limiter, database, "smtp")
	smtpServer := smtpgw.NewServer(cfg.GatewayHostname, api, tlsConfig, smtpgw.NewLimitedStore(database, cfg.MasterKey, sendlimits.FromConfig(cfg.SendLimits)), limiter)
	// #775: forwards leave with an SRS address as their sender, and a bounce to one is
	// checked at RCPT. ValidateListenerSecurity refuses a production gateway without
	// a secret; elsewhere forwards keep their sender and such a bounce is refused.
	srsSigner := srs.New(cfg.SRSSecret, cfg.SRSPreviousSecret)
	smtpServer.SetSRS(srsSigner, cfg.BounceDSNMaxPerRecipient, cfg.BounceDSNRateWindow)
	smtpServer.SetMaxMessageSize(cfg.SMTPMaxMessageSize)
	slog.Info("SMTP max message size configured", "max_message_size", cfg.SMTPMaxMessageSize)
	if warn := cfg.SMTPMaxMessageSizeWarning(); warn != "" {
		slog.Warn(warn, "max_message_size", cfg.SMTPMaxMessageSize)
	}
	smtpServer.SetTransferRatePolicy(cfg.SMTPMinTransferRate, cfg.SMTPTransferGracePeriod, cfg.SMTPTransferStallTimeout)
	rateFloor := "disabled"
	if cfg.SMTPMinTransferRate > 0 {
		rateFloor = fmt.Sprintf("%d bytes/sec", cfg.SMTPMinTransferRate)
	}
	slog.Info("SMTP transfer-rate policy configured: message-body transfers must keep data flowing (slow senders get the grace period; a transfer below the rate floor or stalled past the timeout is dropped)",
		"rate_floor", rateFloor,
		"grace_period", cfg.SMTPTransferGracePeriod.String(),
		"stall_timeout", cfg.SMTPTransferStallTimeout.String(),
	)
	smtpServer.SetTarpitPolicy(cfg.SMTPTarpitEnabled, cfg.SMTPTarpitBase, cfg.SMTPTarpitSoftLimit, cfg.SMTPTarpitMax)
	if cfg.SMTPTarpitEnabled {
		slog.Info("SMTP tarpit configured: an abusive session (invalid inbound RCPT / AUTH failures) is delayed with an escalating, capped sleep once its rejection count crosses the soft limit; legitimate senders are unaffected",
			"base", cfg.SMTPTarpitBase.String(),
			"soft_limit", cfg.SMTPTarpitSoftLimit,
			"max", cfg.SMTPTarpitMax.String(),
		)
	} else {
		slog.Info("SMTP tarpit disabled")
	}
	if len(cfg.ProxyProtocolTrustedCIDRs) > 0 {
		smtpServer.SetProxyProtocol(cfg.ProxyProtocolTrustedCIDRs)
		slog.Info("PROXY protocol configured", "trusted_cidrs", cfg.ProxyProtocolTrustedCIDRs)
	}
	// No Submission field: cleartext submission on 587 does not exist. 465 is implicit
	// TLS, and 25 is inbound relay, which refuses a transaction until STARTTLS.
	if err := smtpServer.ListenAndServe(smtpgw.SMTPPorts{
		Inbound:       cfg.SMTPPortInbound,
		SubmissionTLS: cfg.SMTPPortSubmissionTLS,
	}); err != nil {
		slog.Error("failed to start SMTP server", "error", err)
		os.Exit(1)
	}

	queueWorker := queue.NewWorker(database, cfg.GatewayHostname, cfg.QueueWorkers, cfg.QueuePollInterval)
	queueWorker.EnableTestReset(cfg)
	queueWorker.SetRESTMAILRootCAs(restmailRoots)
	// #171: the worker DKIM-signs outbound mail that arrived unsigned (SMTP
	// submission), which needs the key to decrypt the domain's at-rest DKIM key.
	queueWorker.SetMasterKey(cfg.MasterKey)
	queueWorker.SetSRS(srsSigner)
	queueWorker.SetMTASTSEnforce(cfg.MTASTSEnforce)
	// #725: DANE needs a validating resolver on this host; a wrong address stops the
	// gateway rather than silently sending without DANE.
	if cfg.DANEResolver != "" {
		resolver, err := dane.NewResolver(cfg.DANEResolver, 5*time.Second)
		if err != nil {
			slog.Error("DANE_RESOLVER refused", "error", err)
			os.Exit(1)
		}
		queueWorker.SetDANEResolver(resolver)
		slog.Info("DANE for outbound SMTP is on", "resolver", cfg.DANEResolver)
	}
	queueWorker.SetBounceRateLimit(cfg.BounceDSNMaxPerRecipient, cfg.BounceDSNRateWindow)
	queueWorker.SetSuppressionTTL(cfg.SuppressionTTL)
	// Each SMTP step has its own timer, and the worker holding a row refreshes it
	// while it runs, so reclaim only has to outlast the longest step (#714). The
	// RESTMAIL hop's HTTPS requests keep a size-aware budget (OSI-7).
	queueReclaim := cfg.StaleDeliveringReclaim()
	queueWorker.SetSMTPTimeouts(cfg.QueueSMTPTimeouts)
	queueWorker.SetDeliveryDeadline(cfg.InternalDeliveryDeadline, queueReclaim)
	slog.Info("queue delivery timing configured",
		"smtp_greeting_timeout", cfg.QueueSMTPTimeouts.Greeting.String(),
		"smtp_mail_timeout", cfg.QueueSMTPTimeouts.Mail.String(),
		"smtp_rcpt_timeout", cfg.QueueSMTPTimeouts.Rcpt.String(),
		"smtp_data_timeout", cfg.QueueSMTPTimeouts.Data.String(),
		"smtp_data_block_timeout", cfg.QueueSMTPTimeouts.DataBlock.String(),
		"smtp_final_reply_timeout", cfg.QueueSMTPTimeouts.FinalReply.String(),
		"restmail_max_size_budget", cfg.InternalDeliveryDeadline(cfg.SMTPMaxMessageSize).String(),
		"reclaim_interval", queueReclaim.String(),
	)
	if cfg.Environment == "development" || os.Getenv("QUEUE_TLS_INSECURE") == "true" {
		queueWorker.SetTLSInsecure(true)
		// MTA-STS enforce requires real certificate verification, which is
		// disabled here; downgrade enforcement so dev/test delivery still works.
		queueWorker.SetMTASTSEnforce(false)
		slog.Info("queue worker SMTP TLS verification disabled; RESTMAIL TLS verified; MTA-STS enforcement off", "environment", cfg.Environment)
	}
	// Outbound SSRF guard (#167): by default the worker refuses to dial
	// non-public (loopback/link-local/private/metadata) MX or RESTMAIL endpoint
	// addresses. A dev/testbed delivers between containers on a private bridge
	// network, so opt in when in development or when explicitly requested. This
	// mirrors the QUEUE_TLS_INSECURE dev auto-enable above; production stays deny.
	if cfg.OutboundAllowPrivateDestinations || cfg.Environment == "development" {
		queueWorker.SetAllowPrivateDestinations(true)
		slog.Info("queue worker outbound SSRF guard: private destinations allowed",
			"environment", cfg.Environment, "explicit", cfg.OutboundAllowPrivateDestinations)
	}
	queueWorker.Start()

	slog.Info("SMTP gateway started",
		"hostname", cfg.GatewayHostname,
		"ports", []int{cfg.SMTPPortInbound, cfg.SMTPPortSubmissionTLS},
	)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down SMTP gateway...")
	queueWorker.Shutdown()
	smtpServer.Shutdown()
	metricsServer.Shutdown()
	slog.Info("SMTP gateway stopped")
}
