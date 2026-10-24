package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/dns"
)

func writeTLSACert(t *testing.T, seed byte) string {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Unix(0, 0), NotAfter: time.Unix(86400, 0)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "public.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTLSARecordsCLI(t *testing.T) {
	current, next := writeTLSACert(t, 1), writeTLSACert(t, 2)
	var out, errs bytes.Buffer
	if code := runTLSARecords([]string{"--mail-host", "mx.example.test", "--cert", current, "--next-cert", next, "--json"}, &out, &errs); code != exitOK {
		t.Fatalf("exit %d: %s", code, &errs)
	}
	var got struct {
		Records   []dns.DNSRecord `json:"records"`
		DNSSEC    bool            `json:"dnssec_required"`
		Automatic bool            `json:"automatic_rollover"`
		Rollover  string          `json:"rollover"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Records) != 2 || !got.DNSSEC || got.Automatic || !strings.Contains(got.Rollover, "RRSIG") {
		t.Fatalf("invalid publication output: %s, %v", &out, err)
	}
	out.Reset()
	if code := runTLSARecords([]string{"--mail-host", "mx.example.test", "--cert", current}, &out, &errs); code != exitOK || !strings.Contains(out.String(), "IN TLSA 3 1 1 ") || !strings.Contains(out.String(), "DNSSEC") {
		t.Fatalf("text output: %d %s %s", code, &out, &errs)
	}
}

func TestTLSARecordsCLIRefusesBadInputs(t *testing.T) {
	private := filepath.Join(t.TempDir(), "private.pem")
	if err := os.WriteFile(private, []byte("-----BEGIN PRIVATE KEY-----\nc2VjcmV0\n-----END PRIVATE KEY-----\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--mail-host", "mx.example.test", "--cert", "missing-file"}, {"--mail-host", "mx.example.test", "--cert", private}} {
		var out, errs bytes.Buffer
		if code := runTLSARecords(args, &out, &errs); code == exitOK || out.Len() != 0 || strings.Contains(errs.String(), "c2VjcmV0") {
			t.Fatalf("bad input returned success or leaked data: %d %s %s", code, &out, &errs)
		}
	}
}
