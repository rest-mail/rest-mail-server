package main

import (
	"crypto/tls"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/connlimiter"
	"github.com/restmail/restmail/internal/gateway/queue"
	smtpgw "github.com/restmail/restmail/internal/gateway/smtp"
	"github.com/restmail/restmail/internal/gateway/tlsutil"
)

func main() {
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(logHandler))

	slog.Info("starting SMTP gateway")

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
		// Enable SNI-based cert selection if a cert directory is configured
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

	api := apiclient.New(cfg.APIBaseURL)
	slog.Info("API client configured", "base_url", cfg.APIBaseURL)

	// SMTP gateway needs DB access for the outbound queue worker
	database, err := db.WaitForDB(cfg, 60*time.Second)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
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

	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 20, MaxGlobal: 1000})
	smtpServer := smtpgw.NewServer(cfg.GatewayHostname, api, tlsConfig, database, limiter)
	if err := smtpServer.ListenAndServe(smtpgw.SMTPPorts{
		Inbound:       cfg.SMTPPortInbound,
		Submission:    cfg.SMTPPortSubmission,
		SubmissionTLS: cfg.SMTPPortSubmissionTLS,
	}); err != nil {
		slog.Error("failed to start SMTP server", "error", err)
		os.Exit(1)
	}

	queueWorker := queue.NewWorker(database, cfg.GatewayHostname, cfg.QueueWorkers, cfg.QueuePollInterval)
	if cfg.Environment == "development" || os.Getenv("QUEUE_TLS_INSECURE") == "true" {
		queueWorker.SetTLSInsecure(true)
		slog.Info("queue worker TLS verification disabled", "environment", cfg.Environment)
	}
	queueWorker.Start()

	slog.Info("SMTP gateway started",
		"hostname", cfg.GatewayHostname,
		"ports", []int{cfg.SMTPPortInbound, cfg.SMTPPortSubmission, cfg.SMTPPortSubmissionTLS},
	)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down SMTP gateway...")
	queueWorker.Shutdown()
	smtpServer.Shutdown()
	slog.Info("SMTP gateway stopped")
}
