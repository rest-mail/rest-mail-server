package tlsutil

import (
	"crypto/tls"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// openCertTestDB connects to the test database the CI job provides, and skips
// when there is none, the way the handler tests do.
func openCertTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	port, err := strconv.Atoi(envOrDefault("DB_PORT", "5432"))
	if err != nil {
		t.Skipf("certificate loader test skipped: bad DB_PORT (%v)", err)
	}
	cfg := &config.Config{
		DBHost: envOrDefault("DB_HOST", "localhost"),
		DBPort: port,
		DBName: envOrDefault("DB_NAME", "restmail"),
		DBUser: envOrDefault("DB_USER", "restmail"),
		DBPass: envOrDefault("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("certificate loader test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Certificate{}); err != nil {
		t.Skipf("certificate loader test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// The database-backed loader must keep the same three cases apart as the file
// loader (issue #290): a hosted domain with no usable certificate is refused, a
// name this server does not host is refused, and a handshake with no SNI gets the
// server's own certificate. It must never answer with another name's certificate.
func TestDBCertLoader_NeverAnswersWithAnotherNamesCertificate(t *testing.T) {
	gdb := openCertTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fallback, _, _ := selfSigned(t, "mx.example.test")

	// A domain this server hosts, with no certificate of its own.
	hosted := models.Domain{Name: "hosted-nocert.test", ServerType: "traditional", Active: true}
	if err := tx.Create(&hosted).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	// A domain whose only certificate has expired, so it cannot be served either.
	expiredDomain := models.Domain{Name: "expired.test", ServerType: "traditional", Active: true}
	if err := tx.Create(&expiredDomain).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	_, certPEM, keyPEM := selfSigned(t, "expired.test")
	if err := tx.Create(&models.Certificate{
		DomainID:  expiredDomain.ID,
		CertPEM:   string(certPEM),
		KeyPEM:    string(keyPEM),
		Issuer:    "self-signed",
		NotBefore: time.Now().Add(-48 * time.Hour),
		NotAfter:  time.Now().Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	// A domain that does have a usable certificate, to prove serving still works.
	servedDomain := models.Domain{Name: "served-db.test", ServerType: "traditional", Active: true}
	if err := tx.Create(&servedDomain).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	_, servedCertPEM, servedKeyPEM := selfSigned(t, "served-db.test")
	if err := tx.Create(&models.Certificate{
		DomainID:  servedDomain.ID,
		CertPEM:   string(servedCertPEM),
		KeyPEM:    string(servedKeyPEM),
		Issuer:    "self-signed",
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter:  time.Now().Add(24 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	loader := NewDBCertLoader(tx, "", &fallback)

	t.Run("hosted domain with no certificate is refused", func(t *testing.T) {
		cert, err := loader.GetCertificate(&tls.ClientHelloInfo{ServerName: "hosted-nocert.test"})
		if err == nil {
			t.Fatalf("got a certificate for a hosted domain with none of its own, want an error")
		}
		if cert != nil {
			t.Errorf("cert = %v, want nil", cert)
		}
	})

	t.Run("hosted domain whose certificate expired is refused", func(t *testing.T) {
		cert, err := loader.GetCertificate(&tls.ClientHelloInfo{ServerName: "expired.test"})
		if err == nil {
			t.Fatalf("got a certificate for a domain whose certificate expired, want an error")
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
		cert, err := loader.GetCertificate(&tls.ClientHelloInfo{})
		if err != nil {
			t.Fatalf("no-SNI handshake refused: %v", err)
		}
		if cert != &fallback {
			t.Errorf("cert = %v, want the server's own certificate", cert)
		}
	})

	t.Run("a domain with a valid certificate is still served", func(t *testing.T) {
		cert, err := loader.GetCertificate(&tls.ClientHelloInfo{ServerName: "served-db.test"})
		if err != nil {
			t.Fatalf("served-db.test refused: %v", err)
		}
		if cert == nil || len(cert.Certificate) == 0 {
			t.Fatal("no certificate returned for served-db.test")
		}
	})
}
