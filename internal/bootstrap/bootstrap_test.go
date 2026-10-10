package bootstrap

import (
	"os"
	"strconv"
	"strings"
	"testing"

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
		&models.UserRole{}, &models.RoleCapability{}); err != nil {
		t.Skipf("bootstrap DB test skipped: migrate failed (%v)", err)
	}
	// One rolled-back transaction per test: "no admin exists" is a fact about the
	// whole database, and other packages' tests share it and run in parallel.
	// Inside the transaction the emptied tables are invisible to them, and the
	// TRUNCATE's lock makes them wait for the rollback instead of colliding.
	gdb = gdb.Begin()
	t.Cleanup(func() { gdb.Rollback() })
	// Named by the models, not by hand: a wrong name fails the TRUNCATE.
	tables := strings.Join([]string{
		models.UserRole{}.TableName(), models.RoleCapability{}.TableName(),
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

	created, err := EnsureFirstAdmin(gdb, "admin", "", true)
	if err != nil || created {
		t.Fatalf("created=%v err=%v, want no admin and no error", created, err)
	}
	if n := countAdmins(t, gdb); n != 0 {
		t.Fatalf("admins = %d, want 0", n)
	}
}

func TestEnsureFirstAdmin_RefusesShortPassword(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	mustRBAC(t, gdb)

	_, err := EnsureFirstAdmin(gdb, "admin", "changeme", true)
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

	created, err := EnsureFirstAdmin(gdb, "", strongPassword, true)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v, want a new admin", created, err)
	}

	var user models.AdminUser
	if err := gdb.Preload("Roles").Where("username = ?", "admin").First(&user).Error; err != nil {
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
	if len(user.Roles) != 1 || user.Roles[0].Name != "superadmin" {
		t.Errorf("roles = %+v, want exactly superadmin", user.Roles)
	}
}

// The password stays in the deployment's Secret for as long as the deployment
// exists. Changing it, or restarting, must never touch the account again —
// otherwise whoever can edit the Secret could reset the admin's password.
func TestEnsureFirstAdmin_NeverActsOnceAnAdminExists(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	mustRBAC(t, gdb)

	if _, err := EnsureFirstAdmin(gdb, "admin", strongPassword, true); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureFirstAdmin(gdb, "admin", "a-different-password-entirely", true)
	if err != nil || created {
		t.Fatalf("second call: created=%v err=%v, want a no-op", created, err)
	}
	var user models.AdminUser
	gdb.Where("username = ?", "admin").First(&user)
	if err := auth.CheckPassword(strongPassword, user.PasswordHash); err != nil {
		t.Error("second call changed the admin's password")
	}

	// Nor does a different username add a second admin.
	if created, _ := EnsureFirstAdmin(gdb, "someone-else", strongPassword, true); created {
		t.Error("created a second admin while one already existed")
	}
	if n := countAdmins(t, gdb); n != 1 {
		t.Fatalf("admins = %d, want 1", n)
	}
}

func TestEnsureFirstAdmin_NeedsRolesFirst(t *testing.T) {
	gdb := openBootstrapTestDB(t)

	if _, err := EnsureFirstAdmin(gdb, "admin", strongPassword, true); err == nil {
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

	_, err := EnsureFirstAdmin(gdb, "admin", strongPassword, false)
	if err == nil || !strings.Contains(err.Error(), "TOTP_2FA_ENABLED") {
		t.Fatalf("err = %v, want a refusal naming TOTP_2FA_ENABLED", err)
	}
	if n := countAdmins(t, gdb); n != 0 {
		t.Fatalf("admins = %d, want 0", n)
	}
}
