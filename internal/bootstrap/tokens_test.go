package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

const chosenPassword = "a passphrase only the owner knows"

func migrateTokens(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	if err := gdb.AutoMigrate(&models.OneTimeToken{}, &models.TwoFactor{},
		&models.TwoFactorRecoveryCode{}, &models.RefreshToken{}); err != nil {
		t.Skipf("token DB test skipped: migrate failed (%v)", err)
	}
	if err := gdb.Exec("TRUNCATE one_time_tokens RESTART IDENTITY").Error; err != nil {
		t.Fatal(err)
	}
}

func TestSetupToken_ClaimsAnUnclaimedServerOnce(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)

	token, expires, err := MintSetupToken(gdb, 0)
	if err != nil {
		t.Fatal(err)
	}
	if until := time.Until(expires); until <= 0 || until > DefaultTokenLifetime {
		t.Errorf("expires in %v, want within %v", until, DefaultTokenLifetime)
	}

	admin, err := ClaimServer(gdb, token, "owner", chosenPassword, true)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if auth.CheckPassword(chosenPassword, admin.PasswordHash) != nil {
		t.Error("the first admin does not have the password its owner chose")
	}
	if !admin.TwoFactorRequired || admin.PasswordChangeRequired {
		t.Errorf("first admin: two_factor_required=%v password_change_required=%v; "+
			"it chose its own password but must still enroll 2FA",
			admin.TwoFactorRequired, admin.PasswordChangeRequired)
	}
	var withRoles models.AdminUser
	gdb.Preload("Roles").First(&withRoles, admin.ID)
	if len(withRoles.Roles) != 1 || withRoles.Roles[0].Name != "superadmin" {
		t.Errorf("roles = %+v, want superadmin", withRoles.Roles)
	}

	// Spent: the same token claims nothing again.
	if _, err := ClaimServer(gdb, token, "someone", chosenPassword, true); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("second redemption: err = %v, want ErrTokenInvalid", err)
	}
	// And a claimed server mints no more setup tokens.
	if _, _, err := MintSetupToken(gdb, 0); !errors.Is(err, ErrAlreadyClaimed) {
		t.Errorf("mint on a claimed server: err = %v, want ErrAlreadyClaimed", err)
	}
}

// Two people (or a person and a script) racing with the same link: exactly one
// becomes the admin.
func TestSetupToken_RacingRedemptionsHaveOneWinner(t *testing.T) {
	gdb := openScratchDatabase(t)
	mustRBAC(t, gdb)
	token, _, err := MintSetupToken(gdb, 0)
	if err != nil {
		t.Fatal(err)
	}

	const racers = 8
	var wg sync.WaitGroup
	results := make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each racer on its own connection, as separate HTTP requests are.
			// Different names, so only the token can stop a second winner.
			_, err := ClaimServer(gdb, token, fmt.Sprintf("racer-%d", i), chosenPassword, true)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)

	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d racers claimed the server with one token, want exactly 1", wins)
	}
}

func TestSetupToken_RefusesExpiredWrongPurposeAndBadInput(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)

	expired, _, err := MintSetupToken(gdb, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := ClaimServer(gdb, expired, "owner", chosenPassword, true); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expired token: err = %v, want ErrTokenInvalid", err)
	}

	// Refused input leaves the token unspent: the owner fixes the password and tries again.
	token, _, _ := MintSetupToken(gdb, 0)
	if _, err := ClaimServer(gdb, token, "owner", "short", true); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("short password: err = %v", err)
	}
	if _, err := ClaimServer(gdb, token, "", chosenPassword, true); !errors.Is(err, ErrUsernameRequired) {
		t.Errorf("no username: err = %v", err)
	}
	if _, err := ClaimServer(gdb, token, "owner", chosenPassword, false); err == nil {
		t.Error("claimed a server where 2FA is off; its admin could never finish setup")
	}
	// A setup token is no reset token.
	if _, err := ResetAdmin(gdb, token, chosenPassword); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("setup token used to reset: err = %v, want ErrTokenInvalid", err)
	}
	if _, err := ClaimServer(gdb, token, "owner", chosenPassword, true); err != nil {
		t.Errorf("the token should still work after refused attempts: %v", err)
	}
}

// The hand-over: whoever set the server up held the admin's password,
// authenticator and sessions. A reset redeemed by the owner makes all of them
// worthless.
func TestResetToken_LocksOutWhoeverHeldTheAccount(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)

	setup, _, _ := MintSetupToken(gdb, 0)
	admin, err := ClaimServer(gdb, setup, "admin", "the agent's password 123", true)
	if err != nil {
		t.Fatal(err)
	}
	// What the agent holds: an authenticator and a live session.
	if err := gdb.Create(&models.TwoFactor{UserType: models.TwoFactorUserTypeAdmin, SubjectID: admin.ID,
		EncryptedSecret: "x", Confirmed: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Create(&models.RefreshToken{Jti: "agent-session", UserType: "admin", SubjectID: admin.ID,
		Status: models.RefreshTokenActive, ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}

	if _, _, err := MintResetToken(gdb, "nobody", 0); !errors.Is(err, ErrNoSuchAdmin) {
		t.Errorf("reset for an unknown admin: err = %v, want ErrNoSuchAdmin", err)
	}
	reset, _, err := MintResetToken(gdb, "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResetAdmin(gdb, reset, chosenPassword); err != nil {
		t.Fatalf("reset: %v", err)
	}

	var after models.AdminUser
	gdb.First(&after, admin.ID)
	if auth.CheckPassword("the agent's password 123", after.PasswordHash) == nil {
		t.Error("the old password still works")
	}
	if auth.CheckPassword(chosenPassword, after.PasswordHash) != nil {
		t.Error("the owner's password does not")
	}
	var enrollments int64
	gdb.Model(&models.TwoFactor{}).Where("user_type = ? AND subject_id = ?", models.TwoFactorUserTypeAdmin, admin.ID).Count(&enrollments)
	if enrollments != 0 {
		t.Error("the old authenticator survived the reset")
	}
	if !after.TwoFactorRequired {
		t.Error("the owner is not made to enroll their own authenticator")
	}
	var session models.RefreshToken
	gdb.Where("jti = ?", "agent-session").First(&session)
	if session.Status != models.RefreshTokenRevoked {
		t.Errorf("the old session is %q, want revoked", session.Status)
	}
	if _, err := ResetAdmin(gdb, reset, "another passphrase entirely"); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("reusing the reset token: err = %v, want ErrTokenInvalid", err)
	}
}

func TestTokens_StoredOnlyAsHashes(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	token, _, _ := MintSetupToken(gdb, 0)

	var row models.OneTimeToken
	gdb.First(&row)
	if row.TokenHash == token || row.TokenHash != hashToken(token) {
		t.Fatal("the token is stored as itself, or not as its hash")
	}
}

// openScratchDatabase gives a test a whole database of its own, dropped
// afterwards. For tests that need real concurrent connections, which the
// rolled-back transaction of openBootstrapTestDB (one connection) cannot give,
// without committing rows into the database other packages share.
func openScratchDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	port, _ := strconv.Atoi(envOr("DB_PORT", "5432"))
	cfg := config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: port,
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	}
	admin, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Skipf("scratch database skipped: no database reachable (%v)", err)
	}
	name := fmt.Sprintf("restmail_scratch_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Skipf("scratch database skipped: cannot create a database (%v)", err)
	}
	cfg.DBName = name
	scratch, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Fatalf("connect to scratch database: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := scratch.DB(); err == nil {
			_ = sqlDB.Close()
		}
		admin.Exec("DROP DATABASE IF EXISTS " + name)
		if sqlDB, err := admin.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := scratch.AutoMigrate(&models.AdminUser{}, &models.Role{}, &models.Capability{},
		&models.UserRole{}, &models.RoleCapability{}, &models.OneTimeToken{}); err != nil {
		t.Fatalf("migrate scratch database: %v", err)
	}
	return scratch
}
