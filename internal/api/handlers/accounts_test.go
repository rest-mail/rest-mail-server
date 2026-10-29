package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/crypto"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// openAccountsTestDB is the shared test database with what accounts touch:
// mailboxes, credentials, roles and grants, and the audit log.
func openAccountsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.QuotaUsage{}, &models.RefreshToken{}); err != nil {
		t.Skipf("accounts DB test skipped: migrate failed (%v)", err)
	}
	if err := rmdb.MigrateAuditLog(db); err != nil {
		t.Skipf("accounts DB test skipped: audit log migrate failed (%v)", err)
	}
	if err := bootstrap.EnsureRBAC(db); err != nil {
		t.Fatalf("EnsureRBAC: %v", err)
	}
	return db
}

// accountsFixture is a domain of the test's own with an admin holding
// mailboxes:write on it (the admin role at domain:<name>) and a plain user
// holding nothing, both with the password "s3cret-password".
type accountsFixture struct {
	domain models.Domain
	admin  models.Mailbox
	user   models.Mailbox
}

const accountsPassword = "s3cret-password"

func seedAccountsFixture(t *testing.T, db *gorm.DB) accountsFixture {
	t.Helper()
	domain := models.Domain{Name: fmt.Sprintf("acct-%d.test", time.Now().UnixNano()), DefaultQuotaBytes: 1 << 20}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	hash, err := hashPassword(accountsPassword)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(local string) models.Mailbox {
		mb := models.Mailbox{DomainID: domain.ID, LocalPart: local, Address: local + "@" + domain.Name, Active: true}
		if err := db.Create(&mb).Error; err != nil {
			t.Fatalf("create %s: %v", local, err)
		}
		if _, err := repositories.NewCredentialRepository(db).SetPassword(mb.ID, hash, nil, time.Now()); err != nil {
			t.Fatal(err)
		}
		return mb
	}
	fx := accountsFixture{domain: domain, admin: mk("admin"), user: mk("user")}
	var adminRole models.Role
	if err := db.Where("name = ?", "admin").First(&adminRole).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: fx.admin.ID, RoleID: adminRole.ID,
		Scope: grants.Domain(domain.Name), Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var ids []uint
		db.Model(&models.Mailbox{}).Where("domain_id = ?", domain.ID).Pluck("id", &ids)
		db.Where("holder_kind = ? AND (holder_id IN ? OR scope LIKE ?)", models.GrantHolderAccount, ids, "%@"+domain.Name).Delete(&models.Grant{})
		db.Where("account_id IN ?", ids).Delete(&models.Credential{})
		db.Where("mailbox_id IN ?", ids).Delete(&models.QuotaUsage{})
		db.Where("domain_id = ?", domain.ID).Delete(&models.Mailbox{})
		db.Delete(&models.Domain{}, domain.ID)
	})
	return fx
}

func decodeAccount(t *testing.T, rr *httptest.ResponseRecorder) accountResponse {
	t.Helper()
	var out struct {
		Data accountResponse `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rr.Body.String(), err)
	}
	return out.Data
}

func listAccounts(t *testing.T, h *AccountHandler, accountID uint) []accountResponse {
	t.Helper()
	rr := httptest.NewRecorder()
	accountRoutes(h).ServeHTTP(rr, authzRequest(http.MethodGet, "/api/v1/accounts", accountID, nil, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Data []accountResponse `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Data
}

// TestCreateAccount: POST /api/v1/accounts makes an account the same for
// everyone who may: mailboxes:write on its domain. The creator owns it, with
// a grant; without a password it has no credential at all. Refusals: 403 with
// no capability, 409 when it exists, 422 for a bad address or a domain this
// server does not host.
func TestCreateAccount(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	h := NewAccountHandler(db, grants.NewCache(db, grants.CacheTTL), "")
	create := func(accountID uint, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		accountRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/accounts", accountID, nil, body))
		return rr
	}
	chris := "chris@" + fx.domain.Name

	if rr := create(fx.user.ID, fmt.Sprintf(`{"address":%q}`, chris)); rr.Code != http.StatusForbidden {
		t.Fatalf("an account without mailboxes:write: %d %s", rr.Code, rr.Body.String())
	}
	for _, bad := range []string{"", "no-at-sign", "a b@" + fx.domain.Name, "@" + fx.domain.Name} {
		if rr := create(fx.admin.ID, fmt.Sprintf(`{"address":%q}`, bad)); rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("address %q: %d, want 422", bad, rr.Code)
		}
	}
	if rr := create(fx.admin.ID, fmt.Sprintf(`{"address":%q,"password":"short"}`, chris)); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("a short password: %d, want 422", rr.Code)
	}
	// A domain the admin's grant does not cover is refused before anything
	// says whether it is hosted.
	if rr := create(fx.admin.ID, `{"address":"x@not-hosted.example"}`); rr.Code != http.StatusForbidden {
		t.Errorf("another domain: %d, want 403", rr.Code)
	}

	rr := create(fx.admin.ID, fmt.Sprintf(`{"address":%q,"display_name":"Chris"}`, "Chris@"+fx.domain.Name))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	got := decodeAccount(t, rr)
	if got.Address != chris || got.DisplayName != "Chris" || got.IsPrimary || got.ID == 0 {
		t.Errorf("created = %+v", got)
	}
	if rr := create(fx.admin.ID, fmt.Sprintf(`{"address":%q}`, chris)); rr.Code != http.StatusConflict {
		t.Errorf("again: %d, want 409", rr.Code)
	}

	// No password: no credential, so nobody can sign in to it.
	var creds int64
	db.Model(&models.Credential{}).Where("account_id = ?", got.ID).Count(&creds)
	if creds != 0 {
		t.Errorf("an account made without a password has %d credentials", creds)
	}
	// The creator owns it, through a grant, recorded in the audit log.
	var owner []models.Grant
	db.Preload("Role").Where("holder_id = ? AND scope = ?", fx.admin.ID, grants.Mailbox(chris)).Find(&owner)
	if len(owner) != 1 || owner[0].Role.Name != models.RoleOwner || owner[0].Mode != models.GrantActive {
		t.Fatalf("the creator's grants on it = %+v", owner)
	}
	var created, granted int64
	db.Model(&models.AuditEntry{}).Where("action = ? AND target->>'account_id' = ?", ActionAccountsCreate, fmt.Sprint(got.ID)).Count(&created)
	db.Model(&models.AuditEntry{}).Where("action = ? AND target->>'grant_id' = ?", grants.ActionCreate, fmt.Sprint(owner[0].ID)).Count(&granted)
	if created != 1 || granted != 1 {
		t.Errorf("audit entries: %d accounts.create, %d grants.create, want 1 each", created, granted)
	}

	// It is listed, after the creator's own; it reaches nothing of the
	// creator's.
	list := listAccounts(t, h, fx.admin.ID)
	if len(list) != 2 || !list[0].IsPrimary || list[0].ID != fx.admin.ID || list[1].ID != got.ID {
		t.Fatalf("the creator's accounts = %+v", list)
	}
	if list := listAccounts(t, h, fx.user.ID); len(list) != 1 || list[0].ID != fx.user.ID {
		t.Errorf("another account lists %+v", list)
	}
	if _, err := mayReach(db, &auth.Claims{AccountID: fx.admin.ID}, got.ID, models.CapMailSend); err != nil {
		t.Errorf("the owner cannot send as it: %v", err)
	}
	if _, err := mayReach(db, &auth.Claims{AccountID: fx.user.ID}, got.ID, models.CapMailRead); err == nil {
		t.Error("an unrelated account reaches it")
	}

	// With a password, the account can be signed in to.
	withPassword := "dana@" + fx.domain.Name
	rr = create(fx.admin.ID, fmt.Sprintf(`{"address":%q,"password":"dana's own password"}`, withPassword))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create with a password: %d %s", rr.Code, rr.Body.String())
	}
	dana := decodeAccount(t, rr)
	if c, err := repositories.NewCredentialRepository(db).MatchPassword(dana.ID, "dana's own password", time.Now()); err != nil || c == nil {
		t.Errorf("its password does not match: %v %v", c, err)
	}
}

// TestLinkAccount: linking proves one of an account's passwords and grants the
// caller the owner role on it; a wrong password, an unknown address and an
// account with no password are all the same 403 invalid_credentials, never a
// 401, which would say the caller's own session failed. Linking twice, or
// one's own account, is a conflict; unlinking removes the grant.
func TestLinkAccount(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	h := NewAccountHandler(db, grants.NewCache(db, grants.CacheTTL), testMasterKey)
	linkWith := func(accountID uint, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		accountRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/accounts/link", accountID, nil, body))
		return rr
	}
	link := func(accountID uint, address, password string) *httptest.ResponseRecorder {
		return linkWith(accountID, fmt.Sprintf(`{"address":%q,"password":%q}`, address, password))
	}
	// An account with no password of its own.
	nopw := models.Mailbox{DomainID: fx.domain.ID, LocalPart: "nopw", Address: "nopw@" + fx.domain.Name, Active: true}
	if err := db.Create(&nopw).Error; err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, address, password string }{
		{"wrong password", fx.admin.Address, "not it"},
		{"unknown address", "nobody@" + fx.domain.Name, accountsPassword},
		{"no password at all", nopw.Address, ""},
		{"no password at all, guessed", nopw.Address, accountsPassword},
	} {
		rr := link(fx.user.ID, tc.address, tc.password)
		want, wantCode := http.StatusForbidden, "invalid_credentials"
		if tc.password == "" {
			want, wantCode = http.StatusUnprocessableEntity, "validation_failed"
		}
		if rr.Code != want || errorCodeOf(rr) != wantCode {
			t.Errorf("%s: %d %s, want %d %s", tc.name, rr.Code, errorCodeOf(rr), want, wantCode)
		}
	}
	if rr := link(fx.user.ID, fx.user.Address, accountsPassword); rr.Code != http.StatusConflict {
		t.Errorf("one's own account: %d, want 409", rr.Code)
	}

	rr := link(fx.user.ID, fx.admin.Address, accountsPassword)
	if rr.Code != http.StatusCreated {
		t.Fatalf("link: %d %s", rr.Code, rr.Body.String())
	}
	if got := decodeAccount(t, rr); got.ID != fx.admin.ID || got.IsPrimary {
		t.Errorf("linked = %+v", got)
	}
	if rr := link(fx.user.ID, fx.admin.Address, accountsPassword); rr.Code != http.StatusConflict {
		t.Errorf("linked twice: %d, want 409", rr.Code)
	}
	if list := listAccounts(t, h, fx.user.ID); len(list) != 2 || list[1].ID != fx.admin.ID {
		t.Fatalf("after linking: %+v", list)
	}
	// Linking reaches the account's mail, not its admin grants: grants are not
	// passed on.
	held, _ := grants.Load(db, fx.user.ID, time.Now())
	if !held.Allows(models.CapMailRead, grants.Mailbox(fx.admin.Address)) || held.Allows("mailboxes:write", grants.Domain(fx.domain.Name)) {
		t.Errorf("held after linking: mail %v, admin's domain %v",
			held.Allows(models.CapMailRead, grants.Mailbox(fx.admin.Address)), held.Allows("mailboxes:write", grants.Domain(fx.domain.Name)))
	}

	// An account with 2FA needs its code too: a password alone is less than
	// signing in to it.
	// A readable authenticator, so a wrong code is refused as wrong; one that
	// cannot be decrypted is a server error (second_factor_errors_test.go).
	key, err := auth.GenerateTOTPSecret(auth.DefaultTOTPIssuer, fx.admin.Address)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := crypto.EncryptString(key.Secret(), testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.NewTwoFactorRepository(db).Enroll(fx.admin.ID, secret, nil); err != nil {
		t.Fatal(err)
	}
	db.Model(&models.Credential{}).Where("account_id = ? AND kind = ?", fx.admin.ID, models.CredentialKindTOTP).Update("confirmed_at", time.Now())
	third := models.Mailbox{DomainID: fx.domain.ID, LocalPart: "third", Address: "third@" + fx.domain.Name, Active: true}
	if err := db.Create(&third).Error; err != nil {
		t.Fatal(err)
	}
	if rr := link(third.ID, fx.admin.Address, accountsPassword); rr.Code != http.StatusForbidden || errorCodeOf(rr) != "totp_required" {
		t.Errorf("linking an account with 2FA by its password alone: %d %s", rr.Code, rr.Body.String())
	}
	for _, second := range []string{`"totp_code":"123456"`, `"recovery_code":"not-a-code"`} {
		body := fmt.Sprintf(`{"address":%q,"password":%q,%s}`, fx.admin.Address, accountsPassword, second)
		if rr := linkWith(third.ID, body); rr.Code != http.StatusForbidden || errorCodeOf(rr) != "invalid_totp" {
			t.Errorf("linking an account with 2FA by a wrong %s: %d %s", second, rr.Code, rr.Body.String())
		}
	}

	// Unlinking removes the caller's grant, and only the caller's.
	unlink := func(accountID, id uint) int {
		rr := httptest.NewRecorder()
		accountRoutes(h).ServeHTTP(rr, authzRequest(http.MethodDelete, "/api/v1/accounts/"+uintToStr(id), accountID, nil, ""))
		return rr.Code
	}
	if code := unlink(fx.user.ID, fx.user.ID); code != http.StatusConflict {
		t.Errorf("unlink one's own: %d, want 409", code)
	}
	if code := unlink(third.ID, fx.admin.ID); code != http.StatusNotFound {
		t.Errorf("unlink what one does not hold: %d, want 404", code)
	}
	if code := unlink(fx.user.ID, fx.admin.ID); code != http.StatusNoContent {
		t.Fatalf("unlink: %d", code)
	}
	if list := listAccounts(t, h, fx.user.ID); len(list) != 1 {
		t.Errorf("after unlinking: %+v", list)
	}
	if _, err := mayReach(db, &auth.Claims{AccountID: fx.user.ID}, fx.admin.ID, models.CapMailRead); err == nil {
		t.Error("the unlinked account is still reached")
	}
}

// TestLinkAccount_ConcurrentLinksOneGrant fires many concurrent links of the
// same account by the same caller. Exactly one wins (201), the rest conflict
// (409), and the caller holds one grant on it: the grants table's unique
// holder, role and scope is the backstop.
func TestLinkAccount_ConcurrentLinksOneGrant(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	h := NewAccountHandler(db, grants.NewCache(db, grants.CacheTTL), "")
	body := fmt.Sprintf(`{"address":%q,"password":%q}`, fx.admin.Address, accountsPassword)

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			rr := httptest.NewRecorder()
			accountRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/accounts/link", fx.user.ID, nil, body))
			codes[i] = rr.Code
		}(i)
	}
	close(start)
	wg.Wait()

	created, conflicts := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		default:
			t.Errorf("unexpected status from concurrent link: %d", c)
		}
	}
	if created != 1 || conflicts != n-1 {
		t.Fatalf("created=%d conflicts=%d codes=%v, want 1 and %d", created, conflicts, codes, n-1)
	}
	var count int64
	db.Model(&models.Grant{}).Where("holder_id = ? AND scope = ?", fx.user.ID, grants.Mailbox(fx.admin.Address)).Count(&count)
	if count != 1 {
		t.Fatalf("grants on the account = %d, want 1", count)
	}
}
