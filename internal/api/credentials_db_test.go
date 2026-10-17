package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/gateway/apiclient"
	imapgw "github.com/restmail/restmail/internal/gateway/imap"
	pop3gw "github.com/restmail/restmail/internal/gateway/pop3"
	smtpgw "github.com/restmail/restmail/internal/gateway/smtp"
	"gorm.io/gorm"
)

// The app password tests (#344) run the real router on a database of their
// own, migrated as the server migrates it and dropped afterwards: audit
// entries cannot be deleted once written, so they must not go into the
// database other tests share. They skip when no database is reachable.

func credentialTestEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func openCredentialScratchDB(t *testing.T) *gorm.DB {
	t.Helper()
	scratch := createScratchDB(t, "credentials")
	if err := rmdb.AutoMigrate(scratch); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := bootstrap.EnsureRBAC(scratch); err != nil {
		t.Fatalf("roles: %v", err)
	}
	return scratch
}

// createScratchDB is a new, empty database, restmail_<purpose>_…, dropped when
// the test ends. The test skips when no database server is reachable.
func createScratchDB(t *testing.T, purpose string) *gorm.DB {
	t.Helper()
	port, _ := strconv.Atoi(credentialTestEnv("DB_PORT", "5432"))
	cfg := config.Config{
		DBHost: credentialTestEnv("DB_HOST", "localhost"),
		DBPort: port,
		DBName: credentialTestEnv("DB_NAME", "restmail"),
		DBUser: credentialTestEnv("DB_USER", "restmail"),
		DBPass: credentialTestEnv("DB_PASS", "restmail"),
	}
	admin, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Skipf("skipped: no database reachable (%v)", err)
	}
	name := fmt.Sprintf("restmail_%s_%d_%d", purpose, os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Skipf("skipped: cannot create a database (%v)", err)
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
	return scratch
}

// credentialFixture is a server with one mailbox signed in with its password.
type credentialFixture struct {
	t         *testing.T
	db        *gorm.DB
	router    http.Handler
	jwt       *auth.JWTService
	address   string
	password  string // the mailbox's password now, which creating an app password takes
	mailboxID uint
	accountID uint
	session   string // the mailbox's access token, from signing in with its password
}

const credentialTestPassword = "mailbox-password-123"

func newCredentialFixture(t *testing.T) *credentialFixture {
	t.Helper()
	return newCredentialFixtureWith(t, &config.Config{Environment: "test"})
}

func newCredentialFixtureWith(t *testing.T, cfg *config.Config) *credentialFixture {
	t.Helper()
	db := openCredentialScratchDB(t)
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	routers := NewRouters(db, jwtSvc, cfg, nil)
	t.Cleanup(routers.Close)
	f := &credentialFixture{t: t, db: db, router: routers.Public, jwt: jwtSvc, password: credentialTestPassword}

	domain := models.Domain{Name: "example.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	f.address = "chris@example.test"
	f.mailboxID = f.seedMailbox(domain.ID, "chris")

	rr := f.do(http.MethodPost, "/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":%q,"client":"native"}`, f.address, credentialTestPassword))
	if rr.Code != http.StatusOK {
		t.Fatalf("sign in with the mailbox's password: %d %s", rr.Code, rr.Body.String())
	}
	var login struct {
		Data struct {
			AccessToken string `json:"access_token"`
			User        struct {
				ID uint `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	decodeInto(t, rr, &login)
	f.session, f.accountID = "Bearer "+login.Data.AccessToken, login.Data.User.ID
	return f
}

func (f *credentialFixture) seedMailbox(domainID uint, local string) uint {
	f.t.Helper()
	hash, err := auth.HashPassword(credentialTestPassword)
	if err != nil {
		f.t.Fatal(err)
	}
	mb := models.Mailbox{DomainID: domainID, LocalPart: local, Address: local + "@example.test", Active: true}
	if err := f.db.Create(&mb).Error; err != nil {
		f.t.Fatalf("seed mailbox: %v", err)
	}
	if _, err := repositories.NewCredentialRepository(f.db).SetPassword(mb.ID, hash, nil, time.Now()); err != nil {
		f.t.Fatalf("seed password: %v", err)
	}
	return mb.ID
}

// own gives chris@ the owner grant on the account id at address.
func (f *credentialFixture) own(id uint, address string) {
	f.t.Helper()
	var owner models.Role
	if err := f.db.Where("name = ?", models.RoleOwner).First(&owner).Error; err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: f.mailboxID, RoleID: owner.ID,
		Scope: "mailbox:" + address, Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		f.t.Fatal(err)
	}
}

// passwordHash is the hash of chris@'s one usable password.
func (f *credentialFixture) passwordHash() string {
	f.t.Helper()
	var hashes []string
	f.db.Model(&models.Credential{}).Where("account_id = ? AND kind = ? AND revoked_at IS NULL", f.mailboxID, models.CredentialKindPassword).Pluck("secret_hash", &hashes)
	if len(hashes) != 1 {
		f.t.Fatalf("chris@ has %d usable passwords, want 1", len(hashes))
	}
	return hashes[0]
}

// do sends a request with the given Authorization header ("" for none).
func (f *credentialFixture) do(method, path, authorization, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "192.0.2.10:50000"
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

func basic(address, password string) string {
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth(address, password)
	return req.Header.Get("Authorization")
}

type createdCredential struct {
	ID       uint   `json:"id"`
	PublicID string `json:"public_id"`
	Password string `json:"password"`
}

func (f *credentialFixture) createAppPassword(name string) createdCredential {
	f.t.Helper()
	rr := f.do(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/credentials", f.accountID), f.session, fmt.Sprintf(`{"name":%q,"password":%q}`, name, f.password))
	if rr.Code != http.StatusCreated {
		f.t.Fatalf("create an app password: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data createdCredential `json:"data"`
	}
	decodeInto(f.t, rr, &resp)
	return resp.Data
}

func (f *credentialFixture) credentialCount() int64 {
	var n int64
	f.db.Model(&models.Credential{}).Count(&n)
	return n
}

// entries returns the audit log's entries with the given action, oldest first.
func (f *credentialFixture) entries(action string) []models.AuditEntry {
	var es []models.AuditEntry
	f.db.Where("action = ?", action).Order("seq ASC").Find(&es)
	return es
}

func decodeInto(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v", rr.Body.String(), err)
	}
}

func TestAppPasswords_EndToEnd(t *testing.T) {
	f := newCredentialFixture(t)
	created := f.createAppPassword("Thunderbird")
	if !auth.IsAppPassword(created.Password) || !strings.HasPrefix(created.Password, "rm_"+created.PublicID+"_") {
		t.Fatalf("created %+v", created)
	}
	appPassword := basic(f.address, created.Password)
	folders := fmt.Sprintf("/api/v1/accounts/%d/folders", f.accountID)

	t.Run("listed without its secret or hash", func(t *testing.T) {
		rr := f.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/credentials", f.accountID), f.session, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
		}
		var cred models.Credential
		f.db.First(&cred, created.ID)
		_, secret, _ := auth.ParseAppPassword(created.Password)
		body := rr.Body.String()
		if !strings.Contains(body, created.PublicID) || strings.Contains(body, secret) ||
			strings.Contains(body, cred.SecretHash) || strings.Contains(body, `"password"`) {
			t.Errorf("list: %s", body)
		}
	})

	t.Run("works with HTTP Basic", func(t *testing.T) {
		if rr := f.do(http.MethodGet, folders, appPassword, ""); rr.Code != http.StatusOK {
			t.Fatalf("folders with the app password: %d %s", rr.Code, rr.Body.String())
		}
		var cred models.Credential
		f.db.First(&cred, created.ID)
		if cred.LastUsedAt == nil {
			t.Error("last_used_at was not recorded")
		}
	})

	t.Run("refused by the interactive sign-in", func(t *testing.T) {
		rr := f.do(http.MethodPost, "/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":%q,"client":"native"}`, f.address, created.Password))
		if rr.Code != http.StatusUnauthorized || strings.Contains(rr.Body.String(), "access_token") {
			t.Fatalf("login with an app password: %d %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("cannot manage credentials or 2FA", func(t *testing.T) {
		before := f.credentialCount()
		for _, rt := range []struct{ method, path, body string }{
			{http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/credentials", f.accountID), fmt.Sprintf(`{"name":"another","password":%q}`, credentialTestPassword)},
			{http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/credentials", f.accountID), ""},
			{http.MethodDelete, fmt.Sprintf("/api/v1/accounts/%d/credentials/%d", f.accountID, created.ID), ""},
			{http.MethodPost, "/api/v1/auth/2fa/enroll", `{}`},
			{http.MethodPost, "/api/v1/auth/2fa/disable", `{}`},
			{http.MethodPost, "/api/v1/accounts", `{"address":"x@example.test","password":"y"}`},
		} {
			if rr := f.do(rt.method, rt.path, appPassword, rt.body); rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with an app password: %d %s", rt.method, rt.path, rr.Code, rr.Body.String())
			}
		}
		if f.credentialCount() != before {
			t.Error("an app password created a credential")
		}
		if rr := f.do(http.MethodGet, folders, appPassword, ""); rr.Code != http.StatusOK {
			t.Errorf("the app password stopped working after its attempts: %d", rr.Code)
		}
	})

	t.Run("reaches no admin route", func(t *testing.T) {
		// The admin routes take a session's token alone: Basic is refused
		// before RequireCapability, so an app password never has its grants read
		// (#345) and can change none, nor switch one on (#346), nor its own
		// mailbox's password.
		mailbox := fmt.Sprintf("/api/v1/admin/mailboxes/%d", f.mailboxID)
		for _, rt := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/admin/grants", ""},
			{http.MethodPost, "/api/v1/admin/grants", `{"holder":{"kind":"admin","id":1},"role":"superadmin","scope":"*","mode":"active"}`},
			{http.MethodDelete, "/api/v1/admin/grants/1", ""},
			{http.MethodGet, "/api/v1/admin/grants/mine", ""},
			{http.MethodPost, "/api/v1/admin/grants/1/activate", `{"reason":"r","minutes":60,"totp_code":"000000"}`},
			{http.MethodPost, "/api/v1/admin/grants/1/deactivate", ""},
			{http.MethodPost, "/api/v1/admin/grants/1/make-eligible", `{"totp_code":"000000"}`},
			{http.MethodGet, mailbox, ""},
			{http.MethodPatch, mailbox, `{"password":"a-new-password-456"}`},
			{http.MethodGet, "/api/v1/admin/domains", ""},
		} {
			if rr := f.do(rt.method, rt.path, appPassword, rt.body); rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with an app password: %d %s", rt.method, rt.path, rr.Code, rr.Body.String())
			}
		}
		if rr := f.do(http.MethodGet, folders, appPassword, ""); rr.Code != http.StatusOK {
			t.Errorf("the app password stopped working after its attempts: %d", rr.Code)
		}
	})

	t.Run("a protocol sign-in is recorded with its credential", func(t *testing.T) {
		rr := f.do(http.MethodPost, "/api/v1/auth/protocol-sign-in", appPassword, `{"protocol":"imap"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("protocol sign-in: %d %s", rr.Code, rr.Body.String())
		}
		var resp struct {
			Data struct {
				User struct {
					ID uint `json:"id"`
				} `json:"user"`
				CredentialID uint `json:"credential_id"`
			} `json:"data"`
		}
		decodeInto(t, rr, &resp)
		if resp.Data.User.ID != f.accountID || resp.Data.CredentialID != created.ID {
			t.Errorf("protocol sign-in answered %+v", resp.Data)
		}
		es := f.entries("auth.protocol_sign_in")
		if len(es) != 1 || es[0].CredentialID == nil || *es[0].CredentialID != created.ID ||
			es[0].Outcome != models.AuditOutcomeSuccess || es[0].Target["protocol"] != "imap" ||
			es[0].ActorID == nil || *es[0].ActorID != f.mailboxID {
			t.Errorf("protocol sign-in entries: %+v", es)
		}
		// A session cannot use it.
		if rr := f.do(http.MethodPost, "/api/v1/auth/protocol-sign-in", f.session, `{"protocol":"imap"}`); rr.Code != http.StatusBadRequest {
			t.Errorf("protocol sign-in with a session: %d", rr.Code)
		}
	})

	t.Run("a wrong one is refused and recorded", func(t *testing.T) {
		wrong := created.Password[:len(created.Password)-1] + "A"
		if strings.HasSuffix(created.Password, "A") {
			wrong = created.Password[:len(created.Password)-1] + "B"
		}
		if rr := f.do(http.MethodGet, folders, basic(f.address, wrong), ""); rr.Code != http.StatusUnauthorized {
			t.Fatalf("a wrong secret: %d", rr.Code)
		}
		// The account's own password is a credential too, and Basic takes it:
		// it is how the gateways sign in, one path for both.
		if rr := f.do(http.MethodGet, folders, basic(f.address, credentialTestPassword), ""); rr.Code != http.StatusOK {
			t.Fatalf("the account's own password with Basic: %d", rr.Code)
		}
		if rr := f.do(http.MethodGet, folders, basic(f.address, "not the password"), ""); rr.Code != http.StatusUnauthorized {
			t.Fatalf("a wrong password with Basic: %d", rr.Code)
		}
		es := f.entries(middleware.AuditActionBasicRefused)
		if len(es) != 2 || es[0].Target["reason"] != auth.CredentialWrongSecret || es[0].CredentialID != nil ||
			es[1].Target["reason"] != auth.CredentialWrongPassword || es[0].ActorName != f.address {
			t.Errorf("refusal entries: %+v", es)
		}
	})

	t.Run("revoked, it stops at the next request", func(t *testing.T) {
		rr := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/accounts/%d/credentials/%d", f.accountID, created.ID), f.session, "")
		if rr.Code != http.StatusNoContent {
			t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
		}
		if rr := f.do(http.MethodGet, folders, appPassword, ""); rr.Code != http.StatusUnauthorized {
			t.Fatalf("folders after revoking: %d", rr.Code)
		}
		es := f.entries(middleware.AuditActionBasicRefused)
		last := es[len(es)-1]
		if last.Target["reason"] != auth.CredentialRevoked || last.CredentialID == nil || *last.CredentialID != created.ID {
			t.Errorf("refusal after revoking: %+v", last)
		}
		// It stays in the table, revoked, and leaves the list.
		var cred models.Credential
		if err := f.db.First(&cred, created.ID).Error; err != nil || cred.RevokedAt == nil {
			t.Errorf("the revoked row: %+v, %v", cred, err)
		}
		if rr := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/accounts/%d/credentials/%d", f.accountID, created.ID), f.session, ""); rr.Code != http.StatusNotFound {
			t.Errorf("revoking twice: %d", rr.Code)
		}
	})

	t.Run("creation and revocation are recorded, and no entry holds the secret", func(t *testing.T) {
		create, revoke := f.entries("credentials.create"), f.entries("credentials.revoke")
		if len(create) != 1 || create[0].Target["credential_id"] != strconv.Itoa(int(created.ID)) ||
			create[0].Target["public_id"] != created.PublicID || create[0].Target["name"] != "Thunderbird" ||
			create[0].Outcome != models.AuditOutcomeSuccess {
			t.Errorf("creation entries: %+v", create)
		}
		if len(revoke) < 1 || revoke[0].Target["credential_id"] != strconv.Itoa(int(created.ID)) || revoke[0].Outcome != models.AuditOutcomeSuccess {
			t.Errorf("revocation entries: %+v", revoke)
		}
		_, secret, _ := auth.ParseAppPassword(created.Password)
		var all []models.AuditEntry
		f.db.Order("seq ASC").Find(&all)
		for _, e := range all {
			b, _ := json.Marshal(e)
			if strings.Contains(string(b), secret) || strings.Contains(string(b), credentialTestPassword) {
				t.Errorf("entry %d holds a secret: %s", e.Seq, b)
			}
		}
		if res := audit.Verify(all); !res.OK {
			t.Errorf("the audit chain is broken: %+v", res)
		}
	})
}

func TestAppPasswords_Expiry(t *testing.T) {
	f := newCredentialFixture(t)
	path := fmt.Sprintf("/api/v1/accounts/%d/credentials", f.accountID)

	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	if rr := f.do(http.MethodPost, path, f.session, fmt.Sprintf(`{"name":"n8n","expires_at":%q,"password":%q}`, past, f.password)); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an expiry in the past: %d %s", rr.Code, rr.Body.String())
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	rr := f.do(http.MethodPost, path, f.session, fmt.Sprintf(`{"name":"n8n","expires_at":%q,"password":%q}`, future, f.password))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data struct {
			createdCredential
			ExpiresAt *time.Time `json:"expires_at"`
		} `json:"data"`
	}
	decodeInto(t, rr, &resp)
	if resp.Data.ExpiresAt == nil {
		t.Fatal("no expires_at in the answer")
	}
	folders := fmt.Sprintf("/api/v1/accounts/%d/folders", f.accountID)
	appPassword := basic(f.address, resp.Data.Password)
	if rr := f.do(http.MethodGet, folders, appPassword, ""); rr.Code != http.StatusOK {
		t.Fatalf("before expiry: %d", rr.Code)
	}
	f.db.Model(&models.Credential{}).Where("id = ?", resp.Data.ID).Update("expires_at", time.Now().Add(-time.Second))
	if rr := f.do(http.MethodGet, folders, appPassword, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("after expiry: %d", rr.Code)
	}
	es := f.entries(middleware.AuditActionBasicRefused)
	if len(es) != 1 || es[0].Target["reason"] != auth.CredentialExpired {
		t.Errorf("refusal entries: %+v", es)
	}
}

func TestAppPasswords_CreatingNeedsTheSecondFactor(t *testing.T) {
	f := newCredentialFixture(t)
	if _, err := repositories.NewTwoFactorRepository(f.db).Enroll(f.mailboxID, "x", nil); err != nil {
		t.Fatalf("seed 2FA: %v", err)
	}
	f.db.Model(&models.Credential{}).Where("account_id = ? AND kind = ?", f.mailboxID, models.CredentialKindTOTP).Update("confirmed_at", time.Now())
	path := fmt.Sprintf("/api/v1/accounts/%d/credentials", f.accountID)
	body := fmt.Sprintf(`{"name":"iPhone Mail","password":%q}`, f.password)

	// The fixture's session signed in before 2FA was on: password only. The
	// password entered again is no second factor.
	rr := f.do(http.MethodPost, path, f.session, body)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "two_factor_required") {
		t.Fatalf("without a second factor: %d %s", rr.Code, rr.Body.String())
	}
	pair, err := f.jwt.GenerateSession(f.mailboxID, f.address, 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rr := f.do(http.MethodPost, path, "Bearer "+pair.AccessToken, body); rr.Code != http.StatusCreated {
		t.Fatalf("with a second factor: %d %s", rr.Code, rr.Body.String())
	}
}

// Creating an app password takes the mailbox's current password, entered
// again, as Gmail and GitHub ask for it. A session outlives a change of
// password by up to its access token's lifetime, and an app password made in
// that window, by whoever learnt the old password, would outlive the change.
func TestAppPasswords_CreatingNeedsTheCurrentPassword(t *testing.T) {
	f := newCredentialFixture(t)
	path := fmt.Sprintf("/api/v1/accounts/%d/credentials", f.accountID)
	const wrongPassword = "not-the-mailbox-password"
	refused := func(t *testing.T, session, body, code string) {
		t.Helper()
		before := f.credentialCount()
		rr := f.do(http.MethodPost, path, session, body)
		if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), `"code":"`+code+`"`) {
			t.Fatalf("create with %s: %d %s", body, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "rm_") || strings.Contains(rr.Body.String(), wrongPassword) {
			t.Errorf("the refusal answered with a secret: %s", rr.Body.String())
		}
		if f.credentialCount() != before {
			t.Error("a refused attempt made a credential")
		}
	}

	t.Run("without it, refused", func(t *testing.T) {
		refused(t, f.session, `{"name":"stolen"}`, "reauth_required")
		refused(t, f.session, `{"name":"stolen","password":""}`, "reauth_required")
	})

	t.Run("with a wrong one, refused", func(t *testing.T) {
		refused(t, f.session, fmt.Sprintf(`{"name":"stolen","password":%q}`, wrongPassword), "invalid_password")
	})

	t.Run("with the right one, made", func(t *testing.T) {
		created := f.createAppPassword("Thunderbird")
		if rr := f.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/folders", f.accountID), basic(f.address, created.Password), ""); rr.Code != http.StatusOK {
			t.Errorf("folders with the app password: %d %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("refusals are recorded as refused, never with the password", func(t *testing.T) {
		es := f.entries("credentials.create")
		if len(es) != 4 {
			t.Fatalf("%d creation entries, want 4: %+v", len(es), es)
		}
		for i, code := range []string{"reauth_required", "reauth_required", "invalid_password"} {
			if e := es[i]; e.Outcome != models.AuditOutcomeDenied || e.Status != http.StatusUnauthorized ||
				e.ErrorCode != code || e.Target["name"] != "stolen" || e.Target["credential_id"] != "" ||
				e.ActorKind != models.AuditActorAccount || e.ActorID == nil || *e.ActorID != f.mailboxID {
				t.Errorf("refusal %d: %+v", i, e)
			}
		}
		if es[3].Outcome != models.AuditOutcomeSuccess {
			t.Errorf("the creation: %+v", es[3])
		}
		var all []models.AuditEntry
		f.db.Order("seq ASC").Find(&all)
		for _, e := range all {
			if b, _ := json.Marshal(e); strings.Contains(string(b), wrongPassword) || strings.Contains(string(b), credentialTestPassword) {
				t.Errorf("entry %d holds a password: %s", e.Seq, b)
			}
		}
	})
}

// After a new password, the access token signed in with the old one still
// works until it expires, but the old password makes no app password with it.
func TestAppPasswords_OldPasswordRefusedAfterAChange(t *testing.T) {
	f := newCredentialFixture(t)
	path := fmt.Sprintf("/api/v1/accounts/%d/credentials", f.accountID)
	admin := adminToken(t, f.jwt, []string{middleware.CapMailboxesWrite})
	const newPassword = "a-new-password-456"
	if rr := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/mailboxes/%d", f.mailboxID), "Bearer "+admin,
		fmt.Sprintf(`{"password":%q}`, newPassword)); rr.Code != http.StatusOK {
		t.Fatalf("set a new password: %d %s", rr.Code, rr.Body.String())
	}
	// The session from before the change is still valid.
	if rr := f.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/folders", f.accountID), f.session, ""); rr.Code != http.StatusOK {
		t.Fatalf("the old session after the change: %d %s", rr.Code, rr.Body.String())
	}

	before := f.credentialCount()
	rr := f.do(http.MethodPost, path, f.session, fmt.Sprintf(`{"name":"stolen","password":%q}`, credentialTestPassword))
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), `"code":"invalid_password"`) {
		t.Fatalf("create with the old password: %d %s", rr.Code, rr.Body.String())
	}
	if f.credentialCount() != before {
		t.Fatal("the old password made a credential")
	}

	// Whoever knows the new password is the mailbox's owner now.
	f.password = newPassword
	f.createAppPassword("Thunderbird")
}

// A refused attempt to create one spends login's budget, from the same
// limiter, so checking the password here guesses no faster than login does.
func TestAppPasswords_CreatingIsThrottledLikeLogin(t *testing.T) {
	f := newCredentialFixtureWith(t, &config.Config{
		Environment:          "test",
		AuthRateLimitEnabled: true,
		// No refill during the test; the fixture's sign-in spends the first.
		AuthRateLimitRPS:   0.0001,
		AuthRateLimitBurst: 3,
	})
	path := fmt.Sprintf("/api/v1/accounts/%d/credentials", f.accountID)
	guess := `{"name":"stolen","password":"guess"}`
	for i := 0; i < 2; i++ {
		if rr := f.do(http.MethodPost, path, f.session, guess); rr.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d %s", i, rr.Code, rr.Body.String())
		}
	}
	rr := f.do(http.MethodPost, path, f.session, guess)
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("a guess past the budget: %d %s", rr.Code, rr.Body.String())
	}
	rr = f.do(http.MethodPost, "/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":%q,"client":"native"}`, f.address, credentialTestPassword))
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("a sign-in after the guesses: %d, want 429 from the shared budget", rr.Code)
	}
}

func TestAppPasswords_ReachOnlyTheirOwnMailbox(t *testing.T) {
	f := newCredentialFixture(t)
	// chris@ holds the owner grant on receipts@, so chris@'s session reaches
	// it.
	var domain models.Domain
	f.db.First(&domain)
	receipts := f.seedMailbox(domain.ID, "receipts")
	f.own(receipts, "receipts@example.test")
	linkedFolders := fmt.Sprintf("/api/v1/accounts/%d/folders", receipts)
	if rr := f.do(http.MethodGet, linkedFolders, f.session, ""); rr.Code != http.StatusOK {
		t.Fatalf("the session on a linked mailbox: %d %s", rr.Code, rr.Body.String())
	}

	appPassword := basic(f.address, f.createAppPassword("laptop").Password)
	if rr := f.do(http.MethodGet, linkedFolders, appPassword, ""); rr.Code != http.StatusForbidden {
		t.Errorf("the app password on a linked mailbox: %d %s", rr.Code, rr.Body.String())
	}
	rr := f.do(http.MethodGet, "/api/v1/accounts", appPassword, "")
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "receipts@") || !strings.Contains(rr.Body.String(), f.address) {
		t.Errorf("accounts listed for the app password: %d %s", rr.Code, rr.Body.String())
	}
	rr = f.do(http.MethodPost, "/api/v1/messages/send", appPassword, `{"from":"receipts@example.test","to":["x@example.org"],"subject":"s","body_text":"b"}`)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "not authorized to send from this address") {
		t.Errorf("sending as a linked mailbox with the app password: %d %s", rr.Code, rr.Body.String())
	}
	// An app password never makes credentials; the session of an owner
	// does, for an account it owns (mailbox:manage), proving its own
	// password.
	rr = f.do(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/credentials", receipts), appPassword, fmt.Sprintf(`{"name":"x","password":%q}`, credentialTestPassword))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("an app password making a credential: %d %s", rr.Code, rr.Body.String())
	}
	rr = f.do(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/credentials", receipts), f.session, fmt.Sprintf(`{"name":"for receipts","password":%q}`, credentialTestPassword))
	if rr.Code != http.StatusCreated {
		t.Fatalf("the owner making an app password for receipts@: %d %s", rr.Code, rr.Body.String())
	}
	var made createdCredential
	decodeInto(t, rr, &struct {
		Data *createdCredential `json:"data"`
	}{&made})
	if rr := f.do(http.MethodGet, linkedFolders, basic("receipts@example.test", made.Password), ""); rr.Code != http.StatusOK {
		t.Errorf("receipts@'s own app password: %d %s", rr.Code, rr.Body.String())
	}
	// An account it does not own is answered as one that does not exist.
	stranger := f.seedMailbox(domain.ID, "stranger")
	rr = f.do(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/credentials", stranger), f.session, fmt.Sprintf(`{"name":"x","password":%q}`, credentialTestPassword))
	if rr.Code != http.StatusNotFound {
		t.Errorf("creating a credential for an account it does not own: %d %s", rr.Code, rr.Body.String())
	}
}

// The gateways sign a mail client in with an app password as they do with the
// mailbox's own password, through the API, never its interactive login; and
// once it is revoked, the session they opened stops at its next command.
func TestAppPasswords_GatewaySignIn(t *testing.T) {
	f := newCredentialFixture(t)
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	created := f.createAppPassword("Thunderbird")
	api := apiclient.New(srv.URL)

	imapMailbox, err := imapgw.NewBackend(api, nil).Authenticate(f.address, created.Password)
	if err != nil {
		t.Fatalf("IMAP sign-in with the app password: %v", err)
	}
	if _, err := imapMailbox.Folders(); err != nil {
		t.Fatalf("IMAP LIST: %v", err)
	}
	pop3Mailbox, err := pop3gw.NewBackend(api, nil).Authenticate(f.address, created.Password)
	if err != nil {
		t.Fatalf("POP3 sign-in with the app password: %v", err)
	}
	if _, err := pop3Mailbox.Messages(); err != nil {
		t.Fatalf("POP3 STAT: %v", err)
	}
	// The mailbox's own password still signs in through the same path.
	if _, err := imapgw.NewBackend(api, nil).Authenticate(f.address, credentialTestPassword); err != nil {
		t.Fatalf("IMAP sign-in with the mailbox's password: %v", err)
	}
	if n := len(f.entries("auth.protocol_sign_in")); n != 3 {
		t.Errorf("%d protocol sign-ins recorded, want 3", n)
	}
	if es := f.entries("auth.sign_in"); len(es) != 1 { // the fixture's; the gateways never sign in interactively
		t.Errorf("%d interactive sign-ins recorded, want 1", len(es))
	}

	if rr := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/accounts/%d/credentials/%d", f.accountID, created.ID), f.session, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", rr.Code)
	}
	if _, err := imapMailbox.Folders(); !apiclient.IsAuthRejection(err) {
		t.Errorf("IMAP LIST after revoking = %v, want a rejection", err)
	}
	if _, err := pop3Mailbox.Messages(); !apiclient.IsAuthRejection(err) {
		t.Errorf("POP3 STAT after revoking = %v, want a rejection", err)
	}
	if _, err := imapgw.NewBackend(api, nil).Authenticate(f.address, created.Password); !apiclient.IsAuthRejection(err) {
		t.Errorf("IMAP sign-in after revoking = %v, want a rejection", err)
	}
}

// A new password for a mailbox revokes every app password it has, in the
// transaction that sets it: whoever learnt the old password could have made
// one, and it must not outlive the password, as no session does (#164). The
// gateway sessions open with them stop at their next command, each revocation
// is recorded with its reason, and another mailbox's app passwords go on
// working.
func TestAppPasswords_RevokedByANewPassword(t *testing.T) {
	f := newCredentialFixture(t)
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	api := apiclient.New(srv.URL)
	admin := adminToken(t, f.jwt, []string{middleware.CapMailboxesWrite})
	// Each test admin has an id of its own, holding its grants (#345).
	adminClaims, err := f.jwt.ValidateAccessToken(admin)
	if err != nil {
		t.Fatalf("the admin's token: %v", err)
	}
	mailboxPath := fmt.Sprintf("/api/v1/admin/mailboxes/%d", f.mailboxID)
	folders := fmt.Sprintf("/api/v1/accounts/%d/folders", f.accountID)

	thunderbird, n8n := f.createAppPassword("Thunderbird"), f.createAppPassword("n8n")
	works := func(t *testing.T, created createdCredential) {
		t.Helper()
		if rr := f.do(http.MethodGet, folders, basic(f.address, created.Password), ""); rr.Code != http.StatusOK {
			t.Errorf("folders with %d: %d %s", created.ID, rr.Code, rr.Body.String())
		}
	}

	// Another mailbox, with an app password of its own.
	var domain models.Domain
	f.db.First(&domain)
	danaID := f.seedMailbox(domain.ID, "dana")
	dana, danaPassword, err := repositories.NewCredentialRepository(f.db).CreateAppPassword(danaID, danaID, "Dana's phone", nil)
	if err != nil {
		t.Fatalf("an app password for dana@: %v", err)
	}
	danaFolders := fmt.Sprintf("/api/v1/accounts/%d/folders", danaID)

	// An IMAP and a POP3 session, open before the change.
	imapMailbox, err := imapgw.NewBackend(api, nil).Authenticate(f.address, thunderbird.Password)
	if err != nil {
		t.Fatalf("IMAP sign-in with the app password: %v", err)
	}
	pop3Mailbox, err := pop3gw.NewBackend(api, nil).Authenticate(f.address, n8n.Password)
	if err != nil {
		t.Fatalf("POP3 sign-in with the app password: %v", err)
	}

	t.Run("a change that sets no password leaves them", func(t *testing.T) {
		if rr := f.do(http.MethodPatch, mailboxPath, "Bearer "+admin, `{"display_name":"Chris"}`); rr.Code != http.StatusOK {
			t.Fatalf("PATCH display_name: %d %s", rr.Code, rr.Body.String())
		}
		if rr := f.do(http.MethodPatch, mailboxPath, "Bearer "+admin, `{"password":"short"}`); rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("PATCH a password too short: %d %s", rr.Code, rr.Body.String())
		}
		works(t, thunderbird)
		works(t, n8n)
		if es := f.entries(middleware.AuditActionCredentialsRevoke); len(es) != 0 {
			t.Errorf("revocations recorded: %+v", es)
		}
	})

	// The console's reset (restmail console, users, r) is this same PATCH.
	if err := api.ResetPassword(admin, f.mailboxID, "a-new-password-456"); err != nil {
		t.Fatalf("set a new password: %v", err)
	}

	t.Run("they stop at the next request", func(t *testing.T) {
		for _, created := range []createdCredential{thunderbird, n8n} {
			if rr := f.do(http.MethodGet, folders, basic(f.address, created.Password), ""); rr.Code != http.StatusUnauthorized {
				t.Errorf("folders with %d after a new password: %d", created.ID, rr.Code)
			}
		}
		es := f.entries(middleware.AuditActionBasicRefused)
		if len(es) == 0 || es[len(es)-1].Target["reason"] != auth.CredentialRevoked {
			t.Errorf("refusals after a new password: %+v", es)
		}
	})

	t.Run("open gateway sessions stop at their next command", func(t *testing.T) {
		if _, err := imapMailbox.Folders(); !apiclient.IsAuthRejection(err) {
			t.Errorf("IMAP LIST after a new password = %v, want a rejection", err)
		}
		if _, err := pop3Mailbox.Messages(); !apiclient.IsAuthRejection(err) {
			t.Errorf("POP3 STAT after a new password = %v, want a rejection", err)
		}
		// SMTP checks the credential before each MAIL FROM, with this store.
		if usable, err := smtpgw.NewStore(f.db, "").CredentialUsable(thunderbird.ID); err != nil || usable {
			t.Errorf("SMTP's check after a new password = %v, %v", usable, err)
		}
		if _, err := imapgw.NewBackend(api, nil).Authenticate(f.address, thunderbird.Password); !apiclient.IsAuthRejection(err) {
			t.Errorf("IMAP sign-in after a new password = %v, want a rejection", err)
		}
	})

	t.Run("their rows stay, revoked", func(t *testing.T) {
		var creds []models.Credential
		f.db.Where("account_id = ? AND kind = ?", f.mailboxID, models.CredentialKindAppPassword).Find(&creds)
		if len(creds) != 2 || creds[0].RevokedAt == nil || creds[1].RevokedAt == nil {
			t.Errorf("chris@'s credentials: %+v", creds)
		}
	})

	t.Run("each revocation is recorded with its reason", func(t *testing.T) {
		es := f.entries(middleware.AuditActionCredentialsRevoke)
		if len(es) != 2 {
			t.Fatalf("%d revocations recorded, want 2: %+v", len(es), es)
		}
		for i, created := range []createdCredential{thunderbird, n8n} {
			e := es[i]
			if e.Target["reason"] != handlers.CredentialRevokedPasswordChanged ||
				e.Target["credential_id"] != strconv.Itoa(int(created.ID)) || e.Target["public_id"] != created.PublicID ||
				e.Target["account_id"] != strconv.Itoa(int(f.mailboxID)) ||
				e.ActorKind != models.AuditActorAccount || e.ActorID == nil || *e.ActorID != adminClaims.AccountID ||
				e.Outcome != models.AuditOutcomeSuccess || e.Method != http.MethodPatch ||
				e.Route != "/api/v1/admin/mailboxes/{id}" || e.CredentialID != nil {
				t.Errorf("revocation %d: %+v", i, e)
			}
		}
		var all []models.AuditEntry
		f.db.Order("seq ASC").Find(&all)
		for _, e := range all {
			if b, _ := json.Marshal(e); strings.Contains(string(b), "a-new-password-456") {
				t.Errorf("entry %d holds the new password: %s", e.Seq, b)
			}
		}
		if res := audit.Verify(all); !res.OK {
			t.Errorf("the audit chain is broken: %+v", res)
		}
	})

	t.Run("another mailbox's are untouched", func(t *testing.T) {
		if rr := f.do(http.MethodGet, danaFolders, basic("dana@example.test", danaPassword), ""); rr.Code != http.StatusOK {
			t.Errorf("dana@'s app password after chris@'s new password: %d %s", rr.Code, rr.Body.String())
		}
		var row models.Credential
		if err := f.db.First(&row, dana.ID).Error; err != nil || row.RevokedAt != nil {
			t.Errorf("dana@'s credential: %+v, %v", row, err)
		}
	})

	t.Run("one made under the new password works", func(t *testing.T) {
		rr := f.do(http.MethodPost, "/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":"a-new-password-456","client":"native"}`, f.address))
		if rr.Code != http.StatusOK {
			t.Fatalf("sign in with the new password: %d %s", rr.Code, rr.Body.String())
		}
		var login struct {
			Data struct {
				AccessToken string `json:"access_token"`
			} `json:"data"`
		}
		decodeInto(t, rr, &login)
		f.session, f.password = "Bearer "+login.Data.AccessToken, "a-new-password-456"
		works(t, f.createAppPassword("Thunderbird again"))
	})
}

// The new password, the revocations and their audit entries commit together:
// when a revocation cannot be recorded, the password stays as it was and so do
// the app passwords.
func TestAppPasswords_NewPasswordAndItsRevocationsCommitTogether(t *testing.T) {
	f := newCredentialFixture(t)
	created := f.createAppPassword("Thunderbird")
	before := f.passwordHash()

	// This scratch database's audit log now refuses every entry.
	if err := f.db.Exec(`CREATE FUNCTION refuse_audit_entries() RETURNS trigger LANGUAGE plpgsql AS
		$$ BEGIN RAISE EXCEPTION 'audit log unavailable'; END $$`).Error; err != nil {
		t.Fatalf("create the trigger function: %v", err)
	}
	if err := f.db.Exec(`CREATE TRIGGER refuse_audit_entries BEFORE INSERT ON audit_log
		FOR EACH ROW EXECUTE FUNCTION refuse_audit_entries()`).Error; err != nil {
		t.Fatalf("create the trigger: %v", err)
	}

	admin := adminToken(t, f.jwt, []string{middleware.CapMailboxesWrite})
	rr := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/mailboxes/%d", f.mailboxID), "Bearer "+admin, `{"password":"a-new-password-456"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("a new password with no audit log: %d %s", rr.Code, rr.Body.String())
	}
	if after := f.passwordHash(); after != before {
		t.Error("the password changed although its revocations were not recorded")
	}
	var cred models.Credential
	if err := f.db.First(&cred, created.ID).Error; err != nil || cred.RevokedAt != nil {
		t.Errorf("the app password: %+v, %v", cred, err)
	}
}
