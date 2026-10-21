package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/restmail/restmail/internal/acme"
)

// acmeIssueCmd is the cert_provider: acme branch of `chore instance:certs:issue`:
// it issues the instance's serving cert by HTTP-01 and writes it into the certs
// volume. It lives here, beside the rest of the instance tooling, so there is
// one cert path (#149).
func acmeIssueCmd(args []string) {
	fs := flag.NewFlagSet("acme-issue", flag.ExitOnError)
	out := fs.String("out", "", "directory the pair is written to, as <first domain>.{crt,key}")
	domains := fs.String("domains", "", "comma-separated names the certificate covers; the first names the files")
	email := fs.String("email", "", "ACME account contact email")
	directory := fs.String("directory", "", "ACME directory URL (default: Let's Encrypt production)")
	staging := fs.Bool("staging", false, "use the Let's Encrypt staging directory, overriding -directory")
	listen := fs.String("listen", ":80", "address to serve HTTP-01 challenges on; the CA connects to port 80")
	renewBefore := fs.Duration("renew-before", 0, "reuse a valid matching pair until this long before expiry (e.g. 720h); 0 always issues")
	checkInterval := fs.Duration("check-interval", 0, "repeat expiry-aware checks this long after each attempt (e.g. 12h); requires positive -renew-before; 0 runs once")
	_ = fs.Parse(args)

	names := splitList(*domains)
	if *out == "" || len(names) == 0 || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "instance acme-issue: -out and -domains are required, and nothing else")
		os.Exit(2)
	}
	// Staging overrides the directory, as in acme.NewClient.
	if *staging {
		*directory = acme.LetsEncryptStaging
	}
	cfg := acme.IssueConfig{Directory: *directory, Email: *email, Domains: names}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runACMEChecks(ctx, *renewBefore, *checkInterval, func(ctx context.Context) error {
		return renewACME(ctx, *out, *listen, cfg, *renewBefore)
	}); err != nil {
		fatal("acme-issue: %v", err)
	}
}

// acmeTimeout bounds one whole issuance: account, order, every HTTP-01
// validation and finalization.
const acmeTimeout = 10 * time.Minute

// issueACME obtains one certificate covering domains from the ACME CA in cfg by
// HTTP-01, serving the challenges on listen (":80" in production: the CA always
// connects to port 80), and writes it to <outputDir>/<domains[0]>.{crt,key},
// the files the manual provider expects and SNICertLoader serves. No serving
// pair is written unless issuance succeeds. The account key is saved
// in outputDir before contacting the CA and retained even if issuance fails.
func issueACME(ctx context.Context, outputDir, listen string, cfg acme.IssueConfig) error {
	if len(cfg.Domains) == 0 {
		return errors.New("-domains is required")
	}

	responder := acme.NewHTTP01Responder()
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen for HTTP-01 on %s: %w", listen, err)
	}
	srv := &http.Server{Handler: responder, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	if cfg.AccountKey == nil {
		cfg.AccountKey, err = loadACMEAccountKey(outputDir, cfg.Directory)
		if err != nil {
			return err
		}
	}
	slog.Info("acme: serving HTTP-01 challenges", "addr", ln.Addr().String(), "directory", cfg.Directory, "domains", cfg.Domains)

	ctx, cancel := context.WithTimeout(ctx, acmeTimeout)
	defer cancel()
	certPEM, keyPEM, err := acme.Issue(ctx, cfg, responder)
	if err != nil {
		return err
	}
	return writeServingPair(outputDir, cfg.Domains[0], certPEM, keyPEM)
}

// writeServingPair stages both files before publishing <name>.key (0600) and
// <name>.crt. If publishing the certificate fails, it restores the previous key.
// Each rename is atomic, but the pair is not a filesystem transaction: callers
// must serialize issuance, and readers may see a mismatch between the renames.
// A process crash between renames also requires operator recovery.
func writeServingPair(dir, name, certPEM, keyPEM string) error {
	stage, err := os.MkdirTemp(dir, ".acme-pair-*")
	if err != nil {
		return fmt.Errorf("stage serving pair: %w", err)
	}
	keepBackup := false
	defer func() {
		if !keepBackup {
			_ = os.RemoveAll(stage)
		}
	}()
	stagedKey := filepath.Join(stage, "new.key")
	stagedCert := filepath.Join(stage, "new.crt")
	if err := writeFileAtomic(stagedKey, []byte(keyPEM), 0600); err != nil {
		return err
	}
	if err := writeFileAtomic(stagedCert, []byte(certPEM), 0644); err != nil {
		return err
	}
	keyPath := filepath.Join(dir, name+".key")
	certPath := filepath.Join(dir, name+".crt")
	backup := filepath.Join(stage, "previous.key")
	// A hard link preserves the old inode and permissions without removing the
	// serving key. The staging directory is on the same filesystem and is 0700.
	hadKey := true
	if err := os.Link(keyPath, backup); errors.Is(err, os.ErrNotExist) {
		hadKey = false
	} else if err != nil {
		return fmt.Errorf("back up serving key: %w", err)
	}
	if err := os.Rename(stagedKey, keyPath); err != nil {
		return fmt.Errorf("publish serving key: %w", err)
	}
	if err := os.Rename(stagedCert, certPath); err != nil {
		publishErr := fmt.Errorf("publish serving certificate: %w", err)
		var rollbackErr error
		if hadKey {
			if err := os.Rename(backup, keyPath); err != nil {
				// Never delete the only backup if rollback itself fails.
				keepBackup = true
				rollbackErr = fmt.Errorf("restore serving key (backup %s): %w", backup, err)
			}
		} else {
			if err := os.Remove(keyPath); err != nil {
				rollbackErr = fmt.Errorf("remove newly published serving key: %w", err)
			}
		}
		return errors.Join(publishErr, rollbackErr)
	}
	slog.Info("acme: certificate written", "cert", certPath, "key", keyPath)
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("failed to create temporary file for %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	// CreateTemp opens the file 0600, so key bytes are never readable by others.
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to set permissions on %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("failed to move %s into place: %w", path, err)
	}
	return nil
}
