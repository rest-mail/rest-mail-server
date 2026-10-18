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
	"path/filepath"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/acme"
)

// acmeIssueCmd is the cert_provider: acme branch of `chore instance:certs:issue`:
// it issues the instance's serving cert by HTTP-01 and writes it into the certs
// volume. It lives here, beside the rest of the instance tooling, so cmd/certgen
// stays the testbed's reference CA and nothing more (#149).
func acmeIssueCmd(args []string) {
	fs := flag.NewFlagSet("acme-issue", flag.ExitOnError)
	out := fs.String("out", "", "directory the pair is written to, as <first domain>.{crt,key}")
	domains := fs.String("domains", "", "comma-separated names the certificate covers; the first names the files")
	email := fs.String("email", "", "ACME account contact email")
	directory := fs.String("directory", "", "ACME directory URL (default: Let's Encrypt production)")
	staging := fs.Bool("staging", false, "use the Let's Encrypt staging directory, overriding -directory")
	listen := fs.String("listen", ":80", "address to serve HTTP-01 challenges on; the CA connects to port 80")
	_ = fs.Parse(args)

	if *out == "" || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "instance acme-issue: -out and -domains are required, and nothing else")
		os.Exit(2)
	}
	var names []string
	for _, d := range strings.Split(*domains, ",") {
		if d = strings.TrimSpace(d); d != "" {
			names = append(names, d)
		}
	}
	// Staging overrides the directory, as in acme.NewClient.
	if *staging {
		*directory = acme.LetsEncryptStaging
	}
	cfg := acme.IssueConfig{Directory: *directory, Email: *email, Domains: names}
	if err := issueACME(context.Background(), *out, *listen, cfg); err != nil {
		fatal("acme-issue: %v", err)
	}
}

// acmeTimeout bounds one whole issuance: account, order, every HTTP-01
// validation and finalization.
const acmeTimeout = 10 * time.Minute

// issueACME obtains one certificate covering domains from the ACME CA in cfg by
// HTTP-01, serving the challenges on listen (":80" in production: the CA always
// connects to port 80), and writes it to <outputDir>/<domains[0]>.{crt,key},
// the files the manual provider expects and SNICertLoader serves. Nothing is
// written unless issuance succeeds.
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
	slog.Info("acme: serving HTTP-01 challenges", "addr", ln.Addr().String(), "directory", cfg.Directory, "domains", cfg.Domains)

	ctx, cancel := context.WithTimeout(ctx, acmeTimeout)
	defer cancel()
	certPEM, keyPEM, err := acme.Issue(ctx, cfg, responder)
	if err != nil {
		return err
	}
	return writeServingPair(outputDir, cfg.Domains[0], certPEM, keyPEM)
}

// writeServingPair writes <name>.key (0600) and <name>.crt into dir, each via a
// temporary file renamed into place, so a reader never sees a half-written file
// and a failure leaves any previous pair untouched.
func writeServingPair(dir, name, certPEM, keyPEM string) error {
	if err := writeFileAtomic(filepath.Join(dir, name+".key"), []byte(keyPEM), 0600); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, name+".crt"), []byte(certPEM), 0644); err != nil {
		return err
	}
	slog.Info("acme: certificate written", "cert", filepath.Join(dir, name+".crt"), "key", filepath.Join(dir, name+".key"))
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
