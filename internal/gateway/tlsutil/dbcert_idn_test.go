package tlsutil

import (
	"crypto/tls"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// SNI only carries A-labels (RFC 6066), but a hosted domain is kept as the name
// it was entered with, which for an internationalized domain may be Unicode. The
// loader must find that domain's certificate, and count it as hosted when it has
// none, whichever form it is stored under (#153).
func TestDBCertLoader_IDNMatchesStoredUnicodeName(t *testing.T) {
	gdb := openCertTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	const servedALabel = "xn--bcher-sni-q9a.test"   // bücher-sni.test
	const missingALabel = "xn--mnchen-sni-9db.test" // münchen-sni.test

	fallback, _, _ := selfSigned(t, "mx.example.test")

	served := models.Domain{Name: "bücher-sni.test", ServerType: "traditional", Active: true}
	if err := tx.Create(&served).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	_, certPEM, keyPEM := selfSigned(t, servedALabel)
	if err := tx.Create(&models.Certificate{
		DomainID:  served.ID,
		CertPEM:   string(certPEM),
		KeyPEM:    string(keyPEM),
		Issuer:    "self-signed",
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter:  time.Now().Add(24 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	missing := models.Domain{Name: "münchen-sni.test", ServerType: "traditional", Active: true}
	if err := tx.Create(&missing).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	loader := NewDBCertLoader(tx, "", &fallback)

	for _, sni := range []string{servedALabel, strings.ToUpper(servedALabel)} {
		t.Run("A-label SNI finds the Unicode-stored domain's certificate/"+sni, func(t *testing.T) {
			cert, err := loader.GetCertificate(&tls.ClientHelloInfo{ServerName: sni})
			if err != nil {
				t.Fatalf("%s refused: %v", sni, err)
			}
			if cert == nil || cert == &fallback || !certCovers(cert, servedALabel) {
				t.Fatalf("answered with a certificate that does not carry %s", servedALabel)
			}
		})
	}

	t.Run("a Unicode-stored domain with no certificate is refused as hosted", func(t *testing.T) {
		cert, err := loader.GetCertificate(&tls.ClientHelloInfo{ServerName: missingALabel})
		if err == nil || cert != nil {
			t.Fatalf("GetCertificate(%s) = %v, %v; want a refusal", missingALabel, cert, err)
		}
		if errors.Is(err, ErrNotServedHere) {
			t.Fatalf("refused as not served here, want the fault for a hosted domain: %v", err)
		}
	})

	t.Run("a name that is not a domain is refused as not served here", func(t *testing.T) {
		cert, err := loader.GetCertificate(&tls.ClientHelloInfo{ServerName: "bad_.test"})
		if !errors.Is(err, ErrNotServedHere) || cert != nil {
			t.Fatalf("GetCertificate(bad_.test) = %v, %v; want ErrNotServedHere", cert, err)
		}
	})

	t.Run("HostedInDatabase matches the A-label to the Unicode-stored domain", func(t *testing.T) {
		hosted := HostedInDatabase(tx)
		for _, name := range []string{missingALabel, "münchen-sni.test", "MÜNCHEN-SNI.test"} {
			ok, err := hosted(name)
			if err != nil || !ok {
				t.Errorf("hosted(%q) = %v, %v; want true", name, ok, err)
			}
		}
		if ok, err := hosted("bad_.test"); err != nil || ok {
			t.Errorf("hosted(bad_.test) = %v, %v; want false", ok, err)
		}
	})

	t.Run("Invalidate drops the cached certificate under either form", func(t *testing.T) {
		if _, err := loader.GetCertificate(&tls.ClientHelloInfo{ServerName: servedALabel}); err != nil {
			t.Fatal(err)
		}
		loader.Invalidate("bücher-sni.test")
		loader.mu.RLock()
		_, cached := loader.cache[servedALabel]
		loader.mu.RUnlock()
		if cached {
			t.Error("certificate still cached after Invalidate with the Unicode name")
		}
	})
}
