package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

// revSeedCounter uniquifies fixture identifiers so tests do not collide on a
// persistent (CI) database across runs or within a run.
var revSeedCounter int64

func revUniq() string {
	n := atomic.AddInt64(&revSeedCounter, 1)
	return strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatInt(n, 36)
}

// openRevocationTestDB connects to the unit-test Postgres and migrates the
// tables the session-revocation tests touch. It skips (never fails) when no
// database is reachable, matching the repo's depless-local / DB-in-CI
// convention.
func openRevocationTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("session-revocation DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(
		&models.Domain{},
		&models.Mailbox{},
		&models.WebmailAccount{},
		&models.QuotaUsage{},
		&models.RefreshToken{},
		&models.AdminUser{},
		&models.Role{},
		&models.Capability{},
		&models.UserRole{},
		&models.RoleCapability{},
	); err != nil {
		t.Skipf("session-revocation DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// seedMailbox creates an active mailbox under a throwaway domain and returns it.
// The label only aids debugging; the actual address is uniquified so tests never
// collide on a persistent database.
func seedMailbox(t *testing.T, db *gorm.DB, label string) models.Mailbox {
	t.Helper()
	tok := revUniq()
	domain := models.Domain{Name: "rev-" + tok + ".test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	mb := models.Mailbox{
		DomainID:    domain.ID,
		LocalPart:   label,
		Address:     label + "-" + tok + "@" + domain.Name,
		Password:    hash,
		DisplayName: "User",
		Active:      true,
	}
	if err := db.Create(&mb).Error; err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	return mb
}

// seedAdminWithCaps creates an active admin user holding a single role that
// grants the given capability names, and returns the admin. The label only aids
// debugging; the username and role name are uniquified.
func seedAdminWithCaps(t *testing.T, db *gorm.DB, label string, capNames []string) models.AdminUser {
	t.Helper()
	tok := revUniq()
	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	admin := models.AdminUser{Username: label + "-" + tok, PasswordHash: hash, Active: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	role := models.Role{Name: "role-" + label + "-" + tok}
	if err := db.Create(&role).Error; err != nil {
		t.Fatalf("create role: %v", err)
	}
	if err := db.Create(&models.UserRole{UserID: admin.ID, RoleID: role.ID}).Error; err != nil {
		t.Fatalf("assign role: %v", err)
	}
	setRoleCaps(t, db, role.ID, capNames)
	return admin
}

// setRoleCaps replaces the capabilities granted by a role with capNames.
func setRoleCaps(t *testing.T, db *gorm.DB, roleID uint, capNames []string) {
	t.Helper()
	if err := db.Where("role_id = ?", roleID).Delete(&models.RoleCapability{}).Error; err != nil {
		t.Fatalf("clear role caps: %v", err)
	}
	for _, name := range capNames {
		cap := models.Capability{Name: name}
		if err := db.Where("name = ?", name).FirstOrCreate(&cap).Error; err != nil {
			t.Fatalf("create cap %q: %v", name, err)
		}
		if err := db.Create(&models.RoleCapability{RoleID: roleID, CapabilityID: cap.ID}).Error; err != nil {
			t.Fatalf("grant cap %q: %v", name, err)
		}
	}
}

// seedLedger persists an active refresh-token ledger row for a freshly issued
// pair so it can be exchanged at /auth/refresh.
func seedLedger(t *testing.T, db *gorm.DB, pair *auth.TokenPair, userType string, subjectID uint) {
	t.Helper()
	if err := repositories.NewRefreshTokenRepository(db).Save(&models.RefreshToken{
		Jti:       pair.RefreshJTI,
		UserType:  userType,
		SubjectID: subjectID,
		Status:    models.RefreshTokenActive,
		ExpiresAt: pair.RefreshExpiresAt,
	}); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
}

func newDBAuthHandler(db *gorm.DB, jwt *auth.JWTService) *AuthHandler {
	return NewAuthHandler(db, jwt, "")
}

// TestRefresh_DisabledMailboxRejected: a still-valid refresh token must stop
// working once the mailbox is disabled. Before the fix, Refresh re-minted tokens
// straight from the refresh claims without re-checking account state, so a
// disabled account kept a live session until the token naturally expired.
func TestRefresh_DisabledMailboxRejected(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	h := newDBAuthHandler(db, jwt)

	mb := seedMailbox(t, db, "disabled")
	pair, err := jwt.GenerateTokenPair(mb.ID, mb.Address, 0)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, "mailbox", mb.ID)

	// Disable the mailbox out-of-band.
	if err := db.Model(&models.Mailbox{}).Where("id = ?", mb.ID).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}

	if rr := doRefresh(h, pair.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Fatalf("refresh of disabled mailbox: expected 401, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestRefresh_DisabledAdminRejected: same guarantee for admins — disabling an
// admin invalidates their live session at the next refresh.
func TestRefresh_DisabledAdminRejected(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	h := newDBAuthHandler(db, jwt)

	admin := seedAdminWithCaps(t, db, "disabled-admin", []string{"queue:read"})
	pair, err := jwt.GenerateAdminTokenPair(admin.ID, admin.Username, []string{"queue:read"})
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, "admin", admin.ID)

	if err := db.Model(&models.AdminUser{}).Where("id = ?", admin.ID).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}

	if rr := doRefresh(h, pair.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Fatalf("refresh of disabled admin: expected 401, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestRefresh_AdminCapabilitiesRederivedFromDB: an admin's capabilities are
// re-derived from the database on refresh, not copied from the (up to 7-day-old)
// refresh token. A token minted while the admin was privileged must yield a
// reduced capability set once the admin has been demoted in the database.
func TestRefresh_AdminCapabilitiesRederivedFromDB(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	h := newDBAuthHandler(db, jwt)

	admin := seedAdminWithCaps(t, db, "demoted-admin", []string{"queue:read"})
	// Refresh token still carries the old, elevated capability set.
	pair, err := jwt.GenerateAdminTokenPair(admin.ID, admin.Username, []string{"*", "queue:read"})
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, "admin", admin.ID)

	rr := doRefresh(h, pair.RefreshToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin refresh: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	claims := validateBody(t, jwt, rr)
	if len(claims.Capabilities) != 1 || claims.Capabilities[0] != "queue:read" {
		t.Fatalf("capabilities not re-derived from DB: got %v, want [queue:read]", claims.Capabilities)
	}
}

func mailboxUpdate(t *testing.T, h *MailboxHandler, id uint, body string) *httptest.ResponseRecorder {
	t.Helper()
	idStr := strconv.FormatUint(uint64(id), 10)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/mailboxes/"+idStr, bytes.NewBufferString(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", idStr)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	return rr
}

// TestMailboxPasswordChangeRevokesSessions: changing a mailbox password revokes
// every live session for that mailbox.
func TestMailboxPasswordChangeRevokesSessions(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	authH := newDBAuthHandler(db, jwt)
	mbH := NewMailboxHandler(db)

	mb := seedMailbox(t, db, "pwchange")
	pair, err := jwt.GenerateTokenPair(mb.ID, mb.Address, 0)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, "mailbox", mb.ID)

	if rr := mailboxUpdate(t, mbH, mb.ID, `{"password":"newpassword123"}`); rr.Code != http.StatusOK {
		t.Fatalf("password change: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	if rr := doRefresh(authH, pair.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after password change: expected 401, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestMailboxDisableRevokesSessions: setting active=false revokes live sessions.
func TestMailboxDisableRevokesSessions(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	mbH := NewMailboxHandler(db)

	mb := seedMailbox(t, db, "disable-mb")
	pair, err := jwt.GenerateTokenPair(mb.ID, mb.Address, 0)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, "mailbox", mb.ID)

	if rr := mailboxUpdate(t, mbH, mb.ID, `{"active":false}`); rr.Code != http.StatusOK {
		t.Fatalf("disable: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	if got := ledgerStatus(t, db, pair.RefreshJTI); got != models.RefreshTokenRevoked {
		t.Fatalf("ledger status after disable = %q, want revoked", got)
	}
}

// TestMailboxDeleteRevokesSessions: deleting a mailbox revokes its sessions.
func TestMailboxDeleteRevokesSessions(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	mbH := NewMailboxHandler(db)

	mb := seedMailbox(t, db, "delete-mb")
	pair, err := jwt.GenerateTokenPair(mb.ID, mb.Address, 0)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, "mailbox", mb.ID)

	idStr := strconv.FormatUint(uint64(mb.ID), 10)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/mailboxes/"+idStr, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", idStr)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	mbH.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d (%s)", rr.Code, rr.Body.String())
	}

	if got := ledgerStatus(t, db, pair.RefreshJTI); got != models.RefreshTokenRevoked {
		t.Fatalf("ledger status after delete = %q, want revoked", got)
	}
}

func adminUpdate(t *testing.T, h *AdminUserHandler, id uint, body string) *httptest.ResponseRecorder {
	t.Helper()
	idStr := strconv.FormatUint(uint64(id), 10)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/users/"+idStr, bytes.NewBufferString(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", idStr)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	h.UpdateAdminUser(rr, req)
	return rr
}

// TestAdminPasswordChangeRevokesSessions: changing an admin password revokes
// their live sessions.
func TestAdminPasswordChangeRevokesSessions(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	adminH := NewAdminUserHandler(db)

	admin := seedAdminWithCaps(t, db, "pw-admin", []string{"queue:read"})
	pair, err := jwt.GenerateAdminTokenPair(admin.ID, admin.Username, []string{"queue:read"})
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, "admin", admin.ID)

	if rr := adminUpdate(t, adminH, admin.ID, `{"password":"newpassword123"}`); rr.Code != http.StatusOK {
		t.Fatalf("admin password change: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	if got := ledgerStatus(t, db, pair.RefreshJTI); got != models.RefreshTokenRevoked {
		t.Fatalf("ledger status after admin password change = %q, want revoked", got)
	}
}

// TestAdminRoleChangeRevokesSessions: changing an admin's roles revokes their
// live sessions so stale capabilities cannot persist in an issued token.
func TestAdminRoleChangeRevokesSessions(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	adminH := NewAdminUserHandler(db)

	admin := seedAdminWithCaps(t, db, "role-admin", []string{"queue:read"})
	pair, err := jwt.GenerateAdminTokenPair(admin.ID, admin.Username, []string{"queue:read"})
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, "admin", admin.ID)

	// Create a second role and reassign the admin to it.
	newRole := models.Role{Name: "role-admin-2-" + revUniq()}
	if err := db.Create(&newRole).Error; err != nil {
		t.Fatal(err)
	}
	body := `{"role_ids":[` + strconv.FormatUint(uint64(newRole.ID), 10) + `]}`
	if rr := adminUpdate(t, adminH, admin.ID, body); rr.Code != http.StatusOK {
		t.Fatalf("admin role change: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	if got := ledgerStatus(t, db, pair.RefreshJTI); got != models.RefreshTokenRevoked {
		t.Fatalf("ledger status after admin role change = %q, want revoked", got)
	}
}

// TestAdminDisableRevokesSessions: disabling an admin revokes their sessions.
func TestAdminDisableRevokesSessions(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	adminH := NewAdminUserHandler(db)

	admin := seedAdminWithCaps(t, db, "disable-admin-h", []string{"queue:read"})
	pair, err := jwt.GenerateAdminTokenPair(admin.ID, admin.Username, []string{"queue:read"})
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, "admin", admin.ID)

	if rr := adminUpdate(t, adminH, admin.ID, `{"active":false}`); rr.Code != http.StatusOK {
		t.Fatalf("admin disable: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	if got := ledgerStatus(t, db, pair.RefreshJTI); got != models.RefreshTokenRevoked {
		t.Fatalf("ledger status after admin disable = %q, want revoked", got)
	}
}

// TestAdminDeleteRevokesSessions: deleting an admin revokes their sessions.
func TestAdminDeleteRevokesSessions(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	adminH := NewAdminUserHandler(db)

	admin := seedAdminWithCaps(t, db, "delete-admin", []string{"queue:read"})
	pair, err := jwt.GenerateAdminTokenPair(admin.ID, admin.Username, []string{"queue:read"})
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, "admin", admin.ID)

	idStr := strconv.FormatUint(uint64(admin.ID), 10)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/users/"+idStr, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", idStr)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	adminH.DeleteAdminUser(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("admin delete: expected 204, got %d (%s)", rr.Code, rr.Body.String())
	}

	if got := ledgerStatus(t, db, pair.RefreshJTI); got != models.RefreshTokenRevoked {
		t.Fatalf("ledger status after admin delete = %q, want revoked", got)
	}
}

func ledgerStatus(t *testing.T, db *gorm.DB, jti string) string {
	t.Helper()
	rec, err := repositories.NewRefreshTokenRepository(db).GetByJTI(jti)
	if err != nil {
		t.Fatalf("get ledger row %q: %v", jti, err)
	}
	return rec.Status
}
