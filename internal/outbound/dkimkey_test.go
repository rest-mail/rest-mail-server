package outbound

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	dkim "github.com/rest-mail/go-dkim"
)

// pkcs8PEM encodes key as a PEM "PRIVATE KEY" block, the form openssl genpkey
// writes.
func pkcs8PEM(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal PKCS#8: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// A key the domain can sign with is accepted, in each form go-dkim signs with.
func TestCheckDKIMKey_AcceptsKeysThatSign(t *testing.T) {
	pkcs1, _, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate Ed25519 key: %v", err)
	}

	cases := []struct {
		name, selector, key string
	}{
		{"RSA 2048, PKCS#1", "default", pkcs1},
		{"RSA 2048, PKCS#8", "rm2026", pkcs8PEM(t, rsaKey)},
		{"Ed25519, PKCS#8", "ed1", pkcs8PEM(t, edKey)},
		{"dotted selector", "mail.2026-10", pkcs1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckDKIMKey("example.test", tc.selector, tc.key); err != nil {
				t.Fatalf("CheckDKIMKey refused a key that signs: %v", err)
			}
		})
	}
}

// Each key or selector go-dkim would not sign with, or that this server will
// not sign with, is refused, naming the field at fault.
func TestCheckDKIMKey_Refusals(t *testing.T) {
	good, _, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate EC key: %v", err)
	}
	ecDER, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		t.Fatalf("marshal EC key: %v", err)
	}

	cases := []struct {
		name, selector, key string
		field, reason       string // reason: a fragment of the refusal
	}{
		{"selector with an underscore", "rm_1", good, "selector", "not a DNS label"},
		{"selector with a space", "rm 1", good, "selector", "not a DNS label"},
		{"selector starting with a hyphen", "-rm1", good, "selector", "not a DNS label"},
		{"selector ending with a hyphen", "rm1-", good, "selector", "not a DNS label"},
		{"selector with an empty label", "rm..1", good, "selector", "not a DNS label"},
		{"selector ending with a dot", "rm1.", good, "selector", "not a DNS label"},
		{"selector in Unicode", "sélecteur", good, "selector", "not a DNS label"},
		{"selector longer than the column", strings.Repeat("a", 30) + "." + strings.Repeat("b", 33), good, "selector", "not a DNS label"},
		{"not PEM", "default", "MIIEpAIBAAKCAQEA", "private_key", "not a PEM-encoded private key"},
		{"a public key", "default", publicPEM(t, &small.PublicKey), "private_key", "not an RSA or Ed25519 private key"},
		{"EC P-256, SEC 1", "default", string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER})), "private_key", "not an RSA or Ed25519 private key"},
		{"EC P-256, PKCS#8", "default", pkcs8PEM(t, ec), "private_key", "not an RSA or Ed25519 private key"},
		{"RSA 1024", "default", pkcs8PEM(t, small), "private_key", "1024-bit RSA key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckDKIMKey("example.test", tc.selector, tc.key)
			var refused *DKIMKeyError
			if !errors.As(err, &refused) {
				t.Fatalf("CheckDKIMKey = %v, want a *DKIMKeyError", err)
			}
			if refused.Field != tc.field || !strings.Contains(refused.Reason, tc.reason) {
				t.Fatalf("refused %s: %q, want %s: ...%q...", refused.Field, refused.Reason, tc.field, tc.reason)
			}
		})
	}
}

// Unicode hosted domains remain stored as Unicode but sign using A-labels.
func TestCheckDKIMKey_IDN(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pkcs8PEM(t, key)
	for _, domain := range []string{"bücher.test", "bu\u0308cher.test", "BÜCHER.test", "xn--bcher-kva.test", "faß.de", "例え.jp"} {
		t.Run(domain, func(t *testing.T) {
			if err := CheckDKIMKey(domain, "default", keyPEM); err != nil {
				t.Fatalf("IDN key cannot sign: %v", err)
			}
		})
	}
	for _, domain := range []string{"bad..test", "bad_.test", "a\u200db.test", "\xff.test"} {
		t.Run(domain, func(t *testing.T) {
			if err := CheckDKIMKey(domain, "default", keyPEM); err == nil {
				t.Fatal("invalid IDN accepted")
			}
		})
	}
}

// ParseDKIMKey reads an Ed25519 key, which go-dkim's ParsePrivateKey does
// not, so a key the check accepts is one the signer can load.
func TestParseDKIMKey_ReadsEd25519(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate Ed25519 key: %v", err)
	}
	key, err := ParseDKIMKey(pkcs8PEM(t, priv))
	if err != nil {
		t.Fatalf("ParseDKIMKey: %v", err)
	}
	if got, ok := key.Public().(ed25519.PublicKey); !ok || !got.Equal(pub) {
		t.Fatalf("ParseDKIMKey returned %T, not the Ed25519 key", key)
	}
}

func publicPEM(t *testing.T, pub any) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}
