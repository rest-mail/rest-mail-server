package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	acmeclient "github.com/restmail/restmail/internal/acme"
	"github.com/restmail/restmail/internal/api"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/digest"
	"github.com/restmail/restmail/internal/dmarcreport"
	"github.com/restmail/restmail/internal/dns"
	"github.com/restmail/restmail/internal/greylist"
	"github.com/restmail/restmail/internal/rollup"
	"github.com/restmail/restmail/internal/trace"
)

// loadCACert adds the PEM CA bundle at path to the API's outbound HTTP client
// trust store (the default transport's RootCAs), on top of the system roots, so
// the API trusts TLS peers issued by an operator/testbed CA (e.g. MTA-STS policy
// fetches over the simulated internet). The path is config-driven
// (config.TrustedCACertPath / TRUSTED_CA_CERT) and defaults to the historical
// "/certs/ca.crt", so existing deployments are byte-for-byte unchanged. An empty
// path skips the extra trust; a missing/unreadable file is not fatal — the API
// falls back to the system roots. This is the general outbound trust anchor, NOT
// the internal-mTLS CA (that is loaded from INTERNAL_MTLS_CA_CERT at point of use).
func loadCACert(path string) {
	if path == "" {
		return
	}
	caCert, err := os.ReadFile(path)
	if err != nil {
		slog.Info("no custom CA cert found, using system defaults", "path", path)
		return
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
}

func main() {
	// Configure structured JSON logging
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(logHandler))

	slog.Info("starting restmail API server")

	// Load config
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Trust the operator/testbed CA for the API's outbound HTTP client. The path
	// is config-driven and defaults to the historical /certs/ca.crt, so the
	// testbed is unchanged; empty or missing → system roots only.
	loadCACert(cfg.TrustedCACertPath)

	// Set log level
	switch cfg.LogLevel {
	case "debug":
		logHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
		slog.SetDefault(slog.New(logHandler))
	case "warn":
		logHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})
		slog.SetDefault(slog.New(logHandler))
	case "error":
		logHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})
		slog.SetDefault(slog.New(logHandler))
	}

	// Secure-by-construction (production-only): the API always serves plaintext
	// HTTP (TLS is terminated by a front proxy), so in production refuse to boot
	// unless the operator acknowledges that proxy with
	// API_TLS_TERMINATED_BY_PROXY=true; also refuse a cleartext DB link
	// (DB_SSLMODE=disable) unless acknowledged. In development/test (the testbed &
	// e2e default) each finding only warns and boot proceeds unchanged.
	if err := cfg.ValidateListenerSecurity(config.RoleAPI); err != nil {
		slog.Error("insecure listener configuration refused", "error", err)
		os.Exit(1)
	}

	// Connect to database (with retry)
	database, err := db.WaitForDB(cfg, 60*time.Second)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}

	// Run auto-migration
	if err := db.AutoMigrate(database); err != nil {
		slog.Error("failed to run database migration", "error", err)
		os.Exit(1)
	}

	// Create JWT service
	jwtService := auth.NewJWTService(cfg.JWTSecret, cfg.JWTAccessExpiry, cfg.JWTRefreshExpiry)

	// Create ACME client (if enabled)
	var acmeManager *acmeclient.Manager
	var acmeClientPtr *acmeclient.Client
	if cfg.ACMEEnabled {
		if cfg.ACMEEmail == "" {
			slog.Error("ACME_EMAIL is required when ACME is enabled")
			os.Exit(1)
		}
		acmeClientPtr = acmeclient.NewClient(acmeclient.ClientConfig{
			DB:        database,
			MasterKey: cfg.MasterKey,
			Email:     cfg.ACMEEmail,
			Directory: cfg.ACMEDirectory,
			Staging:   cfg.ACMEStaging,
		})
		acmeManager = acmeclient.NewManager(acmeClientPtr, acmeclient.DefaultCheckInterval)
		slog.Info("ACME certificate auto-provisioning enabled",
			"email", cfg.ACMEEmail,
			"staging", cfg.ACMEStaging,
		)
	}

	// Create DNS provider
	dnsProvider, err := dns.NewProvider(cfg.DNSProvider)
	if err != nil {
		slog.Error("failed to create DNS provider", "error", err)
		os.Exit(1)
	}
	slog.Info("DNS provider initialized", "provider", cfg.DNSProvider)

	// Create routers. When internal mTLS is enabled, routers.Internal carries
	// the gateway-facing routes for the dedicated mTLS listener and they are
	// withheld from the public handler; otherwise those routes live on Public
	// (tokenless), preserving pre-mTLS behavior.
	routers := api.NewRouters(database, jwtService, cfg, dnsProvider, acmeClientPtr)
	router := routers.Public

	// OSI-7: the whole-request read/write budget must accommodate a max-size
	// inbound delivery body (POST /api/v1/messages/deliver, POST /restmail/messages)
	// transferred at the internal floor throughput — a fixed 15 s stranded a large
	// but admin-permitted message. It is derived from SMTP_MAX_MESSAGE_SIZE and the
	// floor rate, so it is BOUNDED (a finite function of the configured max, never
	// infinite). ReadHeaderTimeout stays tight below so header-slowloris protection
	// is unchanged; only the body-transfer window is widened. For a normal
	// deployment (10 MiB max) this is the 30 s floor.
	deliverBudget := cfg.InternalDeliveryDeadline(cfg.SMTPMaxMessageSize)
	slog.Info("API request body budget configured (OSI-7)",
		"max_message_size", cfg.SMTPMaxMessageSize,
		"read_write_timeout", deliverBudget.String(),
		"header_timeout", (15 * time.Second).String(),
	)

	// Build the internal mTLS listener up front (before serving) so a
	// half-configured deployment fails closed at startup rather than after the
	// public listener is already accepting traffic.
	var internalSrv *http.Server
	if cfg.InternalMTLSEnabled {
		internalTLS, err := cfg.InternalMTLSServerTLS()
		if err != nil {
			slog.Error("internal mTLS enabled but server TLS config is invalid", "error", err)
			os.Exit(1)
		}
		internalSrv = &http.Server{
			Addr:      cfg.InternalMTLSAddr(),
			Handler:   routers.Internal,
			TLSConfig: internalTLS,
			// Tight header read (slowloris on headers) with a size-aware whole-request
			// budget so a max-size delivery body is not stranded (OSI-7). The internal
			// listener carries the gateway→API deliver route, so it especially needs
			// the larger body window.
			ReadHeaderTimeout: 15 * time.Second,
			ReadTimeout:       deliverBudget,
			WriteTimeout:      deliverBudget,
			IdleTimeout:       60 * time.Second,
		}
		slog.Info("internal mTLS listener configured (gateway-facing routes require a verified client certificate)",
			"addr", cfg.InternalMTLSAddr(),
		)
	}

	// Start ACME renewal manager (if enabled)
	if acmeManager != nil {
		acmeManager.Start()
	}

	// Start quarantine digest worker
	digestInterval := 24 * time.Hour
	if cfg.Environment == "development" {
		digestInterval = 1 * time.Hour
	}
	digestWorker := digest.NewWorker(database, digestInterval)
	digestWorker.Start()

	// Start quota reconciler (runs every 6 hours)
	quotaReconciler := digest.NewQuotaReconciler(database, 6*time.Hour)
	quotaReconciler.Start()

	// Start DMARC aggregate (rua) reporter — emits RFC 7489 reports for the
	// per-message evaluations captured by dmarc_check.
	dmarcReporter := dmarcreport.NewReporter(database, digestInterval, cfg.GatewayHostname)
	dmarcReporter.Start()

	// Start the rollup worker: it snapshots the always-on, 100%-accurate Prometheus
	// pipeline counters into time-bucketed DB aggregates (pipeline_rollups), giving
	// the in-app dashboard durable windowed history without an external Prometheus.
	// The inbound funnel counters live in this API process's registry and are fully
	// covered; the outbound queue counters run in the smtp-gateway process (no
	// /metrics endpoint there yet — the known gateway-scraping gap) so they are not
	// rolled up yet. Aggregate accuracy is independent of trace sampling/pruning.
	// Multi-resolution downsampling keeps the analytics DB bounded: fine rollups
	// are kept for ROLLUP_DETAILED_RETENTION (default 7 days), then coarse periods
	// aged past that window are condensed to ROLLUP_COARSE_RESOLUTION (default
	// daily) rows and their fine rows removed — an ~288× reduction with no loss of
	// the aggregate signal. Idempotent and crash-safe (per-period transactions).
	rollupWorker := rollup.NewWorker(database, cfg.RollupInterval,
		rollup.WithDownsampling(
			cfg.RollupDetailedRetention(),
			cfg.RollupCoarseResolution(),
			cfg.RollupCoarseRetention(),
			cfg.RollupDownsampleInterval(),
		))
	rollupWorker.Start()

	// Start the trace retention pruner (hourly): deletes per-message traces past
	// their expires_at horizon and enforces the TRACE_MAX_ROWS backstop. It never
	// touches pipeline_rollups — aggregate history is long-lived.
	tracePruner := trace.NewPruner(database, time.Hour, cfg.TraceMaxRows)
	tracePruner.Start()

	// Start the greylist purger (hourly): deletes greylist_entries rows past their
	// ttl_days horizon so the triple table stays bounded rather than growing with
	// every new sender/recipient/IP combination seen.
	greylistPurger := greylist.NewPurger(database, time.Hour)
	greylistPurger.Start()

	// Create HTTP server
	srv := &http.Server{
		Addr:    cfg.APIAddr(),
		Handler: router,
		// Tight header read (slowloris on headers) with a size-aware whole-request
		// budget so a max-size delivery body (POST /restmail/messages, or the deliver
		// route when internal mTLS is off) is not stranded (OSI-7). Non-delivery
		// routes carry tiny bodies, so the wider window only ever benefits large mail.
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       deliverBudget,
		WriteTimeout:      deliverBudget,
		IdleTimeout:       60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		slog.Info("API server listening", "addr", cfg.APIAddr())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Start the internal mTLS listener in a goroutine. Certificates come from
	// the server's TLSConfig, so the cert/key file args to ListenAndServeTLS are
	// empty.
	if internalSrv != nil {
		go func() {
			slog.Info("internal mTLS server listening", "addr", internalSrv.Addr)
			if err := internalSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				slog.Error("internal mTLS server error", "error", err)
				os.Exit(1)
			}
		}()
	}

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server...")
	if acmeManager != nil {
		acmeManager.Shutdown()
	}
	quotaReconciler.Shutdown()
	dmarcReporter.Shutdown()
	// Stop the rollup worker (takes a final snapshot to capture the partial
	// interval) and the trace pruner.
	rollupWorker.Shutdown()
	tracePruner.Shutdown()
	greylistPurger.Shutdown()
	digestWorker.Shutdown()
	// Flush buffered per-message traces and stop the recorder goroutine.
	routers.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
	}
	if internalSrv != nil {
		if err := internalSrv.Shutdown(ctx); err != nil {
			slog.Error("internal mTLS server forced to shutdown", "error", err)
		}
	}

	slog.Info("server stopped")
}
