package acme

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	rmcrypto "github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
)

func servingSPKI(t *testing.T, certPEM string) [32]byte {
	t.Helper()
	b, _ := pem.Decode([]byte(certPEM))
	if b == nil {
		t.Fatal("certificate missing")
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(c.RawSubjectPublicKeyInfo)
}

// Exercise real HTTP-01 orders and CSRs for the key forms accepted by the
// certificate store. TLSA 3 1 1 hashes must survive renewal, including renewal
// of an expired leaf, rather than merely preserving the CA account identity.
func TestIssue_PreservesServingKeyAndTLSA(t *testing.T) {
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]crypto.Signer{"ECDSA": newECKey(t), "RSA": newRSAKey(t), "Ed25519": ed} {
		t.Run(name, func(t *testing.T) {
			const domain = "mail.dane.test"
			oldCert := certFor(t, key, domain, []string{domain}, time.Now().Add(-time.Hour))
			storedKey, err := ParseServingKey(oldCert, keyPEMFor(t, key))
			if err != nil {
				t.Fatal(err)
			}
			responder := NewHTTP01Responder()
			ca := newFakeCA(t, responder)
			cfg := IssueConfig{Directory: ca.url("/directory"), Domains: []string{domain}, AccountKey: newECKey(t), CertificateKey: storedKey}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for i := 0; i < 2; i++ {
				cert, encoded, err := Issue(ctx, cfg, responder)
				if err != nil {
					t.Fatal(err)
				}
				if cert == oldCert || servingSPKI(t, cert) != servingSPKI(t, oldCert) {
					t.Fatal("renewal did not replace the expired leaf while preserving its TLSA SPKI hash")
				}
				cfg.CertificateKey, err = ParseServingKey(cert, encoded)
				if err != nil {
					t.Fatal(err)
				}
			}
			if ca.nextID != 2 || len(ca.accounts) != 1 {
				t.Fatal("expected two certificate orders under one CA account")
			}
		})
	}
}

func TestRenewalKey_StoredFormsAndRefusals(t *testing.T) {
	const domain = "mail.dane.test"
	key := newECKey(t)
	plain := keyPEMFor(t, key)
	cert := certFor(t, key, domain, []string{domain}, time.Now().Add(-time.Hour))
	encrypted, err := rmcrypto.EncryptString(plain, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, master, cert, key string
		wantError               bool
	}{
		{"plaintext", "", cert, plain, false},
		{"legacy plaintext after enabling master key", testMasterKey, cert, plain, false},
		{"encrypted", testMasterKey, cert, encrypted, false},
		{"wrong master key", "wrong-master", cert, encrypted, true},
		{"missing master key", "", cert, encrypted, true},
		{"missing key", testMasterKey, cert, "", true},
		{"damaged key", "", cert, "private material must not leak", true},
		{"mismatched key", "", cert, keyPEMFor(t, newECKey(t)), true},
		{"missing certificate", "", "", plain, true},
		{"extra key block", "", cert, plain + plain, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{masterKey: tc.master}
			stored := &models.Certificate{CertPEM: tc.cert, KeyPEM: tc.key, Domain: models.Domain{Name: domain}}
			got, err := c.renewalKey(stored)
			if (err != nil) != tc.wantError {
				t.Fatalf("renewal key error=%v, wantError=%v", err, tc.wantError)
			}
			if !tc.wantError {
				der, err := x509.MarshalPKIXPublicKey(got.Public())
				if err != nil || sha256.Sum256(der) != servingSPKI(t, cert) {
					t.Fatal("stored serving key changed")
				}
			} else {
				if got != nil || strings.Contains(err.Error(), "PRIVATE KEY") || strings.Contains(err.Error(), "private material") {
					t.Fatal("refusal returned a key or leaked key material")
				}
				// With no DB or CA configured, this also proves refusal happens
				// before a new account/order/store or old-record deletion.
				if err := c.RenewCertificate(context.Background(), stored); err == nil {
					t.Fatal("renewal silently replaced an unusable key")
				}
			}
		})
	}
}

func TestRenewCertificate_PreservesEncryptedServingKey(t *testing.T) {
	gdb := openStoreTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	const domain = "mail.dane-renewal.test"
	d := models.Domain{Name: domain, ServerType: "traditional", Active: true}
	if err := tx.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	var ca *fakeCA
	c := newAccountTestClient(t, tx, &ca)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	old, err := c.ObtainCertificate(ctx, domain)
	if err != nil {
		t.Fatal(err)
	}
	want := servingSPKI(t, old.CertPEM)
	if err := c.RenewCertificate(ctx, old); err != nil {
		t.Fatal(err)
	}
	var rows []models.Certificate
	if err := tx.Where("domain_id = ?", d.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID == old.ID || servingSPKI(t, rows[0].CertPEM) != want {
		t.Fatal("renewal did not replace the old row while retaining its TLSA SPKI")
	}
	if strings.Contains(rows[0].KeyPEM, "PRIVATE KEY") {
		t.Fatal("renewed private key stored in plaintext")
	}
	if _, err := c.renewalKey(&rows[0]); err != nil {
		t.Fatal("renewed encrypted pair cannot be reused")
	}
}
