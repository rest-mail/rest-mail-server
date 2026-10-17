package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/gateway/apiclient"
	imapgw "github.com/restmail/restmail/internal/gateway/imap"
	"gorm.io/gorm"
)

// A fresh install, from an empty database to an admin who runs the server, an
// account with no password the admin uses, and an account linked by its
// password (#347): the server's own first start, then the real router, the
// SMTP gateway's submission port and the IMAP gateway's backend.

const (
	freshDomain   = "fresh.test"
	freshAdmin    = "admin@" + freshDomain
	freshDeployPW = "the installer's deploy password"
	freshAdminPW  = "the admin's own password, chosen"
)

// freshInstall is the server after its first start.
type freshInstall struct {
	t      *testing.T
	db     *gorm.DB
	router http.Handler
}

// firstStart runs what cmd/api runs on start against db: migrate, the roles,
// the bootstrap account from the deploy-time password, and the check that
// someone can sign in.
func firstStart(t *testing.T, db *gorm.DB) bootstrap.FirstAdminOutcome {
	t.Helper()
	if err := rmdb.AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := bootstrap.EnsureRBAC(db); err != nil {
		t.Fatalf("roles: %v", err)
	}
	credential, err := bootstrap.BootstrapFromConfig(freshDeployPW, "")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := bootstrap.EnsureFirstAdminWith(db, freshAdmin, credential, true, time.Now())
	if err != nil {
		t.Fatalf("the bootstrap account: %v", err)
	}
	if err := bootstrap.RequireAdmin(db); err != nil {
		t.Fatalf("no way in after the first start: %v", err)
	}
	return outcome
}

func (f *freshInstall) do(method, path, authorization, body string) *httptest.ResponseRecorder {
	f.t.Helper()
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

// expect fails the test unless rr has status want, and decodes its data into v.
func (f *freshInstall) expect(rr *httptest.ResponseRecorder, want int, what string, v any) {
	f.t.Helper()
	if rr.Code != want {
		f.t.Fatalf("%s: %d %s, want %d", what, rr.Code, rr.Body.String(), want)
	}
	if v != nil {
		decodeInto(f.t, rr, &struct {
			Data any `json:"data"`
		}{v})
	}
}

type freshSession struct {
	AccessToken   string   `json:"access_token"`
	Capabilities  []string `json:"capabilities"`
	SetupRequired []string `json:"setup_required"`
	User          struct {
		ID    uint   `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
}

func (s freshSession) bearer() string { return "Bearer " + s.AccessToken }

func (f *freshInstall) login(address, password, code string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"email": address, "password": password, "totp_code": code, "client": "native"})
	return f.do(http.MethodPost, "/api/v1/auth/login", "", string(body))
}

func (f *freshInstall) signIn(address, password, code string) freshSession {
	f.t.Helper()
	var s freshSession
	f.expect(f.login(address, password, code), http.StatusOK, "sign in as "+address, &s)
	return s
}

type freshAccount struct {
	ID          uint   `json:"id"`
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
	IsPrimary   bool   `json:"is_primary"`
}

func (f *freshInstall) accounts(session freshSession) []freshAccount {
	f.t.Helper()
	var out []freshAccount
	f.expect(f.do(http.MethodGet, "/api/v1/accounts", session.bearer(), ""), http.StatusOK, "list the accounts", &out)
	return out
}

func addresses(accounts []freshAccount) []string {
	out := make([]string, len(accounts))
	for i, a := range accounts {
		out[i] = a.Address
	}
	return out
}

func (f *freshInstall) audited(action, outcome string) int64 {
	var n int64
	f.db.Model(&models.AuditEntry{}).Where("action = ? AND outcome = ?", action, outcome).Count(&n)
	return n
}

func (f *freshInstall) inFolder(accountID uint, folder, subject string) bool {
	var n int64
	f.db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ? AND subject = ?", accountID, folder, subject).Count(&n)
	return n == 1
}

func TestFreshInstall_EndToEnd(t *testing.T) {
	db := createScratchDB(t, "fresh_install")
	useRealGrants(t)
	// The submission port delivers local mail through the inbound pipeline,
	// which looks up SPF and DMARC: nothing is published for fresh.test.
	serveTXT(t, map[string][]string{})

	// 1. The first start, on an empty database: the bootstrap account is
	// admin@<mail domain>, its domain comes with it, and it holds admin,
	// active, and superadmin, eligible. Starting again changes nothing.
	if outcome := firstStart(t, db); outcome != bootstrap.FirstAdminCreated {
		t.Fatalf("first start: outcome %v, want the bootstrap account created", outcome)
	}
	if outcome := firstStart(t, db); outcome != bootstrap.FirstAdminUnchanged {
		t.Fatalf("second start: outcome %v, want nothing changed", outcome)
	}
	var admin models.Mailbox
	if err := db.Preload("Domain").Where("address = ?", freshAdmin).First(&admin).Error; err != nil {
		t.Fatalf("no bootstrap account %s: %v", freshAdmin, err)
	}
	if admin.Domain.Name != freshDomain {
		t.Fatalf("the bootstrap account's domain is %q", admin.Domain.Name)
	}
	var roles []string
	db.Table("grants").Joins("JOIN roles ON roles.id = grants.role_id").
		Where("grants.holder_kind = ? AND grants.holder_id = ? AND grants.scope = '*'", models.GrantHolderAccount, admin.ID).
		Order("roles.name").Pluck("roles.name || ' ' || grants.mode", &roles)
	if !slices.Equal(roles, []string{"admin active", "superadmin eligible"}) {
		t.Fatalf("the bootstrap account's grants: %v", roles)
	}

	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	routers := NewRouters(db, jwtSvc, &config.Config{Environment: "test", TOTP2FAEnabled: true, MasterKey: accessMasterKey}, nil)
	t.Cleanup(routers.Close)
	f := &freshInstall{t: t, db: db, router: routers.Public}
	gateway, submission, rootCAs := startSubmission(t, db, f.router)

	// 2. Signing in with the deploy password, by address: a session in setup
	// that reaches nothing but setting up. A username is not an address.
	setup := f.signIn(freshAdmin, freshDeployPW, "")
	if !slices.Equal(setup.SetupRequired, []string{"password", "two_factor"}) || len(setup.Capabilities) != 0 {
		t.Fatalf("the deploy password's session: setup_required %v, capabilities %v", setup.SetupRequired, setup.Capabilities)
	}
	if setup.User.ID != admin.ID || setup.User.Email != freshAdmin {
		t.Fatalf("the session is %+v, want the account %d %s", setup.User, admin.ID, freshAdmin)
	}
	f.expect(f.do(http.MethodGet, "/api/v1/admin/stats", setup.bearer(), ""), http.StatusForbidden, "the admin API in setup", nil)
	f.expect(f.do(http.MethodGet, "/api/v1/accounts", setup.bearer(), ""), http.StatusForbidden, "the accounts in setup", nil)
	f.expect(f.do(http.MethodPost, "/api/v1/auth/login", "", fmt.Sprintf(`{"username":"admin","password":%q}`, freshDeployPW)),
		http.StatusUnprocessableEntity, "a sign-in by username", nil)

	// 3. A password of its own, then an authenticator; the deploy password is
	// dead, and signing in takes the code.
	f.expect(f.do(http.MethodPost, "/api/v1/auth/password", setup.bearer(),
		fmt.Sprintf(`{"current_password":%q,"new_password":%q}`, freshDeployPW, freshAdminPW)), http.StatusOK, "choose a password", nil)
	f.expect(f.login(freshAdmin, freshDeployPW, ""), http.StatusUnauthorized, "the deploy password after the change", nil)
	setup = f.signIn(freshAdmin, freshAdminPW, "")
	if !slices.Equal(setup.SetupRequired, []string{"two_factor"}) {
		t.Fatalf("after the password: setup_required %v", setup.SetupRequired)
	}
	var enrolled struct {
		Secret string `json:"secret"`
	}
	f.expect(f.do(http.MethodPost, "/api/v1/auth/2fa/enroll", setup.bearer(), ""), http.StatusOK, "enroll", &enrolled)
	code := func() string {
		c, err := auth.GenerateTOTPCode(enrolled.Secret, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	f.expect(f.do(http.MethodPost, "/api/v1/auth/2fa/confirm", setup.bearer(), fmt.Sprintf(`{"code":%q}`, code())), http.StatusNoContent, "confirm", nil)
	f.expect(f.login(freshAdmin, freshAdminPW, ""), http.StatusUnauthorized, "signing in without the code", nil)
	adminS := f.signIn(freshAdmin, freshAdminPW, code())
	if len(adminS.SetupRequired) != 0 {
		t.Fatalf("still in setup: %v", adminS.SetupRequired)
	}

	// 4. The admin works: its capabilities come from its grants (admin, not
	// superadmin), and the admin API answers.
	if !slices.Contains(adminS.Capabilities, "mailboxes:write") || slices.Contains(adminS.Capabilities, "*") {
		t.Fatalf("the admin's capabilities: %v", adminS.Capabilities)
	}
	f.expect(f.do(http.MethodGet, "/api/v1/admin/stats", adminS.bearer(), ""), http.StatusOK, "the admin's stats", nil)
	var domains []struct {
		ID     uint   `json:"id"`
		Name   string `json:"name"`
		Active bool   `json:"active"`
	}
	f.expect(f.do(http.MethodGet, "/api/v1/admin/domains", adminS.bearer(), ""), http.StatusOK, "the admin's domains", &domains)
	if len(domains) != 1 || domains[0].Name != freshDomain || domains[0].Active {
		t.Fatalf("the domains: %+v, want %s, not live", domains, freshDomain)
	}
	// The domain is not live until it has a certificate (#295). The install's
	// certificate arrives, and the admin puts the domain live.
	if err := db.Create(&models.Certificate{DomainID: domains[0].ID, CertPEM: "-- certificate --", KeyPEM: "-- key --",
		Issuer: "the install", NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	f.expect(f.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/domains/%d", domains[0].ID), adminS.bearer(), `{"active":true}`),
		http.StatusOK, "put the domain live", nil)

	// 5. chris@ made with no password: the admin owns it, in the same
	// transaction as the account, and it is recorded.
	var chris freshAccount
	f.expect(f.do(http.MethodPost, "/api/v1/accounts", adminS.bearer(), `{"address":"chris@fresh.test","display_name":"Chris"}`),
		http.StatusCreated, "create chris@", &chris)
	if chris.Address != "chris@fresh.test" || chris.DisplayName != "Chris" || chris.IsPrimary || chris.ID == 0 {
		t.Fatalf("created %+v", chris)
	}
	var passwords int64
	db.Model(&models.Credential{}).Where("account_id = ? AND kind = ?", chris.ID, models.CredentialKindPassword).Count(&passwords)
	if passwords != 0 {
		t.Fatalf("chris@ has %d passwords, want none", passwords)
	}
	if n := f.audited("accounts.create", models.AuditOutcomeSuccess); n != 1 {
		t.Fatalf("accounts.create recorded %d times, want once", n)
	}
	f.expect(f.do(http.MethodPost, "/api/v1/accounts", adminS.bearer(), `{"address":"chris@fresh.test"}`), http.StatusConflict, "create chris@ again", nil)

	// 6. Listed: its own first, then the accounts it holds mail grants on; the
	// ids are mailbox ids.
	if got := f.accounts(adminS); !slices.Equal(addresses(got), []string{freshAdmin, "chris@fresh.test"}) ||
		!got[0].IsPrimary || got[0].ID != admin.ID || got[1].ID != chris.ID {
		t.Fatalf("the admin's accounts: %+v", got)
	}

	// 7. admin@ sends as chris@, and mail to chris@ arrives where admin@ reads
	// it. Over SMTP admin@ signs in with an app password: its password has 2FA
	// behind it, so the gateway refuses it.
	sent := "sent as chris@"
	f.expect(f.do(http.MethodPost, "/api/v1/messages/send", adminS.bearer(),
		fmt.Sprintf(`{"from":"chris@fresh.test","to":["someone@elsewhere.test"],"subject":%q,"body_text":"hello"}`, sent)),
		http.StatusCreated, "send as chris@", nil)
	if !f.inFolder(chris.ID, "Sent", sent) {
		t.Fatal("what admin@ sent as chris@ is not in chris@'s Sent")
	}
	var adminApp createdCredential
	f.expect(f.do(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/credentials", admin.ID), adminS.bearer(),
		fmt.Sprintf(`{"name":"Thunderbird","password":%q}`, freshAdminPW)), http.StatusCreated, "admin@'s app password", &adminApp)
	if _, err := gateway.SignIn(freshAdmin, freshAdminPW, "smtp"); !apiclient.IsAuthRejection(err) {
		t.Fatalf("admin@'s password over SMTP = %v, want refused", err)
	}
	received := "to chris@, over SMTP"
	c := dialSubmission(t, submission, rootCAs, freshAdmin, adminApp.Password)
	if err := send(c, freshAdmin, "chris@fresh.test", submittedMessage(freshAdmin, "chris@fresh.test", received)); err != nil {
		t.Fatalf("send to chris@ over SMTP: %v", err)
	}
	var inbox []struct {
		Subject string `json:"subject"`
	}
	f.expect(f.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages", chris.ID), adminS.bearer(), ""),
		http.StatusOK, "chris@'s INBOX", &inbox)
	if len(inbox) != 1 || inbox[0].Subject != received {
		t.Fatalf("chris@'s INBOX: %+v", inbox)
	}

	// 8. chris@ has no password: nothing signs in to it, by API or by gateway.
	for _, pw := range []string{"", freshAdminPW, freshDeployPW} {
		if rr := f.login("chris@fresh.test", pw, ""); rr.Code != http.StatusUnauthorized && rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("signing in to chris@ with %q: %d %s", pw, rr.Code, rr.Body.String())
		}
	}
	if _, err := imapgw.NewBackend(gateway, nil).Authenticate("chris@fresh.test", freshAdminPW); !apiclient.IsAuthRejection(err) {
		t.Fatalf("chris@ over IMAP = %v, want refused", err)
	}

	// 9. Another person links an account by proving its password. bob@ and
	// carol@ are made with passwords; bob@ sees only itself until it links
	// carol@, then reads carol@'s mail; a wrong password links nothing;
	// unlinking takes the grant away. A refused link is 403 with its reason,
	// never 401: the secrets were another account's, and bob@'s session goes
	// on working.
	var bob, carol freshAccount
	f.expect(f.do(http.MethodPost, "/api/v1/accounts", adminS.bearer(), `{"address":"bob@fresh.test","password":"bob's own password"}`),
		http.StatusCreated, "create bob@", &bob)
	f.expect(f.do(http.MethodPost, "/api/v1/accounts", adminS.bearer(), `{"address":"carol@fresh.test","password":"carol's own password"}`),
		http.StatusCreated, "create carol@", &carol)
	bobS := f.signIn("bob@fresh.test", "bob's own password", "")
	if got := addresses(f.accounts(bobS)); !slices.Equal(got, []string{"bob@fresh.test"}) {
		t.Fatalf("bob@'s accounts before linking: %v", got)
	}
	f.expect(f.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/folders", carol.ID), bobS.bearer(), ""), http.StatusForbidden, "carol@'s folders before linking", nil)
	refused := func(body, want, what string) {
		t.Helper()
		rr := f.do(http.MethodPost, "/api/v1/accounts/link", bobS.bearer(), body)
		if got := errorCode(t, rr); rr.Code != http.StatusForbidden || got != want {
			t.Fatalf("%s: %d %s, want 403 %s", what, rr.Code, got, want)
		}
	}
	wrongCode := code()
	wrongCode = string('0'+(wrongCode[0]-'0'+1)%10) + wrongCode[1:]
	refused(`{"address":"carol@fresh.test","password":"not carol's password"}`, "invalid_credentials", "link with a wrong password")
	refused(fmt.Sprintf(`{"address":%q,"password":%q}`, freshAdmin, freshAdminPW), "totp_required", "link admin@ without its code")
	refused(fmt.Sprintf(`{"address":%q,"password":%q,"totp_code":%q}`, freshAdmin, freshAdminPW, wrongCode), "invalid_totp", "link admin@ with a wrong code")
	if got := addresses(f.accounts(bobS)); !slices.Equal(got, []string{"bob@fresh.test"}) {
		t.Fatalf("bob@'s accounts after the refused links: %v", got)
	}
	var linked freshAccount
	f.expect(f.do(http.MethodPost, "/api/v1/accounts/link", bobS.bearer(), `{"address":"carol@fresh.test","password":"carol's own password"}`),
		http.StatusCreated, "link carol@", &linked)
	if linked.ID != carol.ID {
		t.Fatalf("linked %+v, want carol@ %d", linked, carol.ID)
	}
	if got := addresses(f.accounts(bobS)); !slices.Equal(got, []string{"bob@fresh.test", "carol@fresh.test"}) {
		t.Fatalf("bob@'s accounts after linking: %v", got)
	}
	f.expect(f.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/folders", carol.ID), bobS.bearer(), ""), http.StatusOK, "carol@'s folders, linked", nil)
	if f.audited("accounts.link", models.AuditOutcomeSuccess) != 1 || f.audited("accounts.link", models.AuditOutcomeDenied) != 3 {
		t.Fatal("the link and the three refused links are not each recorded once")
	}
	f.expect(f.do(http.MethodDelete, fmt.Sprintf("/api/v1/accounts/%d", carol.ID), bobS.bearer(), ""), http.StatusNoContent, "unlink carol@", nil)
	if got := addresses(f.accounts(bobS)); !slices.Equal(got, []string{"bob@fresh.test"}) {
		t.Fatalf("bob@'s accounts after unlinking: %v", got)
	}

	// 10. App passwords over IMAP: admin@'s own, and one admin@ makes for
	// chris@, which it owns (proving its own password). Each reaches its own
	// account only, and neither signs in to the API.
	var chrisApp createdCredential
	f.expect(f.do(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/credentials", chris.ID), adminS.bearer(),
		fmt.Sprintf(`{"name":"chris@'s phone","password":%q}`, freshAdminPW)), http.StatusCreated, "an app password for chris@", &chrisApp)
	for _, signIn := range []struct {
		address, password string
		id                uint
	}{{freshAdmin, adminApp.Password, admin.ID}, {"chris@fresh.test", chrisApp.Password, chris.ID}} {
		box, err := imapgw.NewBackend(gateway, nil).Authenticate(signIn.address, signIn.password)
		if err != nil {
			t.Fatalf("%s over IMAP with its app password: %v", signIn.address, err)
		}
		messages, err := box.Messages("INBOX")
		if err != nil {
			t.Fatalf("%s's INBOX over IMAP: %v", signIn.address, err)
		}
		if signIn.id == chris.ID && (len(messages) != 1 || messages[0].Subject != received) {
			t.Fatalf("chris@'s INBOX over IMAP: %+v", messages)
		}
		f.expect(f.login(signIn.address, signIn.password, ""), http.StatusUnauthorized, signIn.address+"'s app password at sign-in", nil)
	}
	if _, err := imapgw.NewBackend(gateway, nil).Authenticate("chris@fresh.test", adminApp.Password); !apiclient.IsAuthRejection(err) {
		t.Fatalf("admin@'s app password for chris@ over IMAP = %v, want refused", err)
	}

	if res, err := audit.VerifyDB(db); err != nil || !res.OK {
		t.Fatalf("audit chain: %+v, %v", res, err)
	}
}
