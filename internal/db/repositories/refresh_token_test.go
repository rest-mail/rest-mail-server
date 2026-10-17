package repositories

import (
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// openRefreshTokenTestDB connects to the unit-test Postgres and migrates the
// refresh_tokens table. It skips (never fails) when no database is reachable,
// matching the repo's depless-local / DB-in-CI convention.
func openRefreshTokenTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("refresh-token repo test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.RefreshToken{}); err != nil {
		t.Skipf("refresh-token repo test skipped: migrate failed (%v)", err)
	}
	return gdb
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func saveActive(t *testing.T, r *RefreshTokenRepository, jti, userType string, subject uint) {
	t.Helper()
	if err := r.Save(&models.RefreshToken{
		Jti:       jti,
		UserType:  userType,
		SubjectID: subject,
		SessionID: "sid-" + jti,
		Status:    models.RefreshTokenActive,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// TestRefreshTokenRepository_SessionIDRequired: every refresh token belongs to
// a session (#367 refuses a token without a sid), so the ledger refuses a row
// whose session_id is empty or NULL.
func TestRefreshTokenRepository_SessionIDRequired(t *testing.T) {
	gdb := openRefreshTokenTestDB(t)
	r := NewRefreshTokenRepository(gdb)
	jti := "nosid-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Cleanup(func() { gdb.Where("jti LIKE ?", jti+"%").Delete(&models.RefreshToken{}) })

	if err := r.Save(&models.RefreshToken{
		Jti: jti, UserType: "mailbox", SubjectID: 1,
		Status: models.RefreshTokenActive, ExpiresAt: time.Now().Add(time.Hour),
	}); err == nil {
		t.Error("saved a refresh token with an empty session_id")
	}
	if err := gdb.Exec(`INSERT INTO refresh_tokens (jti, user_type, subject_id, session_id, status, expires_at, created_at, updated_at)
		VALUES (?, 'mailbox', 1, NULL, 'active', now(), now(), now())`, jti+"-null").Error; err == nil {
		t.Error("stored a refresh token with a NULL session_id")
	}
	if err := r.Save(&models.RefreshToken{
		Jti: jti + "-ok", UserType: "mailbox", SubjectID: 1, SessionID: "sid-" + jti,
		Status: models.RefreshTokenActive, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Errorf("a refresh token with a session_id was refused: %v", err)
	}
}

func TestRefreshTokenRepository_RotateIsSingleUse(t *testing.T) {
	gdb := openRefreshTokenTestDB(t)
	r := NewRefreshTokenRepository(gdb)
	jti := "rot-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Cleanup(func() { gdb.Where("jti = ?", jti).Delete(&models.RefreshToken{}) })

	saveActive(t, r, jti, "mailbox", 101)

	// First rotate succeeds and flips active→rotated.
	if err := r.Rotate(jti); err != nil {
		t.Fatalf("first Rotate: %v", err)
	}
	rec, err := r.GetByJTI(jti)
	if err != nil {
		t.Fatalf("GetByJTI: %v", err)
	}
	if rec.Status != models.RefreshTokenRotated {
		t.Errorf("status = %q, want rotated", rec.Status)
	}

	// Second rotate of the same (now rotated) token is refused — single use.
	if err := r.Rotate(jti); !errors.Is(err, ErrRefreshTokenNotFound) {
		t.Errorf("second Rotate = %v, want ErrRefreshTokenNotFound", err)
	}
}

func TestRefreshTokenRepository_RevokeBlocksRotate(t *testing.T) {
	gdb := openRefreshTokenTestDB(t)
	r := NewRefreshTokenRepository(gdb)
	jti := "rev-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Cleanup(func() { gdb.Where("jti = ?", jti).Delete(&models.RefreshToken{}) })

	saveActive(t, r, jti, "mailbox", 102)

	if err := r.Revoke(jti); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// A revoked token can no longer be rotated (refresh would be refused).
	if err := r.Rotate(jti); !errors.Is(err, ErrRefreshTokenNotFound) {
		t.Errorf("Rotate after Revoke = %v, want ErrRefreshTokenNotFound", err)
	}
}

func TestRefreshTokenRepository_GetByJTIUnknown(t *testing.T) {
	gdb := openRefreshTokenTestDB(t)
	r := NewRefreshTokenRepository(gdb)
	if _, err := r.GetByJTI("does-not-exist-" + strconv.FormatInt(time.Now().UnixNano(), 36)); !errors.Is(err, ErrRefreshTokenNotFound) {
		t.Errorf("GetByJTI(unknown) = %v, want ErrRefreshTokenNotFound", err)
	}
}

func TestRefreshTokenRepository_RevokeAllForSubject(t *testing.T) {
	gdb := openRefreshTokenTestDB(t)
	r := NewRefreshTokenRepository(gdb)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	jtiA, jtiB := "sub-a-"+suffix, "sub-b-"+suffix
	t.Cleanup(func() { gdb.Where("jti IN ?", []string{jtiA, jtiB}).Delete(&models.RefreshToken{}) })

	saveActive(t, r, jtiA, "admin", 500)
	saveActive(t, r, jtiB, "admin", 500)

	if err := r.RevokeAllForSubject("admin", 500); err != nil {
		t.Fatalf("RevokeAllForSubject: %v", err)
	}
	for _, jti := range []string{jtiA, jtiB} {
		if err := r.Rotate(jti); !errors.Is(err, ErrRefreshTokenNotFound) {
			t.Errorf("Rotate(%s) after bulk revoke = %v, want ErrRefreshTokenNotFound", jti, err)
		}
	}
}
