package main

import (
	"crypto/tls"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/imap"
	"github.com/restmail/restmail/internal/gateway/tlsutil"
)

func main() {
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(logHandler))

	slog.Info("starting IMAP gateway")

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
		if cfg.TLSCertDir != "" {
			loader := tlsutil.NewSNICertLoader(cfg.TLSCertDir, &cert)
			tlsConfig.GetCertificate = loader.GetCertificate
			slog.Info("TLS configured with SNI", "cert", cfg.TLSCertPath, "cert_dir", cfg.TLSCertDir)
		} else {
			slog.Info("TLS configured", "cert", cfg.TLSCertPath)
		}
	} else {
		slog.Warn("no TLS certificate configured — running without TLS")
	}

	api := apiclient.New(cfg.APIBaseURL)
	slog.Info("API client configured", "base_url", cfg.APIBaseURL)

	imapServer := imap.NewServer(cfg.GatewayHostname, api, tlsConfig)
	if err := imapServer.ListenAndServe(imap.IMAPPorts{
		IMAP:    cfg.IMAPPort,
		IMAPTLS: cfg.IMAPTLSPort,
	}); err != nil {
		slog.Error("failed to start IMAP server", "error", err)
		os.Exit(1)
	}

	slog.Info("IMAP gateway started",
		"hostname", cfg.GatewayHostname,
		"ports", []int{cfg.IMAPPort, cfg.IMAPTLSPort},
	)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down IMAP gateway...")
	imapServer.Shutdown()
	slog.Info("IMAP gateway stopped")
}
