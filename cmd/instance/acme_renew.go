package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/restmail/restmail/internal/acme"
)

// renewACME allows periodic callers to avoid unnecessary CA orders. A zero
// window preserves the original, unconditional issuance behavior, including
// when an operator deliberately switches CA directories or staging mode.
func renewACME(ctx context.Context, outputDir, listen string, cfg acme.IssueConfig, renewBefore time.Duration) error {
	if renewBefore < 0 {
		return errors.New("-renew-before must not be negative")
	}
	if len(cfg.Domains) == 0 {
		return errors.New("-domains is required")
	}
	if renewBefore > 0 {
		current, err := reusableACMEPair(outputDir, cfg.Domains, renewBefore, time.Now())
		if err != nil {
			return err
		}
		if current {
			slog.Info("acme: existing serving pair does not need renewal", "domains", cfg.Domains, "renew_before", renewBefore)
			return nil
		}
	}
	return issueACME(ctx, outputDir, listen, cfg)
}

// reusableACMEPair checks both files and every requested name. Missing, damaged,
// mismatched, not-yet-valid or expiring pairs need issuance. Other read failures
// are returned so an inaccessible volume does not trigger a pointless CA order.
// This checks usability, not CA provenance or trust: switching CA requires an
// unconditional issuance (-renew-before=0).
func reusableACMEPair(dir string, domains []string, renewBefore time.Duration, now time.Time) (bool, error) {
	certPath := filepath.Join(dir, domains[0]+".crt")
	keyPath := filepath.Join(dir, domains[0]+".key")
	certPEM, err := os.ReadFile(certPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read existing ACME certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read existing ACME private key: %w", err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return false, nil
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return false, nil
	}
	if now.Before(leaf.NotBefore) || !leaf.NotAfter.After(now.Add(renewBefore)) {
		return false, nil
	}
	for _, domain := range domains {
		if err := leaf.VerifyHostname(domain); err != nil {
			return false, nil
		}
	}
	return true, nil
}
