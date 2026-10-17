package bootstrap

import (
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

const strongPassword = "correct-horse-battery-staple"

// openBootstrapTestDB connects to the unit-test Postgres and empties the RBAC
// tables inside a transaction, so each test starts from a fresh install. It skips (never fails) when
// no database is reachable, matching the repo's depless-local / DB-in-CI
// convention.
func openBootstrapTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	port, _ := strconv.Atoi(envOr("DB_PORT", "5432"))
	gdb, err := rmdb.Connect(&config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: port,
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	})
	if err != nil {
		t.Skipf("bootstrap DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.AdminUser{}, &models.Role{}, &models.Capability{},
		&models.UserRole{}, &models.RoleCapability{}, &models.Grant{}, &models.RefreshToken{}); err != nil {
		t.Skipf("bootstrap DB test skipped: migrate failed (%v)", err)
	}
	// Grants made at bootstrap are recorded in the audit log.
	if err := rmdb.MigrateAuditLog(gdb); err != nil {
		t.Skipf("bootstrap DB test skipped: audit log migrate failed (%v)", err)
	}
	// One rolled-back transaction per test: "no admin exists" is a fact about the
	// whole database, and other packages' tests share it and run in parallel.
	// Inside the transaction the emptied tables are invisible to them, and the
	// TRUNCATE's lock makes them wait for the rollback instead of colliding.
	gdb = gdb.Begin()
	t.Cleanup(func() { gdb.Rollback() })
	// Named by the models, not by hand: a wrong name fails the TRUNCATE.
	tables := strings.Join([]string{
		models.Grant{}.TableName(), models.UserRole{}.TableName(), models.RoleCapability{}.TableName(),
		models.AdminUser{}.TableName(), models.Role{}.TableName(), models.Capability{}.TableName(),
	}, ", ")
	if err := gdb.Exec("TRUNCATE " + tables + " RESTART IDENTITY CASCADE").Error; err != nil {
		t.Fatalf("empty RBAC tables: %v", err)
	}
	return gdb
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func countAdmins(t *testing.T, gdb *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := gdb.Model(&models.AdminUser{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// t0 is when the server first starts, in every test that cares about time.
var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

func adminNamed(t *testing.T, gdb *gorm.DB, username string) models.AdminUser {
	t.Helper()
	var user models.AdminUser
	if err := gdb.Where("username = ?", username).First(&user).Error; err != nil {
		t.Fatal(err)
	}
	return user
}

func mustRBAC(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	if err := EnsureRBAC(gdb); err != nil {
		t.Fatalf("EnsureRBAC: %v", err)
	}
}

func TestEnsureRBAC_IdempotentAndSuperadminHasWildcard(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	mustRBAC(t, gdb)

	var roles, caps, links int64
	gdb.Model(&models.Role{}).Count(&roles)
	gdb.Model(&models.Capability{}).Count(&caps)
	gdb.Model(&models.RoleCapability{}).Count(&links)

	// Every API start runs this; the second run must change nothing.
	mustRBAC(t, gdb)
	var roles2, caps2, links2 int64
	gdb.Model(&models.Role{}).Count(&roles2)
	gdb.Model(&models.Capability{}).Count(&caps2)
	gdb.Model(&models.RoleCapability{}).Count(&links2)
	if roles != roles2 || caps != caps2 || links != links2 {
		t.Fatalf("second run changed rows: roles %d→%d caps %d→%d links %d→%d", roles, roles2, caps, caps2, links, links2)
	}
	if roles != 3 {
		t.Errorf("roles = %d, want 3 (superadmin, admin, readonly)", roles)
	}

	var wildcard int64
	var superadmin models.Role
	var star models.Capability
	gdb.Where("name = ?", "superadmin").First(&superadmin)
	gdb.Where("name = ?", "*").First(&star)
	gdb.Model(&models.RoleCapability{}).
		Where("role_id = ? AND capability_id = ?", superadmin.ID, star.ID).
		Count(&wildcard)
	if wildcard != 1 {
		t.Errorf("superadmin wildcard links = %d, want 1", wildcard)
	}
}

func TestEnsureFirstAdmin_NoPasswordDoesNothing(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	mustRBAC(t, gdb)

	outcome, err := EnsureFirstAdmin(gdb, "admin", "", true, t0)
	if err != nil || outcome != FirstAdminUnchanged {
		t.Fatalf("outcome=%v err=%v, want no admin and no error", outcome, err)
	}
	if n := countAdmins(t, gdb); n != 0 {
		t.Fatalf("admins = %d, want 0", n)
	}
}

// A server given no bootstrap password, with no admin, has no way in at all:
// the API refuses to start in production rather than run like that.
func TestRequireAdmin_NoAdminIsNoWayIn(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	mustRBAC(t, gdb)
	if err := RequireAdmin(gdb); !errors.Is(err, ErrNoAdmin) {
		t.Fatalf("no admin: err = %v, want ErrNoAdmin", err)
	}
	if _, err := EnsureFirstAdmin(gdb, "admin", "a-bootstrap-password-of-length", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := RequireAdmin(gdb); err != nil {
		t.Errorf("with the bootstrap admin: err = %v, want nil", err)
	}
}

func TestEnsureFirstAdmin_RefusesShortPassword(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	mustRBAC(t, gdb)

	_, err := EnsureFirstAdmin(gdb, "admin", "changeme", true, t0)
	if err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("err = %v, want a too-short refusal", err)
	}
	if n := countAdmins(t, gdb); n != 0 {
		t.Fatalf("admins = %d after refusal, want 0", n)
	}
}

func TestEnsureFirstAdmin_CreatesSuperadminOnFreshInstall(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	mustRBAC(t, gdb)

	outcome, err := EnsureFirstAdmin(gdb, "", strongPassword, true, t0)
	if err != nil || outcome != FirstAdminCreated {
		t.Fatalf("outcome=%v err=%v, want a new admin", outcome, err)
	}

	var user models.AdminUser
	if err := gdb.Where("username = ?", "admin").First(&user).Error; err != nil {
		t.Fatalf("empty username should default to admin: %v", err)
	}
	if !user.Active {
		t.Error("bootstrap admin is not active")
	}
	if !user.PasswordChangeRequired {
		t.Error("bootstrap admin may keep the deploy-time password; it must be forced to change it")
	}
	if !user.TwoFactorRequired {
		t.Error("bootstrap admin may skip 2FA; it must be forced to enroll an authenticator")
	}
	if err := auth.CheckPassword(strongPassword, user.PasswordHash); err != nil {
		t.Errorf("stored hash does not verify the bootstrap password: %v", err)
	}
	// Superadmin and admin, both at "*" and both active: superadmin becomes
	// eligible only once it can be switched on (#346).
	var held []struct{ Name, Scope, Mode string }
	if err := gdb.Table("grants g").Select("r.name, g.scope, g.mode").
		Joins("JOIN admin_roles r ON r.id = g.role_id").
		Where("g.holder_kind = ? AND g.holder_id = ?", models.GrantHolderAdmin, user.ID).
		Order("r.name").Scan(&held).Error; err != nil {
		t.Fatal(err)
	}
	want := []struct{ Name, Scope, Mode string }{{"admin", "*", "active"}, {"superadmin", "*", "active"}}
	if !reflect.DeepEqual(held, want) {
		t.Errorf("grants = %+v, want %+v", held, want)
	}
	// Each recorded, by the system, in the same transaction.
	var recorded int64
	if err := gdb.Model(&models.AuditEntry{}).
		Where("action = ? AND actor_kind = ? AND target->>'holder_id' = ?", "grants.create", models.AuditActorSystem, strconv.FormatUint(uint64(user.ID), 10)).
		Count(&recorded).Error; err != nil {
		t.Fatal(err)
	}
	if recorded != 2 {
		t.Errorf("audit entries for the bootstrap grants = %d, want 2", recorded)
	}
	if want := t0.Add(BootstrapPasswordLifetime); user.BootstrapExpiresAt == nil || !user.BootstrapExpiresAt.Equal(want) {
		t.Errorf("bootstrap_expires_at = %v, want %v", user.BootstrapExpiresAt, want)
	}
}

// Restarting with the same password changes nothing — in particular it does
// not push the deadline back, or a server restarted daily would never expire.
func TestEnsureFirstAdmin_RestartDoesNotExtendTheDeadline(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	mustRBAC(t, gdb)

	if _, err := EnsureFirstAdmin(gdb, "admin", strongPassword, true, t0); err != nil {
		t.Fatal(err)
	}
	before := adminNamed(t, gdb, "admin")
	outcome, err := EnsureFirstAdmin(gdb, "admin", strongPassword, true, t0.Add(48*time.Hour))
	if err != nil || outcome != FirstAdminUnchanged {
		t.Fatalf("restart: outcome=%v err=%v, want a no-op", outcome, err)
	}
	after := adminNamed(t, gdb, "admin")
	if !after.BootstrapExpiresAt.Equal(*before.BootstrapExpiresAt) || after.CredentialEpoch != before.CredentialEpoch {
		t.Errorf("restart moved the deadline %v -> %v or the epoch %d -> %d",
			before.BootstrapExpiresAt, after.BootstrapExpiresAt, before.CredentialEpoch, after.CredentialEpoch)
	}
}

// Deploying again with a new password, before anyone has finished the install,
// is the recovery for one that leaked or ran out. It replaces the password,
// signs out whoever used the old one, and starts the clock again.
func TestEnsureFirstAdmin_NewPasswordRearmsAnUnfinishedAdmin(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	mustRBAC(t, gdb)

	if _, err := EnsureFirstAdmin(gdb, "admin", strongPassword, true, t0); err != nil {
		t.Fatal(err)
	}
	before := adminNamed(t, gdb, "admin")
	session := models.RefreshToken{Jti: "rearm-" + strconv.FormatInt(time.Now().UnixNano(), 36), UserType: "admin",
		SubjectID: before.ID, Status: models.RefreshTokenActive, ExpiresAt: t0.Add(24 * time.Hour)}
	if err := gdb.Create(&session).Error; err != nil {
		t.Fatal(err)
	}

	later := t0.Add(100 * time.Hour) // after the first password expired
	outcome, err := EnsureFirstAdmin(gdb, "admin", "a-different-password-entirely", true, later)
	if err != nil || outcome != FirstAdminRearmed {
		t.Fatalf("outcome=%v err=%v, want rearmed", outcome, err)
	}
	after := adminNamed(t, gdb, "admin")
	if auth.CheckPassword("a-different-password-entirely", after.PasswordHash) != nil {
		t.Error("the new password does not work")
	}
	if auth.CheckPassword(strongPassword, after.PasswordHash) == nil {
		t.Error("the old password still works")
	}
	if want := later.Add(BootstrapPasswordLifetime); after.BootstrapExpiresAt == nil || !after.BootstrapExpiresAt.Equal(want) {
		t.Errorf("bootstrap_expires_at = %v, want %v", after.BootstrapExpiresAt, want)
	}
	if after.CredentialEpoch != before.CredentialEpoch+1 {
		t.Errorf("credential epoch %d -> %d, want it bumped", before.CredentialEpoch, after.CredentialEpoch)
	}
	if !after.PasswordChangeRequired || !after.TwoFactorRequired {
		t.Error("re-arming let the account out of its setup")
	}
	var status string
	gdb.Model(&models.RefreshToken{}).Where("jti = ?", session.Jti).Pluck("status", &status)
	if status == models.RefreshTokenActive {
		t.Error("a session opened with the old password survived the re-arm")
	}
}

// Once the admin has chosen its own password the deployment's is history:
// changing or restarting must never touch the account again, or whoever can
// edit the Secret could reset the admin's password. Nor is a second admin
// ever added.
// A deployment that holds only a hash of the password makes the same admin:
// the installer's password signs in, and the account starts in setup.
func TestEnsureFirstAdminWith_AHash(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	mustRBAC(t, gdb)
	hash := hashOf(t, strongPassword, 10)
	credential, err := BootstrapPasswordHash(hash)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := EnsureFirstAdminWith(gdb, "admin", credential, true, t0)
	if err != nil || outcome != FirstAdminCreated {
		t.Fatalf("outcome=%v err=%v, want created", outcome, err)
	}
	admin := adminNamed(t, gdb, "admin")
	if auth.CheckPassword(strongPassword, admin.PasswordHash) != nil {
		t.Error("the installer's password does not sign in")
	}
	if !admin.PasswordChangeRequired || !admin.TwoFactorRequired || admin.BootstrapExpiresAt == nil {
		t.Errorf("not in setup: %+v", admin)
	}

	// The same hash again, as every restart gives it: nothing changes.
	if outcome, err := EnsureFirstAdminWith(gdb, "admin", credential, true, t0.Add(time.Hour)); err != nil || outcome != FirstAdminUnchanged {
		t.Errorf("restart: outcome=%v err=%v, want unchanged", outcome, err)
	}
	// A new deployment's hash re-arms it, as a new password does.
	next, _ := BootstrapPasswordHash(hashOf(t, "a-different-password-entirely", 10))
	if outcome, err := EnsureFirstAdminWith(gdb, "admin", next, true, t0.Add(100*time.Hour)); err != nil || outcome != FirstAdminRearmed {
		t.Fatalf("new hash: outcome=%v err=%v, want rearmed", outcome, err)
	}
	after := adminNamed(t, gdb, "admin")
	if auth.CheckPassword("a-different-password-entirely", after.PasswordHash) != nil || auth.CheckPassword(strongPassword, after.PasswordHash) == nil {
		t.Error("after re-arming, the new password should work and the old should not")
	}
}

func TestEnsureFirstAdmin_NeverActsOnceTheAdminHasChosenAPassword(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	mustRBAC(t, gdb)

	if _, err := EnsureFirstAdmin(gdb, "admin", strongPassword, true, t0); err != nil {
		t.Fatal(err)
	}
	chosen, err := auth.HashPassword("the-admins-own-password")
	if err != nil {
		t.Fatal(err)
	}
	// What POST /auth/password does.
	if err := gdb.Model(&models.AdminUser{}).Where("username = ?", "admin").Updates(map[string]interface{}{
		"password_hash": chosen, "password_change_required": false, "bootstrap_expires_at": nil,
	}).Error; err != nil {
		t.Fatal(err)
	}

	outcome, err := EnsureFirstAdmin(gdb, "admin", "a-different-password-entirely", true, t0.Add(time.Hour))
	if err != nil || outcome != FirstAdminUnchanged {
		t.Fatalf("outcome=%v err=%v, want a no-op", outcome, err)
	}
	if auth.CheckPassword("the-admins-own-password", adminNamed(t, gdb, "admin").PasswordHash) != nil {
		t.Error("the deployment's password replaced one the admin chose")
	}

	if outcome, _ := EnsureFirstAdmin(gdb, "someone-else", strongPassword, true, t0); outcome != FirstAdminUnchanged {
		t.Error("created a second admin while one already existed")
	}
	if n := countAdmins(t, gdb); n != 1 {
		t.Fatalf("admins = %d, want 1", n)
	}
}

// A different username is a different account, not the bootstrap admin: it is
// neither created alongside nor re-armed.
func TestEnsureFirstAdmin_OnlyRearmsTheBootstrapUsername(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	mustRBAC(t, gdb)

	if _, err := EnsureFirstAdmin(gdb, "admin", strongPassword, true, t0); err != nil {
		t.Fatal(err)
	}
	if outcome, err := EnsureFirstAdmin(gdb, "someone-else", "a-different-password-entirely", true, t0); err != nil || outcome != FirstAdminUnchanged {
		t.Fatalf("outcome=%v err=%v, want a no-op", outcome, err)
	}
	if auth.CheckPassword(strongPassword, adminNamed(t, gdb, "admin").PasswordHash) != nil {
		t.Error("another username's password replaced the bootstrap admin's")
	}
	if n := countAdmins(t, gdb); n != 1 {
		t.Fatalf("admins = %d, want 1", n)
	}
}

func TestEnsureFirstAdmin_NeedsRolesFirst(t *testing.T) {
	gdb := openBootstrapTestDB(t)

	if _, err := EnsureFirstAdmin(gdb, "admin", strongPassword, true, t0); err == nil {
		t.Fatal("created an admin with no superadmin role to give it")
	}
	if n := countAdmins(t, gdb); n != 0 {
		t.Fatalf("admins = %d, want 0", n)
	}
}

// Without 2FA enrollment the bootstrap admin could never finish its setup and
// would be locked out for good, so refuse rather than create it.
func TestEnsureFirstAdmin_RefusesWhenTwoFactorIsOff(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	mustRBAC(t, gdb)

	_, err := EnsureFirstAdmin(gdb, "admin", strongPassword, false, t0)
	if err == nil || !strings.Contains(err.Error(), "TOTP_2FA_ENABLED") {
		t.Fatalf("err = %v, want a refusal naming TOTP_2FA_ENABLED", err)
	}
	if n := countAdmins(t, gdb); n != 0 {
		t.Fatalf("admins = %d, want 0", n)
	}
}
