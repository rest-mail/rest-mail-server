package db

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// plaintextDKIMPEM is a legacy-style plaintext key value. The migration only
// needs it to look like a PEM block ("-----BEGIN…"); its bytes are treated as
// opaque and round-tripped through encrypt/decrypt.
const plaintextDKIMPEM = "-----BEGIN RSA PRIVATE KEY-----\nMOCKKEYMATERIAL\n-----END RSA PRIVATE KEY-----\n"

func dbEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func dbEnvIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// openDKIMMigrationTestDB connects to the Postgres provisioned for unit tests
// (CI sets DB_*; defaults match a local `postgres` container). It skips — never
// fails — when no database is reachable, per the repo's DB-test convention.
func openDKIMMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: dbEnvOr("DB_HOST", "localhost"),
		DBPort: dbEnvIntOr("DB_PORT", 5432),
		DBName: dbEnvOr("DB_NAME", "restmail"),
		DBUser: dbEnvOr("DB_USER", "restmail"),
		DBPass: dbEnvOr("DB_PASS", "restmail"),
	}
	gdb, err := Connect(cfg)
	if err != nil {
		t.Skipf("DKIM migration DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}); err != nil {
		t.Skipf("DKIM migration DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// TestEncryptPlaintextDKIMKeys_MigratesAndIsIdempotent seeds a domain with a
// plaintext DKIM key, runs the at-rest encryption migration, and asserts the row
// becomes versioned ciphertext that decrypts back to the original — then that a
// second run leaves that row byte-for-byte unchanged (no double-encryption).
func TestEncryptPlaintextDKIMKeys_MigratesAndIsIdempotent(t *testing.T) {
	gdb := openDKIMMigrationTestDB(t)

	// Isolate in a transaction that rolls back, leaving no residue in the
	// shared test database.
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	const masterKey = "db-test-master-key"
	name := fmt.Sprintf("osi8-migration-%d.test", time.Now().UnixNano())
	dom := models.Domain{Name: name, DKIMSelector: "sel", DKIMPrivateKey: plaintextDKIMPEM}
	if err := tx.Create(&dom).Error; err != nil {
		t.Fatalf("seed domain: %v", err)
	}

	// ── First run: plaintext must be encrypted in place ──────────────────────
	n, err := encryptPlaintextDKIMKeys(tx, masterKey)
	if err != nil {
		t.Fatalf("first migration run: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected >=1 migrated row, got %d", n)
	}

	var afterFirst models.Domain
	if err := tx.First(&afterFirst, dom.ID).Error; err != nil {
		t.Fatalf("reload after first run: %v", err)
	}
	if !models.DKIMKeyIsEncrypted(afterFirst.DKIMPrivateKey) {
		t.Fatalf("row not encrypted after migration: %q", afterFirst.DKIMPrivateKey)
	}
	got, err := models.LoadDKIMPrivateKey(afterFirst.DKIMPrivateKey, masterKey)
	if err != nil {
		t.Fatalf("load migrated key: %v", err)
	}
	if got != plaintextDKIMPEM {
		t.Fatal("migrated key did not decrypt back to the original plaintext")
	}

	// ── Second run: idempotent, no double-encryption ─────────────────────────
	if _, err := encryptPlaintextDKIMKeys(tx, masterKey); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
	var afterSecond models.Domain
	if err := tx.First(&afterSecond, dom.ID).Error; err != nil {
		t.Fatalf("reload after second run: %v", err)
	}
	if afterSecond.DKIMPrivateKey != afterFirst.DKIMPrivateKey {
		t.Fatal("second run mutated an already-encrypted row (not idempotent)")
	}
	// A double-encrypted value would decrypt to a dkim:v1: ciphertext, not the
	// original plaintext — assert we still recover the plaintext exactly.
	got2, err := models.LoadDKIMPrivateKey(afterSecond.DKIMPrivateKey, masterKey)
	if err != nil {
		t.Fatalf("load after second run: %v", err)
	}
	if got2 != plaintextDKIMPEM {
		t.Fatal("value was double-encrypted: does not decrypt to original plaintext")
	}
}
