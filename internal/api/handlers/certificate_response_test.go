package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// Moving off database models must preserve the production wire representation,
// including association presence and the different precision of detail dates.
func TestCertificateResponseKeepsExistingJSON(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 34, 56, 123456000, time.UTC)
	for _, domain := range []models.Domain{{}, {
		ID: 7, Name: "certificate.test", ServerType: "restmail", Active: true,
		DefaultQuotaBytes: 1024, DKIMSelector: "mail", DKIMPrivateKey: "dkim-secret-sentinel",
		SendingPolicy: "bulk", CreatedAt: now, UpdatedAt: now,
	}} {
		cert := models.Certificate{ID: 9, DomainID: 7, CertPEM: "public-cert", KeyPEM: "tls-secret-sentinel",
			Issuer: "issuer", NotBefore: now, NotAfter: now.Add(time.Hour), AutoRenew: true, CreatedAt: now, UpdatedAt: now, Domain: domain}
		assertJSON := func(got, want interface{}) {
			t.Helper()
			actual, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			if string(actual) != string(expected) {
				t.Fatalf("JSON changed:\ngot  %s\nwant %s", actual, expected)
			}
		}
		assertJSON(certificateOf(cert), cert)
		// This is the previous details response, including its omitzero association.
		oldDetails := struct {
			ID        uint          `json:"id"`
			DomainID  uint          `json:"domain_id"`
			CertPEM   string        `json:"cert_pem"`
			Issuer    string        `json:"issuer"`
			NotBefore string        `json:"not_before"`
			NotAfter  string        `json:"not_after"`
			AutoRenew bool          `json:"auto_renew"`
			CreatedAt string        `json:"created_at"`
			UpdatedAt string        `json:"updated_at"`
			Domain    models.Domain `json:"domain,omitzero"`
		}{cert.ID, cert.DomainID, cert.CertPEM, cert.Issuer, cert.NotBefore.Format(time.RFC3339), cert.NotAfter.Format(time.RFC3339), cert.AutoRenew, cert.CreatedAt.Format(time.RFC3339), cert.UpdatedAt.Format(time.RFC3339), domain}
		assertJSON(certificateDetailsOf(cert), oldDetails)
	}
}
