package acme

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
)

// ObtainCertificate answers HTTP-01 through ChallengeHandler, the handler the
// API mounts at /.well-known/acme-challenge/, and stores the issued pair with
// its key encrypted. Before #123 it accepted the challenge without serving the
// answer, so a real CA could never validate it.
func TestObtainCertificate_ServesChallengeThroughChallengeHandler(t *testing.T) {
	gdb := openStoreTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	const domain = "mail.acme-obtain.test"
	if err := tx.Create(&models.Domain{Name: domain, ServerType: "traditional", Active: true}).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	// The CA reaches the client the way it would on port 80: through the
	// handler the router mounts, never the client's internals.
	var handler http.Handler
	ca := newFakeCA(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	c := NewClient(ClientConfig{DB: tx, MasterKey: testMasterKey, Email: "admin@acme-obtain.test", Directory: ca.url("/directory")})
	handler = c.ChallengeHandler()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cert, err := c.ObtainCertificate(ctx, domain)
	if err != nil {
		t.Fatalf("ObtainCertificate: %v", err)
	}

	if got := ca.fetched[domain]; ca.thumbprint == "" || !strings.HasSuffix(got, "."+ca.thumbprint) {
		t.Errorf("CA fetched %q for %s, want <token>.%s", got, domain, ca.thumbprint)
	}
	var stored models.Certificate
	if err := tx.First(&stored, cert.ID).Error; err != nil {
		t.Fatalf("read stored certificate: %v", err)
	}
	if stored.Issuer != "letsencrypt" || !stored.AutoRenew {
		t.Errorf("stored issuer=%q auto_renew=%v, want letsencrypt and true", stored.Issuer, stored.AutoRenew)
	}
	keyPEM, err := crypto.DecryptString(stored.KeyPEM, testMasterKey)
	if err != nil {
		t.Fatalf("stored key does not decrypt with the master key: %v", err)
	}
	if _, err := validateCertificate(domain, stored.CertPEM, keyPEM, time.Now()); err != nil {
		t.Errorf("stored pair is not servable for %s: %v", domain, err)
	}

	// The answer is withdrawn once the order is done.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, challengePathPrefix+"token-1", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("answered challenge still served: %d", rec.Code)
	}
}

// A CA that cannot reach ChallengeHandler fails the order, and nothing is
// stored.
func TestObtainCertificate_UnreachableChallengeStoresNothing(t *testing.T) {
	gdb := openStoreTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	const domain = "mail.acme-unreachable.test"
	if err := tx.Create(&models.Domain{Name: domain, ServerType: "traditional", Active: true}).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	ca := newFakeCA(t, http.NotFoundHandler())
	c := NewClient(ClientConfig{DB: tx, MasterKey: testMasterKey, Directory: ca.url("/directory")})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.ObtainCertificate(ctx, domain); err == nil {
		t.Fatal("ObtainCertificate succeeded though the CA could not fetch the challenge")
	}
	var n int64
	if err := tx.Model(&models.Certificate{}).Joins("JOIN domains ON domains.id = certificates.domain_id").
		Where("domains.name = ?", domain).Count(&n).Error; err != nil {
		t.Fatalf("count certificates: %v", err)
	}
	if n != 0 {
		t.Errorf("%d certificate rows stored after a failed order, want 0", n)
	}
}
