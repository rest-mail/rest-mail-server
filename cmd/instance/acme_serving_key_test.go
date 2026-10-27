package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/acme"
)

func TestExistingACMEServingKey(t *testing.T) {
	const name = "mx.dane.test"
	cert, key := renewalTestPair(t, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour), name)
	_, otherKey := renewalTestPair(t, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour), name)
	for _, tc := range []struct {
		name, cert, key    string
		wantKey, wantError bool
	}{
		{"first issuance", "", "", false, false},
		{"expired leaf retains key", cert, key, true, false},
		{"lost certificate", "", key, false, true},
		{"lost key", cert, "", false, true},
		{"damaged key", cert, "damaged", false, true},
		{"mismatch", cert, otherKey, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for suffix, data := range map[string]string{".crt": tc.cert, ".key": tc.key} {
				if data != "" {
					if err := os.WriteFile(filepath.Join(dir, name+suffix), []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := existingACMEServingKey(dir, name)
			if (err != nil) != tc.wantError || (got != nil) != tc.wantKey {
				t.Fatalf("keyPresent=%v error=%v", got != nil, err)
			}
			if got != nil {
				b, _ := pem.Decode([]byte(cert))
				leaf, err := x509.ParseCertificate(b.Bytes)
				if err != nil {
					t.Fatal(err)
				}
				der, err := x509.MarshalPKIXPublicKey(got.Public())
				if err != nil || string(der) != string(leaf.RawSubjectPublicKeyInfo) {
					t.Fatal("loaded a different serving key")
				}
			}
			if tc.wantError {
				err := renewACME(context.Background(), dir, "invalid-listen", acme.IssueConfig{Domains: []string{name}}, 0)
				if err == nil || !strings.Contains(err.Error(), "refusing to replace") {
					t.Fatalf("renewal should fail before listening or contacting the CA: %v", err)
				}
			}
			for suffix, want := range map[string]string{".crt": tc.cert, ".key": tc.key} {
				data, err := os.ReadFile(filepath.Join(dir, name+suffix))
				if want == "" {
					if !os.IsNotExist(err) {
						t.Fatal("key loading created a missing serving file")
					}
				} else if err != nil || string(data) != want {
					t.Fatal("key loading or refused renewal changed existing files")
				}
			}
		})
	}
}
