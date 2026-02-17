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
	"github.com/restmail/restmail/internal/gateway/imap"
	"github.com/restmail/restmail/internal/gateway/pop3"
	"github.com/restmail/restmail/internal/gateway/queue"
	smtpgw "github.com/restmail/restmail/internal/gateway/smtp"
)

func main() {
	// Configure structured JSON logging
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(logHandler))

	slog.Info("starting restmail protocol gateway")

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

	// Load TLS certificates
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
		slog.Info("TLS configured", "cert", cfg.TLSCertPath)
	} else {
		slog.Warn("no TLS certificate configured — running without TLS")
	}

	// Create API client
	api := apiclient.New(cfg.APIBaseURL)
	slog.Info("API client configured", "base_url", cfg.APIBaseURL)

	// Connect to database for queue worker
	database, err := db.WaitForDB(cfg, 60*time.Second)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}

	// ── Start SMTP server ───────────────────────────────────────────
	smtpServer := smtpgw.NewServer(cfg.GatewayHostname, api, tlsConfig)
	if err := smtpServer.ListenAndServe(smtpgw.SMTPPorts{
		Inbound:       cfg.SMTPPortInbound,
		Submission:    cfg.SMTPPortSubmission,
		SubmissionTLS: cfg.SMTPPortSubmissionTLS,
	}); err != nil {
		slog.Error("failed to start SMTP server", "error", err)
		os.Exit(1)
	}

	// ── Start IMAP server ───────────────────────────────────────────
	imapServer := imap.NewServer(cfg.GatewayHostname, api, tlsConfig)
	if err := imapServer.ListenAndServe(imap.IMAPPorts{
		IMAP:    cfg.IMAPPort,
		IMAPTLS: cfg.IMAPTLSPort,
	}); err != nil {
		slog.Error("failed to start IMAP server", "error", err)
		os.Exit(1)
	}

	// ── Start POP3 server ───────────────────────────────────────────
	pop3Server := pop3.NewServer(cfg.GatewayHostname, api, tlsConfig)
	if err := pop3Server.ListenAndServe(pop3.POP3Ports{
		POP3:    cfg.POP3Port,
		POP3TLS: cfg.POP3TLSPort,
	}); err != nil {
		slog.Error("failed to start POP3 server", "error", err)
		os.Exit(1)
	}

	// ── Start queue worker ──────────────────────────────────────────
	queueWorker := queue.NewWorker(database, cfg.GatewayHostname, cfg.QueueWorkers, cfg.QueuePollInterval)
	queueWorker.Start()

	slog.Info("protocol gateway fully started",
		"hostname", cfg.GatewayHostname,
		"smtp_ports", []int{cfg.SMTPPortInbound, cfg.SMTPPortSubmission, cfg.SMTPPortSubmissionTLS},
		"imap_ports", []int{cfg.IMAPPort, cfg.IMAPTLSPort},
		"pop3_ports", []int{cfg.POP3Port, cfg.POP3TLSPort},
	)

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down protocol gateway...")

	queueWorker.Shutdown()
	smtpServer.Shutdown()
	imapServer.Shutdown()
	pop3Server.Shutdown()

	slog.Info("protocol gateway stopped")
}
