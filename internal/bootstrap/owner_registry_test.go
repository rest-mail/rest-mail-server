package bootstrap

import (
	"errors"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

func emptyOwnerRegistry(t *testing.T) *gorm.DB {
	t.Helper()
	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&models.OwnerKey{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("1 = 1").Delete(&models.OwnerKey{}).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestOwnerRegistry_SeedOnceAndKeepRevocation(t *testing.T) {
	db := emptyOwnerRegistry(t)
	owner, replacement := newSigner(t), newSigner(t)
	// Comments and duplicate lines do not create duplicate authorities.
	if err := SeedOwnerKeys(db, authorized(owner)+" laptop\n"+authorized(owner)); err != nil {
		t.Fatal(err)
	}
	if err := SeedOwnerKeys(db, authorized(replacement)); err != nil {
		t.Fatal(err)
	}
	keys, err := LoadOwnerKeys(db)
	if err != nil || keys.Len() != 1 || !keys.has(owner.PublicKey()) || keys.has(replacement.PublicKey()) {
		t.Fatalf("changed startup config replaced the registered owner: keys=%v err=%v", keys, err)
	}
	fingerprint := ssh.FingerprintSHA256(owner.PublicKey())
	if err := RevokeOwnerKey(db, fingerprint, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SeedOwnerKeys(db, authorized(owner)); err != nil {
		t.Fatal(err)
	}
	keys, err = LoadOwnerKeys(db)
	if err != nil || keys.Len() != 0 {
		t.Fatalf("restart restored a revoked owner: keys=%v err=%v", keys, err)
	}
	// Restoring a revoked key requires an explicit trusted operator action.
	if _, err := RegisterOwnerKeys(db, authorized(owner)); err != nil {
		t.Fatal(err)
	}
	keys, err = LoadOwnerKeys(db)
	if err != nil || !keys.has(owner.PublicKey()) {
		t.Fatalf("explicit registration failed: %v", err)
	}
}

func TestOwnerRegistry_RecoverySurvivesMissingBootstrapInput(t *testing.T) {
	db := emptyOwnerRegistry(t)
	migrateTokens(t, db)
	mustRBAC(t, db)
	newAdmin(t, db, adminAddr, "the installed administrator password")
	owner := newSigner(t)
	if err := SeedOwnerKeys(db, authorized(owner)); err != nil {
		t.Fatal(err)
	}
	// A new process on another platform receives no owner-key configuration.
	if err := SeedOwnerKeys(db, ""); err != nil {
		t.Fatal(err)
	}
	keys, err := LoadOwnerKeys(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := MintForOwner(db, keys, challengeKey, signed(t, owner, "reset-admin", adminAddr), time.Now()); err != nil {
		t.Fatalf("database-backed ownership did not survive restart: %v", err)
	}
}

func TestOwnerRegistry_InvalidInputIsAtomic(t *testing.T) {
	db := emptyOwnerRegistry(t)
	owner := newSigner(t)
	for _, register := range []func(*gorm.DB, string) error{
		SeedOwnerKeys,
		func(db *gorm.DB, spec string) error { _, err := RegisterOwnerKeys(db, spec); return err },
	} {
		if err := register(db, authorized(owner)+"\nnot a public key"); err == nil {
			t.Fatal("invalid bundle was accepted")
		}
		var count int64
		if err := db.Model(&models.OwnerKey{}).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("invalid bundle partially changed registry: count=%d err=%v", count, err)
		}
	}
}

func TestOwnerRegistry_AuditFailureRollsBackEnrollment(t *testing.T) {
	db := emptyOwnerRegistry(t)
	const callback = "owner-registry-test-audit-failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (models.AuditEntry{}).TableName() {
			tx.AddError(errors.New("audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove(callback) })
	if err := SeedOwnerKeys(db, authorized(newSigner(t))); err == nil {
		t.Fatal("enrollment succeeded without its audit record")
	}
	var count int64
	if err := db.Model(&models.OwnerKey{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("unaudited enrollment persisted: count=%d err=%v", count, err)
	}
}
