package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// selfSigned returns a certificate for name, and its PEM blocks.
func selfSigned(t *testing.T, name string) (tls.Certificate, []byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert, certPEM, keyPEM
}

// A client that asks for a name by SNI must never be answered with a certificate
// for a different name (issue #290). Whatever the reason — the domain is hosted
// here but its certificate is missing or unreadable, or the name is not hosted at
// all — the handshake is refused. Answering with the fallback certificate fails
// verification on the client, or teaches people to click through the warning.
func TestSNICertLoader_NeverAnswersWithAnotherNamesCertificate(t *testing.T) {
	dir := t.TempDir()
	fallback, _, _ := selfSigned(t, "mx.example.test")

	// A certificate this server really does have, to prove the loader still works.
	served, certPEM, keyPEM := selfSigned(t, "served.test")
	if err := os.WriteFile(filepath.Join(dir, "served.test.crt"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "served.test.key"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	// A hosted domain whose key file is missing, so the pair cannot be loaded.
	brokenPEM := certPEM
	if err := os.WriteFile(filepath.Join(dir, "broken.test.crt"), brokenPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	loader := NewSNICertLoader(dir, &fallback)
	loader.SetHostedNames(func(name string) (bool, error) {
		return name == "hosted.test" || name == "broken.test" || name == "served.test", nil
	})

	t.Run("hosted domain with no certificate is refused", func(t *testing.T) {
		cert, err := loader.GetCertificate(&tls.ClientHelloInfo{ServerName: "hosted.test"})
		if err == nil {
			t.Fatalf("got a certificate for a hosted domain with none of its own, want an error")
		}
		if cert != nil {
			t.Errorf("cert = %v, want nil", cert)
		}
	})

	t.Run("hosted domain whose certificate will not load is refused", func(t *testing.T) {
		cert, err := loader.GetCertificate(&tls.ClientHelloInfo{ServerName: "broken.test"})
		if err == nil {
			t.Fatalf("got a certificate for a domain whose pair cannot be loaded, want an error")
		}
		if cert != nil {
			t.Errorf("cert = %v, want nil", cert)
		}
	})

	t.Run("name this server does not host is refused", func(t *testing.T) {
		cert, err := loader.GetCertificate(&tls.ClientHelloInfo{ServerName: "someone-else.test"})
		if err == nil {
			t.Fatalf("got a certificate for a name this server does not host, want an error")
		}
		if cert != nil {
			t.Errorf("cert = %v, want nil", cert)
		}
	})

	t.Run("no SNI gets the server's own certificate", func(t *testing.T) {
		// Other mail servers connecting to port 25 often send no SNI at all.
		cert, err := loader.GetCertificate(&tls.ClientHelloInfo{})
		if err != nil {
			t.Fatalf("no-SNI handshake refused: %v", err)
		}
		if cert != &fallback {
			t.Errorf("cert = %v, want the server's own certificate", cert)
		}
	})

	t.Run("a domain with its own certificate is still served", func(t *testing.T) {
		cert, err := loader.GetCertificate(&tls.ClientHelloInfo{ServerName: "served.test"})
		if err != nil {
			t.Fatalf("served.test refused: %v", err)
		}
		if cert == nil || len(cert.Certificate) == 0 {
			t.Fatal("no certificate returned for served.test")
		}
		if cert.Certificate[0] == nil || string(cert.Certificate[0]) != string(served.Certificate[0]) {
			t.Error("served.test was answered with a different certificate")
		}
	})
}

// Without a hosted-names check the loader cannot tell "hosted but broken" from
// "not ours", but it still must not answer with another name's certificate.
func TestSNICertLoader_RefusesUnknownNameWithoutHostedNamesCheck(t *testing.T) {
	dir := t.TempDir()
	fallback, _, _ := selfSigned(t, "mx.example.test")
	loader := NewSNICertLoader(dir, &fallback)

	cert, err := loader.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.test"})
	if err == nil {
		t.Fatalf("got a certificate for an unknown name, want an error")
	}
	if cert != nil {
		t.Errorf("cert = %v, want nil", cert)
	}
}
