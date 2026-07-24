package repositories

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// openTwoFactorTestDB connects to the unit-test Postgres and migrates the 2FA
// tables. It skips (never fails) when no database is reachable, matching the
// repo's depless-local / DB-in-CI convention.
func openTwoFactorTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("two-factor repo test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.TwoFactor{}, &models.TwoFactorRecoveryCode{}); err != nil {
		t.Skipf("two-factor repo test skipped: migrate failed (%v)", err)
	}
	return gdb
}

func enrollForTest(t *testing.T, r *TwoFactorRepository, subject uint) ([]string, *models.TwoFactor) {
	t.Helper()
	plain, err := auth.GenerateRecoveryCodes()
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	hashes := make([]string, len(plain))
	for i, c := range plain {
		h, herr := auth.HashRecoveryCode(c)
		if herr != nil {
			t.Fatalf("HashRecoveryCode: %v", herr)
		}
		hashes[i] = h
	}
	tf, err := r.Enroll(models.TwoFactorUserTypeMailbox, subject, "enc-secret-"+strconv.Itoa(int(subject)), hashes)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	return plain, tf
}

// TestTwoFactor_EnrollPendingThenConfirm: a fresh enrollment is pending
// (GetActive misses) until Confirm flips it active.
func TestTwoFactor_EnrollPendingThenConfirm(t *testing.T) {
	gdb := openTwoFactorTestDB(t)
	r := NewTwoFactorRepository(gdb)
	subject := uint(time.Now().UnixNano() % 1_000_000)
	t.Cleanup(func() { _ = r.Delete(models.TwoFactorUserTypeMailbox, subject) })

	_, tf := enrollForTest(t, r, subject)

	if got, err := r.Get(models.TwoFactorUserTypeMailbox, subject); err != nil || got.Confirmed {
		t.Fatalf("Get after enroll: got=%+v err=%v; want pending row", got, err)
	}
	if _, err := r.GetActive(models.TwoFactorUserTypeMailbox, subject); !errors.Is(err, ErrTwoFactorNotFound) {
		t.Errorf("GetActive on pending = %v, want ErrTwoFactorNotFound", err)
	}

	if err := r.Confirm(tf.ID); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	active, err := r.GetActive(models.TwoFactorUserTypeMailbox, subject)
	if err != nil {
		t.Fatalf("GetActive after confirm: %v", err)
	}
	if !active.Confirmed || active.ConfirmedAt == nil {
		t.Errorf("confirmed row missing Confirmed/ConfirmedAt: %+v", active)
	}

	// Confirming again is refused (single transition).
	if err := r.Confirm(tf.ID); !errors.Is(err, ErrTwoFactorNotFound) {
		t.Errorf("second Confirm = %v, want ErrTwoFactorNotFound", err)
	}
}

// TestTwoFactor_RecoveryCodeSingleUse: a recovery code is redeemable once; a
// second redemption and an unknown code both fail.
func TestTwoFactor_RecoveryCodeSingleUse(t *testing.T) {
	gdb := openTwoFactorTestDB(t)
	r := NewTwoFactorRepository(gdb)
	subject := uint(time.Now().UnixNano()%1_000_000) + 1
	t.Cleanup(func() { _ = r.Delete(models.TwoFactorUserTypeMailbox, subject) })

	plain, tf := enrollForTest(t, r, subject)

	if used, err := r.ConsumeRecoveryCode(tf.ID, plain[0]); err != nil || !used {
		t.Fatalf("first ConsumeRecoveryCode: used=%v err=%v", used, err)
	}
	if used, err := r.ConsumeRecoveryCode(tf.ID, plain[0]); err != nil || used {
		t.Errorf("reused recovery code: used=%v err=%v, want used=false", used, err)
	}
	// A different, still-unused code works.
	if used, err := r.ConsumeRecoveryCode(tf.ID, plain[1]); err != nil || !used {
		t.Errorf("second distinct code: used=%v err=%v, want used=true", used, err)
	}
	// An unknown code never matches.
	if used, err := r.ConsumeRecoveryCode(tf.ID, "zzzzz-zzzzz"); err != nil || used {
		t.Errorf("unknown code: used=%v err=%v, want used=false", used, err)
	}
}

// TestTwoFactor_ReenrollReplacesPriorCodes: re-enrolling wipes the previous
// enrollment and its recovery codes, so old codes stop working.
func TestTwoFactor_ReenrollReplacesPriorCodes(t *testing.T) {
	gdb := openTwoFactorTestDB(t)
	r := NewTwoFactorRepository(gdb)
	subject := uint(time.Now().UnixNano()%1_000_000) + 2
	t.Cleanup(func() { _ = r.Delete(models.TwoFactorUserTypeMailbox, subject) })

	oldPlain, _ := enrollForTest(t, r, subject)
	_, newTF := enrollForTest(t, r, subject)

	// Old recovery code no longer matches the (replaced) enrollment.
	if used, err := r.ConsumeRecoveryCode(newTF.ID, oldPlain[0]); err != nil || used {
		t.Errorf("stale recovery code after re-enroll: used=%v err=%v, want used=false", used, err)
	}
	// Exactly one enrollment row remains for the owner.
	var count int64
	gdb.Model(&models.TwoFactor{}).Where("user_type = ? AND subject_id = ?", models.TwoFactorUserTypeMailbox, subject).Count(&count)
	if count != 1 {
		t.Errorf("enrollment rows for owner = %d, want 1", count)
	}
}

// TestTwoFactor_DeleteRemovesEnrollmentAndCodes: disable removes the enrollment
// and leaves no orphan recovery-code rows.
func TestTwoFactor_DeleteRemovesEnrollmentAndCodes(t *testing.T) {
	gdb := openTwoFactorTestDB(t)
	r := NewTwoFactorRepository(gdb)
	subject := uint(time.Now().UnixNano()%1_000_000) + 3
	t.Cleanup(func() { _ = r.Delete(models.TwoFactorUserTypeMailbox, subject) })

	_, tf := enrollForTest(t, r, subject)
	if err := r.Delete(models.TwoFactorUserTypeMailbox, subject); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := r.Get(models.TwoFactorUserTypeMailbox, subject); !errors.Is(err, ErrTwoFactorNotFound) {
		t.Errorf("Get after delete = %v, want ErrTwoFactorNotFound", err)
	}
	var codeCount int64
	gdb.Model(&models.TwoFactorRecoveryCode{}).Where("two_factor_id = ?", tf.ID).Count(&codeCount)
	if codeCount != 0 {
		t.Errorf("orphan recovery codes after delete = %d, want 0", codeCount)
	}
}
