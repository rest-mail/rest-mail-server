package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// recoverACMEServingKey repairs a publication interrupted after the key rename
// but before the certificate rename. writeServingPair leaves a hard link to
// the old key in its private staging directory on process exit. Only a backup
// that matches the certificate still on disk may replace the serving key.
// Call with the issuer lock held, before checking reuse or contacting the CA.
func recoverACMEServingKey(dir, name string) error {
	certPEM, err := os.ReadFile(filepath.Join(dir, name+".crt"))
	if errors.Is(err, os.ErrNotExist) {
		return nil // First issuance has no old certificate to recover.
	}
	if err != nil {
		return fmt.Errorf("read serving certificate for recovery: %w", err)
	}
	keyPath := filepath.Join(dir, name+".key")
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read serving key for recovery: %w", err)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err == nil {
		return nil // Never roll back a complete published pair.
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("find ACME serving key backups: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".acme-pair-") {
			continue
		}
		backup := filepath.Join(dir, entry.Name(), "previous.key")
		previous, err := os.ReadFile(backup)
		if errors.Is(err, os.ErrNotExist) {
			continue // Crash before the backup link was created.
		}
		if err != nil {
			return fmt.Errorf("read ACME serving key backup: %w", err)
		}
		if _, err := tls.X509KeyPair(certPEM, previous); err != nil {
			continue // Another name's backup, or a damaged backup.
		}
		if err := writeFileAtomic(keyPath, previous, 0600); err != nil {
			return fmt.Errorf("recover ACME serving key: %w", err)
		}
		// Retain the backup: the staging directory can belong to another name
		// sharing this key, and recovery must not destroy its only backup.
		slog.Warn("acme: restored serving key after interrupted publication", "cert", filepath.Join(dir, name+".crt"), "backup", backup)
		return nil
	}
	return nil // No matching backup: renewal validates the pair before reusing its key.
}
