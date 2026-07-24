package main

import (
	"crypto/tls"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	pop3srv "github.com/rest-mail/go-pop3"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/bancheck"
	"github.com/restmail/restmail/internal/gateway/connlimiter"
	"github.com/restmail/restmail/internal/gateway/metricsrv"
	"github.com/restmail/restmail/internal/gateway/pop3"
	"github.com/restmail/restmail/internal/gateway/tlsutil"
)

func main() {
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(logHandler))

	slog.Info("starting POP3 gateway")

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

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

	// Secure-by-construction (production-only): refuse to boot when the POP3
	// listeners would serve plaintext where they shouldn't — no TLS keypair to
	// advertise STLS on 110 or bind implicit TLS on 995 (which would also accept
	// USER/PASS before TLS). In development/test (the testbed & e2e default) each
	// finding only warns and boot proceeds unchanged.
	if err := cfg.ValidateListenerSecurity(config.RolePOP3Gateway); err != nil {
		slog.Error("insecure listener configuration refused", "error", err)
		os.Exit(1)
	}

	// Prometheus /metrics endpoint for this gateway process. Serves the process
	// registry the connection limiter increments into.
	metricsServer := metricsrv.New(cfg.POP3MetricsPort, cfg.MetricsAllowedCIDRs(), cfg.ProxyProtocolTrustedCIDRs)
	metricsServer.Start()

	var tlsConfig *tls.Config
	if cfg.TLSCertPath != "" && cfg.TLSKeyPath != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertPath, cfg.TLSKeyPath)
		if err != nil {
			slog.Error("failed to load TLS certificate", "error", err)
			os.Exit(1)
		}
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		if cfg.TLSCertDir != "" {
			loader := tlsutil.NewSNICertLoader(cfg.TLSCertDir, &cert)
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

	// Open database for DB-backed SNI certificate loading
	database, err := db.WaitForDB(cfg, 30*time.Second)
	if err != nil {
		slog.Warn("database not available for DB-backed SNI", "error", err)
	}

	// Wire DB-backed certificate loading for SNI
	if tlsConfig != nil && database != nil {
		fallbackCert := &tlsConfig.Certificates[0]
		dbCertLoader := tlsutil.NewDBCertLoader(database, cfg.MasterKey, fallbackCert)
		prevGetCert := tlsConfig.GetCertificate
		tlsConfig.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if c, err := dbCertLoader.GetCertificate(hello); c != nil && err == nil {
				return c, nil
			}
			if prevGetCert != nil {
				return prevGetCert(hello)
			}
			return nil, nil
		}
		slog.Info("DB-backed SNI certificate loading enabled")
	}

	// Internal mTLS (gateway → API machine auth): when enabled, the two
	// tokenless machine routes (recipient check + inbound delivery) go to the
	// API's dedicated internal listener (API_INTERNAL_BASE_URL) presenting the
	// gateway client certificate; all token/credential routes keep using the
	// public API_BASE_URL. Disabled → single plain client, unchanged.
	var apiOpts []apiclient.Option
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
		slog.Info("internal mTLS enabled — machine routes use the internal listener with the gateway client certificate", "internal_base_url", cfg.APIInternalBaseURL)
	}
	// OSI-7: size-aware deadline for message-carrying API calls — GetRawMessage
	// streams a full stored message back to POP3 clients, so a large message must
	// not be stranded by a fixed 30 s timeout. Bounded by the configured max size.
	msgDeadline := cfg.InternalDeliveryDeadline(cfg.SMTPMaxMessageSize)
	apiOpts = append(apiOpts, apiclient.WithMessageDeadline(msgDeadline))
	api := apiclient.New(cfg.APIBaseURL, apiOpts...)
	slog.Info("API client configured", "base_url", cfg.APIBaseURL, "message_deadline", msgDeadline.String())

	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 20, MaxGlobal: 1000})
	limiter.SetProtocol("pop3")
	bancheck.Wire(limiter, database, "pop3")
	// The shared connlimiter satisfies the library's structural Limiter interface;
	// the rest-mail Backend adapter maps API responses onto the library's types.
	pop3Server := pop3srv.NewServer(pop3.NewBackend(api), tlsConfig, limiter)
	if err := pop3Server.ListenAndServe(pop3srv.Ports{
		POP3:    cfg.POP3Port,
		POP3TLS: cfg.POP3TLSPort,
	}); err != nil {
		slog.Error("failed to start POP3 server", "error", err)
		os.Exit(1)
	}

	slog.Info("POP3 gateway started",
		"hostname", cfg.GatewayHostname,
		"ports", []int{cfg.POP3Port, cfg.POP3TLSPort},
	)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down POP3 gateway...")
	pop3Server.Shutdown()
	metricsServer.Shutdown()
	slog.Info("POP3 gateway stopped")
}
