package handlers

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// throwawayKey is a fresh private key, made for one test and never kept.
func throwawayKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// throwawayCertificate is a self-signed certificate for key, naming cn and sans.
func throwawayCertificate(t *testing.T, key *ecdsa.PrivateKey, cn string, sans []string, notBefore, notAfter time.Time) string {
	t.Helper()
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		DNSNames: sans, NotBefore: notBefore, NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// An upload the gateways could not serve the domain with is refused where the
// person uploading it sees why, instead of turning into failed handshakes later
// (issue #1704): a key that is not the certificate's own, a certificate that
// does not name the domain, and one that has already expired.
func TestCertificateUpload_RefusesWhatCannotBeServed(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.Certificate{}); err != nil {
		t.Skipf("certificate upload test skipped: migrate failed (%v)", err)
	}
	parent := fmt.Sprintf("upload-%d.test", time.Now().UnixNano())
	domain := "mx." + parent
	key, keyPEM := throwawayKey(t)
	_, otherPEM := throwawayKey(t)
	now := time.Now()
	current := func(cn string, sans ...string) string {
		return throwawayCertificate(t, key, cn, sans, now.Add(-time.Hour), now.Add(30*24*time.Hour))
	}

	for _, tc := range []struct {
		name, certPEM, keyPEM string
		field                 string // empty: accepted
		says                  string
	}{
		{name: "the domain's own pair", certPEM: current(domain, domain), keyPEM: keyPEM},
		{name: "a wildcard over the domain", certPEM: current("*."+parent, "*."+parent), keyPEM: keyPEM},
		{name: "a common name when there are no alternative names", certPEM: current(domain), keyPEM: keyPEM},
		{name: "another certificate's key", certPEM: current(domain, domain), keyPEM: otherPEM,
			field: "key_pem", says: "does not belong to this certificate"},
		{name: "a key that is not a key", certPEM: current(domain, domain), keyPEM: "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
			field: "key_pem", says: "Failed to read the private key"},
		{name: "the mail host's name only", certPEM: current("mail."+domain, "mail."+domain), keyPEM: keyPEM,
			field: "cert_pem", says: "does not name " + domain},
		{name: "a wildcard one level too deep", certPEM: current("*."+domain, "*."+domain), keyPEM: keyPEM,
			field: "cert_pem", says: "does not name " + domain},
		{name: "the common name, overridden by alternative names", certPEM: current(domain, "other."+parent), keyPEM: keyPEM,
			field: "cert_pem", says: "does not name " + domain},
		{name: "an expired certificate", certPEM: throwawayCertificate(t, key, domain, []string{domain}, now.Add(-60*24*time.Hour), now.Add(-time.Hour)),
			keyPEM: keyPEM, field: "cert_pem", says: "expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := gdb.Begin()
			t.Cleanup(func() { tx.Rollback() })
			d := models.Domain{Name: domain, ServerType: "traditional"}
			if err := tx.Create(&d).Error; err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]interface{}{"domain_id": d.ID, "cert_pem": tc.certPEM, "key_pem": tc.keyPEM})
			rr := httptest.NewRecorder()
			certificateRoutes(NewCertificateHandler(tx, "")).ServeHTTP(rr,
				httptest.NewRequest(http.MethodPost, "/api/v1/admin/certificates", bytes.NewReader(body)))
			if strings.Contains(rr.Body.String(), "PRIVATE KEY") {
				t.Fatal("the answer carries private key material")
			}
			var stored int64
			if err := tx.Model(&models.Certificate{}).Where("domain_id = ?", d.ID).Count(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if tc.field == "" {
				if rr.Code != http.StatusCreated || stored != 1 {
					t.Fatalf("status %d, %d stored, want 201 and one row: %s", rr.Code, stored, rr.Body)
				}
				return
			}
			if rr.Code != http.StatusUnprocessableEntity || stored != 0 {
				t.Fatalf("status %d, %d stored, want 422 and nothing kept: %s", rr.Code, stored, rr.Body)
			}
			var got struct {
				Error struct {
					Code    string `json:"code"`
					Details struct {
						Fields map[string]string `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Error.Code != "validation_failed" || !strings.Contains(got.Error.Details.Fields[tc.field], tc.says) {
				t.Fatalf("answered %s, want validation_failed naming %s with %q", rr.Body, tc.field, tc.says)
			}
		})
	}
}
