package acme

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// newAccountTestClient points a client at a fake CA whose port 80 is the
// client's own ChallengeHandler.
func newAccountTestClient(t *testing.T, tx *gorm.DB, ca **fakeCA) *Client {
	t.Helper()
	var handler http.Handler
	*ca = newFakeCA(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	c := NewClient(ClientConfig{DB: tx, MasterKey: testMasterKey, Directory: (*ca).url("/directory")})
	handler = c.ChallengeHandler()
	return c
}

// Every order, from this client or a later one (a restart), uses the one
// stored account: a renewal never opens a new CA account. The key is stored
// encrypted, never as PEM.
func TestObtainCertificate_ReusesStoredAccountKey(t *testing.T) {
	gdb := openStoreTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	const domain = "mail.acme-account.test"
	if err := tx.Create(&models.Domain{Name: domain, ServerType: "traditional", Active: true}).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	var ca *fakeCA
	c := newAccountTestClient(t, tx, &ca)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := c.ObtainCertificate(ctx, domain); err != nil {
		t.Fatalf("first order: %v", err)
	}
	restarted := NewClient(ClientConfig{DB: tx, MasterKey: testMasterKey, Directory: c.directory})
	restarted.responder = c.responder
	if _, err := restarted.ObtainCertificate(ctx, domain); err != nil {
		t.Fatalf("second order: %v", err)
	}
	if len(ca.accounts) != 1 {
		t.Errorf("CA saw %d account keys, want 1", len(ca.accounts))
	}

	var accounts []models.ACMEAccount
	if err := tx.Find(&accounts).Error; err != nil {
		t.Fatalf("read accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Directory != c.directory {
		t.Fatalf("stored accounts = %+v, want one for %s", accounts, c.directory)
	}
	if strings.Contains(accounts[0].KeyPEM, "PRIVATE KEY") {
		t.Error("account key stored in plaintext")
	}
	if _, err := crypto.DecryptString(accounts[0].KeyPEM, testMasterKey); err != nil {
		t.Errorf("stored account key does not decrypt with the master key: %v", err)
	}
}

// Each CA directory has its own account key, so staging and production never
// share an identity.
func TestAccountKey_SeparatePerDirectory(t *testing.T) {
	gdb := openStoreTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	ctx := context.Background()

	prod := NewClient(ClientConfig{DB: tx, MasterKey: testMasterKey})
	staging := NewClient(ClientConfig{DB: tx, MasterKey: testMasterKey, Staging: true})
	prodKey, err := prod.accountKey(ctx)
	if err != nil {
		t.Fatalf("production key: %v", err)
	}
	stagingKey, err := staging.accountKey(ctx)
	if err != nil {
		t.Fatalf("staging key: %v", err)
	}
	if prodKey.Equal(stagingKey) {
		t.Error("staging and production share an account key")
	}
	again, err := prod.accountKey(ctx)
	if err != nil {
		t.Fatalf("production key again: %v", err)
	}
	if !again.Equal(prodKey) {
		t.Error("production account key changed between calls")
	}
}

// A stored key that cannot be used stops the order before it reaches the CA,
// and is never replaced: a new key would be a new CA account.
func TestObtainCertificate_RefusesUnusableStoredAccountKey(t *testing.T) {
	for name, stored := range map[string]string{
		"wrong master key": mustEncrypt(t, "-----BEGIN EC PRIVATE KEY-----\n-----END EC PRIVATE KEY-----\n", "another-master-key"),
		"not a key":        mustEncrypt(t, "garbage", testMasterKey),
	} {
		t.Run(name, func(t *testing.T) {
			gdb := openStoreTestDB(t)
			tx := gdb.Begin()
			t.Cleanup(func() { tx.Rollback() })

			const domain = "mail.acme-badaccount.test"
			if err := tx.Create(&models.Domain{Name: domain, ServerType: "traditional", Active: true}).Error; err != nil {
				t.Fatalf("create domain: %v", err)
			}
			var ca *fakeCA
			c := newAccountTestClient(t, tx, &ca)
			if err := tx.Create(&models.ACMEAccount{Directory: c.directory, KeyPEM: stored}).Error; err != nil {
				t.Fatalf("store account: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := c.ObtainCertificate(ctx, domain)
			if err == nil {
				t.Fatal("ObtainCertificate succeeded with an unusable account key")
			}
			if !strings.Contains(err.Error(), "refusing to replace it") {
				t.Errorf("error = %v, want a refusal to replace the key", err)
			}
			if len(ca.accounts) != 0 {
				t.Errorf("CA saw %d registrations, want none", len(ca.accounts))
			}
			var after models.ACMEAccount
			if err := tx.Where("directory = ?", c.directory).First(&after).Error; err != nil {
				t.Fatalf("read account: %v", err)
			}
			if after.KeyPEM != stored {
				t.Error("unusable account key was replaced")
			}
		})
	}
}

func mustEncrypt(t *testing.T, plaintext, key string) string {
	t.Helper()
	out, err := crypto.EncryptString(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return out
}
