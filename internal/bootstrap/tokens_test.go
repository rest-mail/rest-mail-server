package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

const chosenPassword = "a passphrase only the owner knows"

func migrateTokens(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	if err := gdb.AutoMigrate(&models.OneTimeToken{}, &models.Credential{}, &models.RefreshToken{}); err != nil {
		t.Skipf("token DB test skipped: migrate failed (%v)", err)
	}
	if err := gdb.Exec("TRUNCATE one_time_tokens RESTART IDENTITY").Error; err != nil {
		t.Fatal(err)
	}
}

// newAdmin makes an admin the way a server has one: an account at address,
// with its password, superadmin at "*", and an authenticator still owed.
func newAdmin(t *testing.T, gdb *gorm.DB, address, password string) *models.Mailbox {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	var superadmin models.Role
	if err := gdb.Where("name = ?", "superadmin").First(&superadmin).Error; err != nil {
		t.Fatalf("superadmin role: %v", err)
	}
	local, domainName, _ := strings.Cut(address, "@")
	domain, err := ensureDomain(gdb, domainName)
	if err != nil {
		t.Fatal(err)
	}
	admin := models.Mailbox{DomainID: domain.ID, LocalPart: local, Address: address, Active: true, TwoFactorRequired: true}
	if err := gdb.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.NewCredentialRepository(gdb).SetPassword(admin.ID, hash, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Create(&models.Grant{
		HolderKind: models.GrantHolderAccount, HolderID: admin.ID, RoleID: superadmin.ID,
		Scope: "*", Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	return &admin
}

// Two people (or a person and a script) racing with the same link: exactly one
// chooses the admin's password.
func TestResetToken_RacingRedemptionsHaveOneWinner(t *testing.T) {
	gdb := openScratchDatabase(t)
	mustRBAC(t, gdb)
	newAdmin(t, gdb, adminAddr, "the first admin's password")
	token, _, err := MintResetToken(gdb, adminAddr, 0)
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
			// Different passwords, so only the token can stop a second winner.
			_, err := ResetAdmin(gdb, token, fmt.Sprintf("racer %d's passphrase", i))
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
		t.Fatalf("%d racers reset the admin with one token, want exactly 1", wins)
	}
}

func TestResetToken_RefusesExpiredTokensAndBadInput(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	newAdmin(t, gdb, adminAddr, "the first admin's password")

	expired, _, err := MintResetToken(gdb, adminAddr, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := ResetAdmin(gdb, expired, chosenPassword); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expired token: err = %v, want ErrTokenInvalid", err)
	}

	// Refused input leaves the token unspent: the owner fixes the password and tries again.
	token, expires, _ := MintResetToken(gdb, adminAddr, 0)
	if until := time.Until(expires); until <= 0 || until > DefaultTokenLifetime {
		t.Errorf("expires in %v, want within %v", until, DefaultTokenLifetime)
	}
	if _, err := ResetAdmin(gdb, token, "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("short password: err = %v", err)
	}
	if _, err := ResetAdmin(gdb, token, chosenPassword); err != nil {
		t.Errorf("the token should still work after a refused attempt: %v", err)
	}
}

// A one-time token of any purpose but reset_admin is no way in.
func TestResetToken_AnotherPurposeIsNoWayIn(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	newAdmin(t, gdb, adminAddr, "the first admin's password")
	for _, purpose := range []string{"setup", "setup_code"} {
		token := "other-purpose-" + purpose
		if err := gdb.Create(&models.OneTimeToken{Purpose: purpose, TokenHash: hashToken(token),
			ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := ResetAdmin(gdb, token, chosenPassword); !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("a %s token reset the admin: err = %v, want ErrTokenInvalid", purpose, err)
		}
	}
}

// The hand-over: whoever set the server up held the admin's password,
// authenticator and sessions. A reset redeemed by the owner makes all of them
// worthless.
func TestResetToken_LocksOutWhoeverHeldTheAccount(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)

	admin := newAdmin(t, gdb, adminAddr, "the agent's password 123")
	// What the agent holds: an authenticator, an app password and a live
	// session.
	if _, err := repositories.NewTwoFactorRepository(gdb).Enroll(admin.ID, "x", nil); err != nil {
		t.Fatal(err)
	}
	gdb.Model(&models.Credential{}).Where("account_id = ? AND kind = ?", admin.ID, models.CredentialKindTOTP).Update("confirmed_at", time.Now())
	if _, _, err := repositories.NewCredentialRepository(gdb).CreateAppPassword(admin.ID, admin.ID, "agent", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Create(&models.RefreshToken{Jti: "agent-session", AccountID: admin.ID,
		SessionID: "agent-session", Status: models.RefreshTokenActive, ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}

	if _, _, err := MintResetToken(gdb, "nobody@example.com", 0); !errors.Is(err, ErrNoSuchAdmin) {
		t.Errorf("reset for an unknown admin: err = %v, want ErrNoSuchAdmin", err)
	}
	reset, _, err := MintResetToken(gdb, adminAddr, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResetAdmin(gdb, reset, chosenPassword); err != nil {
		t.Fatalf("reset: %v", err)
	}

	var after models.Mailbox
	gdb.First(&after, admin.ID)
	if auth.CheckPassword("the agent's password 123", passwordOf(t, gdb, admin.ID)) == nil {
		t.Error("the old password still works")
	}
	if auth.CheckPassword(chosenPassword, passwordOf(t, gdb, admin.ID)) != nil {
		t.Error("the owner's password does not")
	}
	var live int64
	gdb.Model(&models.Credential{}).Where("account_id = ? AND kind IN ? AND revoked_at IS NULL", admin.ID,
		[]string{models.CredentialKindTOTP, models.CredentialKindAppPassword}).Count(&live)
	if live != 0 {
		t.Error("the old authenticator or app password survived the reset")
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
	newAdmin(t, gdb, adminAddr, "the first admin's password")
	token, _, _ := MintResetToken(gdb, adminAddr, 0)

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
	if err := scratch.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.Pipeline{}, &models.Role{}, &models.Capability{},
		&models.RoleCapability{}, &models.Grant{}, &models.GrantActivation{}, &models.OneTimeToken{}, &models.Credential{},
		&models.RefreshToken{}); err != nil {
		t.Fatalf("migrate scratch database: %v", err)
	}
	if err := rmdb.MigrateAuditLog(scratch); err != nil {
		t.Fatalf("migrate scratch database: %v", err)
	}
	return scratch
}
