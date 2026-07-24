package main

import (
	"os"
	"strconv"
	"testing"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/crypto"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// openRotateTestDB connects to the unit-test Postgres and migrates the tables
// rotate-key touches. It skips (never fails) when no database is reachable,
// matching the repo's depless-local / DB-in-CI convention.
func openRotateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: envIntOr("DB_PORT", 5432),
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("rotate-key test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.TwoFactor{}, &models.Certificate{}, &models.DKIMKey{}); err != nil {
		t.Skipf("rotate-key test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// TestRotate_ReEncryptsDKIMAndTwoFactor is the regression guard for #165: a
// rotation must re-encrypt the columns the running server actually reads —
// domains.dkim_private_key and two_factor.encrypted_secret — not just the
// unused dkim_keys table. After rotating from oldKey to newKey, both secrets
// must decrypt under newKey and no longer under oldKey.
//
// The whole test runs inside a transaction that is rolled back, so it never
// mutates the shared test database (rotate() scans every row, but any change is
// discarded; rows encrypted under a different key simply fail to decrypt and are
// left untouched).
func TestRotate_ReEncryptsDKIMAndTwoFactor(t *testing.T) {
	base := openRotateTestDB(t)

	const (
		oldKey    = "rotate-test-old-master-key"
		newKey    = "rotate-test-new-master-key"
		dkimPEM   = "-----BEGIN PRIVATE KEY-----\nMIIrotateKeyTestFakeDKIMPrivateKeyMaterial\n-----END PRIVATE KEY-----\n"
		totpPlain = "JBSWY3DPEHPK3PXP"
	)

	tx := base.Begin()
	if tx.Error != nil {
		t.Fatalf("begin tx: %v", tx.Error)
	}
	defer tx.Rollback()

	// Seed a domain whose DKIM key and a 2FA enrollment are encrypted under oldKey.
	storedDKIM, err := models.EncryptDKIMPrivateKey(dkimPEM, oldKey)
	if err != nil {
		t.Fatalf("EncryptDKIMPrivateKey: %v", err)
	}
	dom := models.Domain{Name: "rotate-test.example", DKIMSelector: "sel1", DKIMPrivateKey: storedDKIM}
	if err := tx.Create(&dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	storedSecret, err := crypto.EncryptString(totpPlain, oldKey)
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}
	tf := models.TwoFactor{
		UserType:        models.TwoFactorUserTypeMailbox,
		SubjectID:       987654321, // unlikely to collide within the rolled-back tx
		EncryptedSecret: storedSecret,
		Confirmed:       true,
	}
	if err := tx.Create(&tf).Error; err != nil {
		t.Fatalf("create two_factor: %v", err)
	}

	// Rotate within the transaction.
	if _, err := rotate(tx, oldKey, newKey); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// Reload the seeded rows and prove both secrets now decrypt under newKey.
	var gotDom models.Domain
	if err := tx.First(&gotDom, dom.ID).Error; err != nil {
		t.Fatalf("reload domain: %v", err)
	}
	loaded, err := models.LoadDKIMPrivateKey(gotDom.DKIMPrivateKey, newKey)
	if err != nil {
		t.Fatalf("DKIM key does not decrypt under the new key after rotation: %v", err)
	}
	if loaded != dkimPEM {
		t.Fatalf("rotated DKIM key mismatch: got %q want %q", loaded, dkimPEM)
	}
	// And no longer under the retired old key (proves it was actually re-encrypted).
	if _, err := models.LoadDKIMPrivateKey(gotDom.DKIMPrivateKey, oldKey); err == nil {
		t.Fatal("DKIM key still decrypts under the OLD key after rotation — it was not re-encrypted")
	}

	var gotTF models.TwoFactor
	if err := tx.First(&gotTF, tf.ID).Error; err != nil {
		t.Fatalf("reload two_factor: %v", err)
	}
	secret, err := crypto.DecryptString(gotTF.EncryptedSecret, newKey)
	if err != nil {
		t.Fatalf("2FA secret does not decrypt under the new key after rotation: %v", err)
	}
	if secret != totpPlain {
		t.Fatalf("rotated 2FA secret mismatch: got %q want %q", secret, totpPlain)
	}
	if _, err := crypto.DecryptString(gotTF.EncryptedSecret, oldKey); err == nil {
		t.Fatal("2FA secret still decrypts under the OLD key after rotation — it was not re-encrypted")
	}
}
