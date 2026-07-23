package main

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func parseCertFile(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatalf("no PEM in %s", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return cert
}

func TestGenerateInternalMTLS(t *testing.T) {
	dir := t.TempDir()
	if err := generateInternalMTLS(dir, "api,localhost", "127.0.0.1,10.99.0.20", "smtp-gateway"); err != nil {
		t.Fatalf("generateInternalMTLS: %v", err)
	}

	caCert := parseCertFile(t, filepath.Join(dir, internalCACertFile))
	if !caCert.IsCA {
		t.Error("internal CA cert is not marked IsCA")
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	// Server cert: verifies as a server cert against the CA, carries the IP SAN.
	server := parseCertFile(t, filepath.Join(dir, internalServerCertFile))
	if _, err := server.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Errorf("server cert failed ServerAuth verification: %v", err)
	}
	foundIP := false
	for _, ip := range server.IPAddresses {
		if ip.String() == "10.99.0.20" {
			foundIP = true
		}
	}
	if !foundIP {
		t.Errorf("server cert missing IP SAN 10.99.0.20, got %v", server.IPAddresses)
	}

	// Client cert: verifies as a client cert against the CA, carries the CN.
	client := parseCertFile(t, filepath.Join(dir, internalClientCertFile))
	if _, err := client.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("client cert failed ClientAuth verification: %v", err)
	}
	if client.Subject.CommonName != "smtp-gateway" {
		t.Errorf("client CN = %q, want smtp-gateway", client.Subject.CommonName)
	}

	// A client cert must NOT pass server-auth verification (EKU is enforced).
	if _, err := client.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err == nil {
		t.Error("client cert unexpectedly passed ServerAuth verification")
	}
}

func TestGenerateInternalMTLS_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := generateInternalMTLS(dir, "api", "127.0.0.1", "gw"); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(dir, internalCACertFile))
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	clientBefore, err := os.ReadFile(filepath.Join(dir, internalClientCertFile))
	if err != nil {
		t.Fatalf("read client: %v", err)
	}

	if err := generateInternalMTLS(dir, "api", "127.0.0.1", "gw"); err != nil {
		t.Fatalf("second generate: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, internalCACertFile))
	if err != nil {
		t.Fatalf("read CA 2: %v", err)
	}
	clientAfter, err := os.ReadFile(filepath.Join(dir, internalClientCertFile))
	if err != nil {
		t.Fatalf("read client 2: %v", err)
	}
	if string(before) != string(after) {
		t.Error("CA cert changed on re-run — not CA-preserving")
	}
	if string(clientBefore) != string(clientAfter) {
		t.Error("client cert changed on re-run — not idempotent")
	}
}

func TestGenerateInternalMTLS_RejectsBadIP(t *testing.T) {
	dir := t.TempDir()
	if err := generateInternalMTLS(dir, "api", "not-an-ip", "gw"); err == nil {
		t.Fatal("expected error for invalid --server-ip, got nil")
	}
}
