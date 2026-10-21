package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/acme"
)

func renewalTestPair(t *testing.T, notBefore, notAfter time.Time, names ...string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     names,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func TestReusableACMEPair(t *testing.T) {
	const primary = "mail.example.test"
	now := time.Now().UTC().Truncate(time.Second)
	window := acme.RenewalWindow
	cert, key := renewalTestPair(t, now.Add(-time.Hour), now.Add(90*24*time.Hour), primary, "smtp.example.test")
	_, otherKey := renewalTestPair(t, now.Add(-time.Hour), now.Add(90*24*time.Hour), primary)
	for _, tt := range []struct {
		name      string
		domains   []string
		now       time.Time
		cert, key string
		want      bool
	}{
		{"healthy", []string{primary}, now, cert, key, true},
		{"all SANs covered", []string{primary, "smtp.example.test"}, now, cert, key, true},
		{"added SAN", []string{primary, "imap.example.test"}, now, cert, key, false},
		{"before renewal window", []string{primary}, now.Add(60*24*time.Hour - time.Second), cert, key, true},
		{"exact renewal boundary", []string{primary}, now.Add(60 * 24 * time.Hour), cert, key, false},
		{"inside renewal window", []string{primary}, now.Add(61 * 24 * time.Hour), cert, key, false},
		{"expired", []string{primary}, now.Add(91 * 24 * time.Hour), cert, key, false},
		{"not yet valid", []string{primary}, now.Add(-2 * time.Hour), cert, key, false},
		{"mismatched key", []string{primary}, now, cert, otherKey, false},
		{"malformed certificate", []string{primary}, now, "damaged", key, false},
		{"malformed key", []string{primary}, now, cert, "damaged", false},
		{"missing certificate", []string{primary}, now, "", key, false},
		{"missing key", []string{primary}, now, cert, "", false},
		{"no pair", []string{primary}, now, "", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for suffix, data := range map[string]string{".crt": tt.cert, ".key": tt.key} {
				if data != "" {
					if err := os.WriteFile(filepath.Join(dir, primary+suffix), []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := reusableACMEPair(dir, tt.domains, window, tt.now)
			if err != nil || got != tt.want {
				t.Fatalf("reusableACMEPair = %v, %v; want %v, nil", got, err, tt.want)
			}
		})
	}
}

func TestRenewACME_SkipsWithoutListenerAccountOrCA(t *testing.T) {
	dir := t.TempDir()
	const primary = "mail.example.test"
	now := time.Now()
	cert, key := renewalTestPair(t, now.Add(-time.Hour), now.Add(90*24*time.Hour), primary)
	if err := writeServingPair(dir, primary, cert, key); err != nil {
		t.Fatal(err)
	}
	// A reusable pair must succeed even though neither the listener nor CA
	// could work, and it must not create an account key or rewrite the pair.
	if err := renewACME(context.Background(), dir, "invalid listen address", acme.IssueConfig{
		Directory: "http://127.0.0.1:1/directory", Domains: []string{primary},
	}, acme.RenewalWindow); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("skipped renewal changed directory: %d files, %v", len(entries), err)
	}
	for suffix, want := range map[string]string{".crt": cert, ".key": key} {
		got, err := os.ReadFile(filepath.Join(dir, primary+suffix))
		if err != nil || string(got) != want {
			t.Fatalf("skipped renewal changed %s: %v", suffix, err)
		}
	}
}

func TestRenewACME_FailedOrderPreservesPair(t *testing.T) {
	const primary = "mail.example.test"
	for _, tt := range []struct {
		name        string
		renewBefore time.Duration
		validFor    time.Duration
	}{
		{"due", acme.RenewalWindow, 24 * time.Hour},
		{"force", 0, 90 * 24 * time.Hour},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			now := time.Now()
			cert, key := renewalTestPair(t, now.Add(-time.Hour), now.Add(tt.validFor), primary)
			if err := writeServingPair(dir, primary, cert, key); err != nil {
				t.Fatal(err)
			}
			if err := renewACME(context.Background(), dir, "127.0.0.1:0", acme.IssueConfig{
				Directory: "http://127.0.0.1:1/directory", Domains: []string{primary},
			}, tt.renewBefore); err == nil {
				t.Fatal("issuance succeeded without a CA")
			}
			// Reaching the CA requires a persisted account, proving issuance
			// was attempted even when the old certificate is still valid.
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 3 {
				t.Fatalf("want pair plus account key, got %d files: %v", len(entries), err)
			}
			for suffix, want := range map[string]string{".crt": cert, ".key": key} {
				got, err := os.ReadFile(filepath.Join(dir, primary+suffix))
				if err != nil || string(got) != want {
					t.Fatalf("failed renewal changed %s: %v", suffix, err)
				}
			}
		})
	}
}

func TestRenewACME_RejectsInvalidOptions(t *testing.T) {
	for _, tt := range []struct {
		name        string
		domains     []string
		renewBefore time.Duration
	}{
		{"no domains", nil, acme.RenewalWindow},
		{"negative window", []string{"mail.example.test"}, -time.Hour},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := renewACME(context.Background(), dir, "127.0.0.1:0", acme.IssueConfig{
				Domains: tt.domains,
			}, tt.renewBefore); err == nil {
				t.Fatal("invalid options accepted")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid options wrote files: %v", err)
			}
		})
	}
}

func TestReusableACMEPair_ReadError(t *testing.T) {
	const primary = "mail.example.test"
	for _, suffix := range []string{".crt", ".key"} {
		t.Run(suffix, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, primary+".crt"), []byte("CERT"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, primary+suffix)
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := reusableACMEPair(dir, []string{primary}, acme.RenewalWindow, time.Now()); err == nil {
				t.Fatal("read failure treated as permission to place an order")
			}
		})
	}
}
