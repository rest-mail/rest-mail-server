package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
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
	gdb, err := connectTestDB()
	if err != nil {
		t.Skipf("session-revocation DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(accountModels...); err != nil {
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
	hash, err := hashPassword("password123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	mb := models.Mailbox{
		DomainID:    domain.ID,
		LocalPart:   label,
		Address:     label + "-" + tok + "@" + domain.Name,
		DisplayName: "User",
		Active:      true,
	}
	if err := db.Create(&mb).Error; err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	if _, err := repositories.NewCredentialRepository(db).SetPassword(mb.ID, hash, nil, time.Now()); err != nil {
		t.Fatalf("set password: %v", err)
	}
	return mb
}

// seedAdminWithCaps creates an active account holding a single role, at "*",
// that grants the given capability names, and returns it: an admin. The label
// only aids debugging; the address and role name are uniquified.
func seedAdminWithCaps(t *testing.T, db *gorm.DB, label string, capNames []string) models.Mailbox {
	t.Helper()
	admin := seedMailbox(t, db, label)
	role := models.Role{Name: "role-" + label + "-" + revUniq()}
	if err := db.Create(&role).Error; err != nil {
		t.Fatalf("create role: %v", err)
	}
	if err := db.Create(&models.Grant{
		HolderKind: models.GrantHolderAccount, HolderID: admin.ID, RoleID: role.ID,
		Scope: "*", Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("grant role: %v", err)
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
func seedLedger(t *testing.T, db *gorm.DB, pair *auth.TokenPair, accountID uint) {
	t.Helper()
	if err := repositories.NewRefreshTokenRepository(db).Save(&models.RefreshToken{
		Jti:       pair.RefreshJTI,
		AccountID: accountID,
		SessionID: pair.SessionID,
		Status:    models.RefreshTokenActive,
		ExpiresAt: pair.RefreshExpiresAt,
	}); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
}

func newDBAuthHandler(db *gorm.DB, jwt *auth.JWTService) *AuthHandler {
	return NewAuthHandler(db, jwt, "", nil)
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
	pair, err := jwt.GenerateSession(mb.ID, mb.Address, 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, mb.ID)

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
	pair, err := jwt.GenerateSession(admin.ID, admin.Address, 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, admin.ID)

	if err := db.Model(&models.Mailbox{}).Where("id = ?", admin.ID).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}

	if rr := doRefresh(h, pair.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Fatalf("refresh of disabled admin: expected 401, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestRefresh_AdminCapabilitiesRederivedFromDB: the capabilities a refresh
// answers with are the admin's grants now. Tokens carry none: the refreshed
// session's response lists what the database grants, whatever it held when
// the refresh token was minted.
func TestRefresh_AdminCapabilitiesRederivedFromDB(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	h := newDBAuthHandler(db, jwt)

	admin := seedAdminWithCaps(t, db, "demoted-admin", []string{"queue:read"})
	pair, err := jwt.GenerateSession(admin.ID, admin.Address, 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, admin.ID)

	rr := doRefresh(h, pair.RefreshToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin refresh: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var body struct {
		Data struct {
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got := body.Data.Capabilities; len(got) != 1 || got[0] != "queue:read" {
		t.Fatalf("capabilities not re-derived from DB: got %v, want [queue:read]", got)
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
	mailboxRoutes(h).ServeHTTP(rr, req)
	return rr
}

// TestMailboxPasswordChangeRevokesSessions: changing a mailbox password revokes
// every live session for that mailbox.
func TestMailboxPasswordChangeRevokesSessions(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	authH := newDBAuthHandler(db, jwt)
	mbH := NewMailboxHandler(db, grants.NewCache(db, grants.CacheTTL), nil)

	mb := seedMailbox(t, db, "pwchange")
	pair, err := jwt.GenerateSession(mb.ID, mb.Address, 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, mb.ID)

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
	mbH := NewMailboxHandler(db, grants.NewCache(db, grants.CacheTTL), nil)

	mb := seedMailbox(t, db, "disable-mb")
	pair, err := jwt.GenerateSession(mb.ID, mb.Address, 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, mb.ID)

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
	mbH := NewMailboxHandler(db, grants.NewCache(db, grants.CacheTTL), nil)

	mb := seedMailbox(t, db, "delete-mb")
	pair, err := jwt.GenerateSession(mb.ID, mb.Address, 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, mb.ID)

	idStr := strconv.FormatUint(uint64(mb.ID), 10)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/mailboxes/"+idStr, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", idStr)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	mailboxRoutes(mbH).ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d (%s)", rr.Code, rr.Body.String())
	}

	if got := ledgerStatus(t, db, pair.RefreshJTI); got != models.RefreshTokenRevoked {
		t.Fatalf("ledger status after delete = %q, want revoked", got)
	}
}

// grantingTx is a transaction, rolled back after the test, for a test that
// changes grants: each change writes to the audit log, which cannot be
// emptied, and this database is shared. It returns the session of a
// superadmin, signed in with 2FA, to make the changes as.
func grantingTx(t *testing.T, db *gorm.DB) (*gorm.DB, *auth.Claims) {
	t.Helper()
	if err := rmdb.MigrateAuditLog(db); err != nil {
		t.Skipf("grant change DB test skipped: audit log migrate failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	granter := seedAdminWithCaps(t, tx, "granter", []string{"*"})
	return tx, &auth.Claims{
		AccountID: granter.ID, Address: granter.Address,
		AMR: []string{auth.AMRPassword, auth.AMROTP}, TwoFactor: true,
	}
}

// TestAdminDeleteRevokesSessions: deleting an account that holds admin grants
// removes them under the grants rules (a session with 2FA, holding what it
// removes), revokes its sessions and its credentials, and removes the grants
// others held on it.
func TestAdminDeleteRevokesSessions(t *testing.T) {
	db, granter := grantingTx(t, openRevocationTestDB(t))
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	mbH := NewMailboxHandler(db, grants.NewCache(db, grants.CacheTTL), nil)

	admin := seedAdminWithCaps(t, db, "delete-admin", []string{"queue:read"})
	pair, err := jwt.GenerateSession(admin.ID, admin.Address, 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, pair, admin.ID)
	// Someone else owns it, too.
	var owner models.Role
	if err := db.Where("name = ?", models.RoleOwner).FirstOrCreate(&owner, models.Role{Name: models.RoleOwner}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: granter.AccountID, RoleID: owner.ID,
		Scope: grants.Mailbox(admin.Address), Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	del := func(as *auth.Claims) *httptest.ResponseRecorder {
		idStr := strconv.FormatUint(uint64(admin.ID), 10)
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/mailboxes/"+idStr, nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", idStr)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsKey, as))
		rr := httptest.NewRecorder()
		mailboxRoutes(mbH).ServeHTTP(rr, req)
		return rr
	}
	noOTP := *granter
	noOTP.AMR, noOTP.TwoFactor = []string{auth.AMRPassword}, false
	if rr := del(&noOTP); rr.Code != http.StatusForbidden {
		t.Fatalf("delete an admin without 2FA: expected 403, got %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := del(granter); rr.Code != http.StatusNoContent {
		t.Fatalf("admin delete: expected 204, got %d (%s)", rr.Code, rr.Body.String())
	}

	if got := ledgerStatus(t, db, pair.RefreshJTI); got != models.RefreshTokenRevoked {
		t.Fatalf("ledger status after admin delete = %q, want revoked", got)
	}
	var grantsLeft, liveCreds int64
	db.Model(&models.Grant{}).Where("holder_id = ? OR scope = ?", admin.ID, grants.Mailbox(admin.Address)).Count(&grantsLeft)
	db.Model(&models.Credential{}).Where("account_id = ? AND revoked_at IS NULL", admin.ID).Count(&liveCreds)
	if grantsLeft != 0 || liveCreds != 0 {
		t.Errorf("after deleting: %d grants by or on it, %d live credentials", grantsLeft, liveCreds)
	}
}

// TestAccountPasswordChangeRevokesSessions: an account choosing a new
// password (POST /auth/password) ends its sessions and app passwords, bumps
// its epoch, and leaves it the one new password.
func TestAccountPasswordChangeRevokesSessions(t *testing.T) {
	db := openRevocationTestDB(t)
	if err := rmdb.MigrateAuditLog(db); err != nil {
		t.Skipf("audit log migrate failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	h := newDBAuthHandler(tx, jwt)

	mb := seedMailbox(t, tx, "own-pw")
	pair, err := jwt.GenerateSession(mb.ID, mb.Address, 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, tx, pair, mb.ID)
	if _, _, err := repositories.NewCredentialRepository(tx).CreateAppPassword(mb.ID, mb.ID, "phone", nil); err != nil {
		t.Fatal(err)
	}

	change := func(current, next string) *httptest.ResponseRecorder {
		body := `{"current_password":"` + current + `","new_password":"` + next + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", bytes.NewBufferString(body))
		req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: mb.ID, Address: mb.Address}))
		rr := httptest.NewRecorder()
		passwordChangeRoutes(h).ServeHTTP(rr, req)
		return rr
	}
	if rr := change("wrong", "a brand new passphrase"); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), `"code":"invalid_password"`) {
		t.Fatalf("wrong current password: %d %s", rr.Code, rr.Body.String())
	}
	if rr := change("password123", "a brand new passphrase"); rr.Code != http.StatusOK {
		t.Fatalf("change: %d %s", rr.Code, rr.Body.String())
	}
	if got := ledgerStatus(t, tx, pair.RefreshJTI); got != models.RefreshTokenRevoked {
		t.Errorf("session after a new password = %q, want revoked", got)
	}
	creds := repositories.NewCredentialRepository(tx)
	if c, _ := creds.MatchPassword(mb.ID, "password123", time.Now()); c != nil {
		t.Error("the old password still matches")
	}
	if c, _ := creds.MatchPassword(mb.ID, "a brand new passphrase", time.Now()); c == nil {
		t.Error("the new password does not match")
	}
	if live, _ := creds.ListForAccount(mb.ID, models.CredentialKindAppPassword); len(live) != 0 {
		t.Errorf("app passwords outlived the new password: %d", len(live))
	}
	var after models.Mailbox
	tx.First(&after, mb.ID)
	if after.CredentialEpoch != mb.CredentialEpoch+1 {
		t.Errorf("epoch %d -> %d, want it bumped", mb.CredentialEpoch, after.CredentialEpoch)
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
