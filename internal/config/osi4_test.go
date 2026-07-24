package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// validSecretsProd sets a JWT_SECRET and MASTER_KEY that both clear the OSI-4
// length floors, so a production Load fails (or not) solely on the setting under
// test rather than on the secrets.
func validSecretsProd(t *testing.T) {
	t.Helper()
	t.Setenv("JWT_SECRET", "a-strong-production-secret-32-bytes-long")
	t.Setenv("MASTER_KEY", "a-strong-master-key-16plus")
}

// writeKeypair writes a freshly generated self-signed cert and its matching key
// as PEM files in dir and returns their paths. The pair is valid, so
// tls.LoadX509KeyPair accepts it.
func writeKeypair(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	certPath = filepath.Join(dir, "tls.crt")
	keyPath = filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

// TestOSI4_ProductionRejectsInsecure exercises every insecure case and asserts
// that Load() refuses to boot when ENVIRONMENT=production.
func TestOSI4_ProductionRejectsInsecure(t *testing.T) {
	// A valid keypair reused by the cases that must fail on something else, so a
	// missing-cert finding does not mask the case under test.
	goodDir := t.TempDir()
	goodCert, goodKey := writeKeypair(t, goodDir)

	tests := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{
			name: "empty JWT secret",
			setup: func(t *testing.T) {
				t.Setenv("JWT_SECRET", "")
				t.Setenv("MASTER_KEY", "a-strong-master-key-16plus")
			},
		},
		{
			name: "default JWT secret",
			setup: func(t *testing.T) {
				// JWT_SECRET left unset -> compiled-in dev default.
				t.Setenv("MASTER_KEY", "a-strong-master-key-16plus")
			},
		},
		{
			name: "short JWT secret",
			setup: func(t *testing.T) {
				t.Setenv("JWT_SECRET", "too-short")
				t.Setenv("MASTER_KEY", "a-strong-master-key-16plus")
			},
		},
		{
			name: "empty MASTER_KEY",
			setup: func(t *testing.T) {
				t.Setenv("JWT_SECRET", "a-strong-production-secret-32-bytes-long")
				// MASTER_KEY left unset.
			},
		},
		{
			name: "short MASTER_KEY",
			setup: func(t *testing.T) {
				t.Setenv("JWT_SECRET", "a-strong-production-secret-32-bytes-long")
				t.Setenv("MASTER_KEY", "short")
			},
		},
		{
			name: "TLS cert without key",
			setup: func(t *testing.T) {
				validSecretsProd(t)
				t.Setenv("TLS_CERT_PATH", goodCert)
			},
		},
		{
			name: "TLS key without cert",
			setup: func(t *testing.T) {
				validSecretsProd(t)
				t.Setenv("TLS_KEY_PATH", goodKey)
			},
		},
		{
			name: "TLS cert file missing",
			setup: func(t *testing.T) {
				validSecretsProd(t)
				t.Setenv("TLS_CERT_PATH", filepath.Join(goodDir, "does-not-exist.crt"))
				t.Setenv("TLS_KEY_PATH", goodKey)
			},
		},
		{
			name: "TLS keypair does not parse",
			setup: func(t *testing.T) {
				validSecretsProd(t)
				dir := t.TempDir()
				bad := filepath.Join(dir, "bad.crt")
				if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
					t.Fatalf("write bad cert: %v", err)
				}
				t.Setenv("TLS_CERT_PATH", bad)
				t.Setenv("TLS_KEY_PATH", goodKey)
			},
		},
		{
			name: "TLS_CERT_DIR missing",
			setup: func(t *testing.T) {
				validSecretsProd(t)
				t.Setenv("TLS_CERT_DIR", filepath.Join(goodDir, "no-such-dir"))
			},
		},
		{
			name: "malformed PROXY_PROTOCOL_TRUSTED_CIDRS",
			setup: func(t *testing.T) {
				validSecretsProd(t)
				t.Setenv("PROXY_PROTOCOL_TRUSTED_CIDRS", "10.0.0.0/8,not-a-cidr")
			},
		},
		{
			name: "malformed METRICS_ALLOWED_CIDRS",
			setup: func(t *testing.T) {
				validSecretsProd(t)
				t.Setenv("METRICS_ALLOWED_CIDRS", "garbage")
			},
		},
		{
			name: "malformed RESTMAIL_DELIVER_TRUSTED_CIDRS",
			setup: func(t *testing.T) {
				validSecretsProd(t)
				t.Setenv("RESTMAIL_DELIVER_TRUSTED_CIDRS", "999.999.999.999")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("ENVIRONMENT", "production")
			tc.setup(t)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() in production must reject %q, got nil error", tc.name)
			}
		})
	}
}

// TestOSI4_DevelopmentWarnsButBoots asserts that every insecure case that would
// fail in production only warns (and still boots) in development — the testbed/
// e2e default — so enforcement is strictly gated on ENVIRONMENT=production.
func TestOSI4_DevelopmentWarnsButBoots(t *testing.T) {
	goodDir := t.TempDir()
	goodCert, goodKey := writeKeypair(t, goodDir)

	setups := map[string]func(t *testing.T){
		"empty JWT secret":   func(t *testing.T) { t.Setenv("JWT_SECRET", "") },
		"default JWT secret": func(t *testing.T) {}, // unset -> default
		"short JWT secret":   func(t *testing.T) { t.Setenv("JWT_SECRET", "too-short") },
		"empty MASTER_KEY":   func(t *testing.T) {}, // unset
		"short MASTER_KEY":   func(t *testing.T) { t.Setenv("MASTER_KEY", "short") },
		"TLS half-config":    func(t *testing.T) { t.Setenv("TLS_CERT_PATH", goodCert) },
		"TLS keypair missing": func(t *testing.T) {
			t.Setenv("TLS_CERT_PATH", filepath.Join(goodDir, "nope.crt"))
			t.Setenv("TLS_KEY_PATH", goodKey)
		},
		"malformed trusted CIDRs": func(t *testing.T) { t.Setenv("PROXY_PROTOCOL_TRUSTED_CIDRS", "not-a-cidr") },
		"malformed metrics CIDRs": func(t *testing.T) { t.Setenv("METRICS_ALLOWED_CIDRS", "garbage") },
	}

	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			// ENVIRONMENT unset -> "development" default.
			setup(t)

			if _, err := Load(); err != nil {
				t.Fatalf("Load() in development must warn (not fail) for %q, got error: %v", name, err)
			}
		})
	}
}

// TestOSI4_ValidProductionBootsClean asserts that a fully-valid production config
// — strong secrets, a real TLS keypair, and well-formed CIDR allowlists — boots
// without error.
func TestOSI4_ValidProductionBootsClean(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	cert, key := writeKeypair(t, dir)

	t.Setenv("ENVIRONMENT", "production")
	validSecretsProd(t)
	t.Setenv("TLS_CERT_PATH", cert)
	t.Setenv("TLS_KEY_PATH", key)
	t.Setenv("TLS_CERT_DIR", dir)
	t.Setenv("PROXY_PROTOCOL_TRUSTED_CIDRS", "10.0.0.0/8, 192.168.0.0/16")
	t.Setenv("METRICS_ALLOWED_CIDRS", "127.0.0.1, ::1/128")
	t.Setenv("RESTMAIL_DELIVER_TRUSTED_CIDRS", "172.16.0.0/12")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() must boot clean for a valid production config, got error: %v", err)
	}
	if cfg.Environment != "production" {
		t.Errorf("Environment = %q, want production", cfg.Environment)
	}
}

// TestOSI4_Helpers unit-tests the pure validators directly for clearer failure
// localization than the env-driven Load path.
func TestOSI4_Helpers(t *testing.T) {
	t.Run("validateCIDRList", func(t *testing.T) {
		if f := validateCIDRList("K", []string{"10.0.0.0/8", "127.0.0.1", "::1/128", ""}); len(f) != 0 {
			t.Errorf("valid CIDRs/IPs produced findings: %v", f)
		}
		if f := validateCIDRList("K", []string{"nope", "10.0.0.0/8"}); len(f) != 1 {
			t.Errorf("one malformed entry should produce exactly one finding, got %v", f)
		}
	})

	t.Run("validateTLSKeypair none set", func(t *testing.T) {
		if f := validateTLSKeypair("C", "", "K", ""); f != nil {
			t.Errorf("unset TLS paths should produce no finding, got %v", f)
		}
	})

	t.Run("validateTLSKeypair half config", func(t *testing.T) {
		if f := validateTLSKeypair("C", "/tmp/x.crt", "K", ""); len(f) != 1 {
			t.Errorf("half-config should produce one finding, got %v", f)
		}
	})

	t.Run("validateTLSKeypair valid", func(t *testing.T) {
		dir := t.TempDir()
		cert, key := writeKeypair(t, dir)
		if f := validateTLSKeypair("C", cert, "K", key); f != nil {
			t.Errorf("valid keypair should produce no finding, got %v", f)
		}
	})
}
