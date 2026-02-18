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
	"github.com/restmail/restmail/internal/dns"
)

func loadCACert() {
	caCert, err := os.ReadFile("/certs/ca.crt")
	if err != nil {
		slog.Info("no custom CA cert found, using system defaults")
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
	slog.Info("loaded custom CA certificate", "path", "/certs/ca.crt")
}

func main() {
	loadCACert()

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

	// Create router
	router := api.NewRouter(database, jwtService, cfg, dnsProvider, acmeClientPtr)

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

	// Create HTTP server
	srv := &http.Server{
		Addr:         cfg.APIAddr(),
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		slog.Info("API server listening", "addr", cfg.APIAddr())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server...")
	if acmeManager != nil {
		acmeManager.Shutdown()
	}
	quotaReconciler.Shutdown()
	digestWorker.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
	}

	slog.Info("server stopped")
}
