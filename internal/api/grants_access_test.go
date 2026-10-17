package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/crypto"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// These tests run the real router on a real database of their own: accounts
// holding roles at "*" and on domains, signed in as each kind of admin, with
// every route checked against the capabilities its roles hold.

const (
	accessPassword  = "an admin's own password, long"
	accessMasterKey = "grants-access-master-key-0123456789abcdef"
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// openAccessDB is a database of the test's own, dropped afterwards, migrated as
// a server before grants migrated it. It skips (never fails) when no database
// is reachable.
func openAccessDB(t *testing.T) *gorm.DB {
	t.Helper()
	port, _ := strconv.Atoi(envOrDefault("DB_PORT", "5432"))
	cfg := config.Config{
		DBHost: envOrDefault("DB_HOST", "localhost"),
		DBPort: port,
		DBName: envOrDefault("DB_NAME", "restmail"),
		DBUser: envOrDefault("DB_USER", "restmail"),
		DBPass: envOrDefault("DB_PASS", "restmail"),
	}
	server, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Skipf("grants access test skipped: no database reachable (%v)", err)
	}
	name := fmt.Sprintf("restmail_grants_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := server.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Skipf("grants access test skipped: cannot create a database (%v)", err)
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
		server.Exec("DROP DATABASE IF EXISTS " + name)
		if sqlDB, err := server.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := rmdb.AutoMigrate(scratch); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := bootstrap.EnsureRBAC(scratch); err != nil {
		t.Fatalf("roles: %v", err)
	}
	return scratch
}

// useRealGrants puts back what routes_seams_test.go stands aside, for one test.
func useRealGrants(t *testing.T) {
	session, held := sessionCheck, heldGrants
	sessionCheck, heldGrants = cachedSessionCheck, cachedHeldGrants
	t.Cleanup(func() { sessionCheck, heldGrants = session, held })
}

func accessRouter(t *testing.T, db *gorm.DB) http.Handler {
	t.Helper()
	useRealGrants(t)
	jwtSvc := auth.NewJWTService("grants-access-secret", 15*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins: []string{"http://localhost:3000"},
		Environment:        "test",
		TOTP2FAEnabled:     true,
		MasterKey:          accessMasterKey,
	}
	return NewRouter(db, jwtSvc, cfg, nil)
}

// fixtureAdmin is an admin: an account holding roles at "*".
type fixtureAdmin struct {
	name      string
	roles     []string
	inactive  bool
	twoFactor bool // enrolled, and so signs in with a code
	setup     bool // the bootstrap admin, not yet set up
}

var fixtureAdmins = []fixtureAdmin{
	{name: "root", roles: []string{"superadmin"}},
	{name: "owner", roles: []string{"superadmin"}, twoFactor: true},
	{name: "ops", roles: []string{"admin"}},
	{name: "viewer", roles: []string{"readonly"}},
	{name: "both", roles: []string{"admin", "readonly"}},
	{name: "nobody"},
	{name: "fresh", roles: []string{"superadmin"}, setup: true},
	{name: "gone", roles: []string{"admin"}, inactive: true},
}

type seededAdmin struct {
	fixtureAdmin
	user   models.Mailbox
	secret string // TOTP secret, when twoFactor
}

// seedAdmins makes each admin an account of access.test, with the
// password accessPassword, holding each of its roles actively at "*".
func seedAdmins(t *testing.T, db *gorm.DB, admins []fixtureAdmin) map[string]*seededAdmin {
	t.Helper()
	hash, err := auth.HashPassword(accessPassword)
	if err != nil {
		t.Fatal(err)
	}
	var domain models.Domain
	if err := db.Where("name = ?", "access.test").FirstOrCreate(&domain, models.Domain{Name: "access.test", ServerType: "restmail", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]*seededAdmin{}
	for _, a := range admins {
		s := &seededAdmin{fixtureAdmin: a}
		s.user = models.Mailbox{DomainID: domain.ID, LocalPart: a.name, Address: a.name + "@access.test", Active: true}
		if a.setup {
			expires := time.Now().Add(time.Hour)
			s.user.PasswordChangeRequired, s.user.TwoFactorRequired, s.user.BootstrapExpiresAt = true, true, &expires
		}
		if a.twoFactor {
			s.user.TwoFactorRequired = true
		}
		if err := db.Create(&s.user).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := repositories.NewCredentialRepository(db).SetPassword(s.user.ID, hash, nil, time.Now()); err != nil {
			t.Fatal(err)
		}
		if a.inactive {
			if err := db.Model(&s.user).Update("active", false).Error; err != nil {
				t.Fatal(err)
			}
		}
		for _, roleName := range a.roles {
			var role models.Role
			if err := db.Where("name = ?", roleName).First(&role).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: s.user.ID, RoleID: role.ID,
				Scope: grants.All, Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
				t.Fatal(err)
			}
		}
		if a.twoFactor {
			s.secret = enrollTOTP(t, db, s.user.ID)
		}
		out[a.name] = s
	}
	return out
}

func enrollTOTP(t *testing.T, db *gorm.DB, adminID uint) string {
	t.Helper()
	key, err := auth.GenerateTOTPSecret(auth.DefaultTOTPIssuer, "admin-"+strconv.FormatUint(uint64(adminID), 10))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.EncryptString(key.Secret(), accessMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	repo := repositories.NewTwoFactorRepository(db)
	tf, err := repo.Enroll(adminID, enc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Confirm(tf.ID); err != nil {
		t.Fatal(err)
	}
	return key.Secret()
}

// roleCapabilities is every capability of every role the admin holds at
// "*": what it may do anywhere.
func roleCapabilities(t *testing.T, db *gorm.DB, adminID uint) []string {
	t.Helper()
	var names []string
	err := db.Raw(`SELECT DISTINCT c.name FROM capabilities c
		JOIN role_capabilities rc ON rc.capability_id = c.id
		JOIN grants g ON g.role_id = rc.role_id
		WHERE g.holder_id = ? AND g.scope = '*' AND g.mode = 'active' ORDER BY c.name`, adminID).Scan(&names).Error
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// rolesAllow is the rule: a route wanting need is reached by an admin
// holding need, or "*".
func rolesAllow(caps []string, need string) bool {
	if need == "" {
		return true
	}
	for _, c := range caps {
		if c == "*" || c == need {
			return true
		}
	}
	return false
}

type accessSession struct {
	status        int
	code          string
	AccessToken   string   `json:"access_token"`
	Capabilities  []string `json:"capabilities"`
	SetupRequired []string `json:"setup_required"`
}

func (s *seededAdmin) login(t *testing.T, router http.Handler) accessSession {
	t.Helper()
	body := map[string]string{"email": s.user.Address, "password": accessPassword, "client": "native"}
	if s.secret != "" {
		code, err := auth.GenerateTOTPCode(s.secret, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		body["totp_code"] = code
	}
	b, _ := json.Marshal(body)
	rr := doRequest(router, http.MethodPost, "/api/v1/auth/login", "", string(b))
	var env struct {
		Data  accessSession `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	env.Data.status, env.Data.code = rr.Code, env.Error.Code
	return env.Data
}

// refusedForAccess reports whether the request was refused for who is asking,
// rather than reaching its handler.
func refusedForAccess(rr *httptest.ResponseRecorder) bool {
	if rr.Code == http.StatusUnauthorized {
		return true
	}
	if rr.Code != http.StatusForbidden {
		return false
	}
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	return middlewareDenialMessages[resp.Error.Message] || resp.Error.Code == "setup_required"
}

type accessRoute struct {
	method, path, body string
	need               string // the capability the route wants; "" for any admin
}

// accessRoutes are representative admin routes: reads and writes of every
// resource, with bodies and ids that change nothing when they get through.
func accessRoutes(domainID, mailboxID uint) []accessRoute {
	d, m := strconv.FormatUint(uint64(domainID), 10), strconv.FormatUint(uint64(mailboxID), 10)
	return []accessRoute{
		{"GET", "/api/v1/admin/stats", "", "domains:read"},
		{"GET", "/api/v1/admin/grants", "", "users:read"},
		{"POST", "/api/v1/admin/grants", `{}`, "users:write"},
		{"DELETE", "/api/v1/admin/grants/999999", "", "users:write"},
		{"GET", "/api/v1/admin/roles", "", "users:read"},
		{"GET", "/api/v1/admin/capabilities", "", "users:read"},
		{"GET", "/api/v1/admin/domains", "", "domains:read"},
		{"POST", "/api/v1/admin/domains", `{}`, "domains:write"},
		{"GET", "/api/v1/admin/domains/" + d, "", "domains:read"},
		{"PATCH", "/api/v1/admin/domains/999999", `{}`, "domains:write"},
		{"DELETE", "/api/v1/admin/domains/999999", "", "domains:delete"},
		{"GET", "/api/v1/admin/domains/" + d + "/allowlist", "", "domains:read"},
		{"GET", "/api/v1/admin/mailboxes", "", "mailboxes:read"},
		{"GET", "/api/v1/admin/mailboxes/" + m, "", "mailboxes:read"},
		{"PATCH", "/api/v1/admin/mailboxes/999999", `{}`, "mailboxes:write"},
		{"DELETE", "/api/v1/admin/mailboxes/999999", "", "mailboxes:delete"},
		{"GET", "/api/v1/admin/aliases", "", "mailboxes:read"},
		{"GET", "/api/v1/admin/pipelines", "", "pipelines:read"},
		{"DELETE", "/api/v1/admin/pipelines/999999", "", "pipelines:delete"},
		{"GET", "/api/v1/admin/pipelines/analytics", "", "observability:read"},
		{"GET", "/api/v1/admin/queue", "", "queue:read"},
		{"POST", "/api/v1/admin/queue/999999/retry", "", "queue:manage"},
		{"GET", "/api/v1/admin/bans", "", "bans:read"},
		{"POST", "/api/v1/admin/bans", `{}`, "bans:write"},
		{"DELETE", "/api/v1/admin/bans/999999", "", "bans:delete"},
		{"GET", "/api/v1/admin/dkim", "", "domains:read"},
		{"GET", "/api/v1/admin/logs/delivery", "", "messages:read"},
		{"GET", "/api/v1/admin/audit", "", "audit:read"},
		{"GET", "/api/v1/admin/audit/verify", "", "audit:read"},
	}
}

func seedDomain(t *testing.T, db *gorm.DB, name string) (models.Domain, models.Mailbox) {
	t.Helper()
	d := models.Domain{Name: name, ServerType: "restmail", Active: true}
	if err := db.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	m := models.Mailbox{DomainID: d.ID, LocalPart: "x", Address: "x@" + name, Active: true}
	if err := db.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	return d, m
}

// displayEqual says whether the capabilities a login answered with show the
// same access as before: "*" alone when the account had "*", else the same set.
func displayEqual(before, after []string) bool {
	if contains(before, "*") {
		return len(after) == 1 && after[0] == "*"
	}
	a, b := append([]string(nil), before...), append([]string(nil), after...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, ",") == strings.Join(b, ",")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestGrants_EveryAdminReachesWhatItsRolesHold is the proof the admin API
// checks grants: each admin, an account holding roles at "*", reaches exactly
// the routes its roles' capabilities reach, and is shown those capabilities.
// One in setup reaches none; one disabled cannot sign in; one holding nothing
// is refused everywhere.
func TestGrants_EveryAdminReachesWhatItsRolesHold(t *testing.T) {
	db := openAccessDB(t)
	domain, mailbox := seedDomain(t, db, "a.test")
	admins := seedAdmins(t, db, fixtureAdmins)

	before := map[string][]string{}
	for name, a := range admins {
		before[name] = roleCapabilities(t, db, a.user.ID)
	}

	router := accessRouter(t, db)
	routes := accessRoutes(domain.ID, mailbox.ID)
	for _, a := range fixtureAdmins {
		a := admins[a.name]
		t.Run(a.name, func(t *testing.T) {
			s := a.login(t, router)
			if a.inactive {
				if s.status != http.StatusUnauthorized {
					t.Fatalf("a disabled admin signed in: %d", s.status)
				}
				return
			}
			if s.status != http.StatusOK {
				t.Fatalf("sign-in = %d %s", s.status, s.code)
			}
			if a.setup {
				if len(s.SetupRequired) == 0 || len(s.Capabilities) != 0 {
					t.Fatalf("a bootstrap admin in setup got setup_required=%v capabilities=%v", s.SetupRequired, s.Capabilities)
				}
			} else if !displayEqual(before[a.name], s.Capabilities) {
				t.Errorf("capabilities shown = %v, before %v", s.Capabilities, before[a.name])
			}
			for _, rt := range routes {
				rr := doRequest(router, rt.method, rt.path, s.AccessToken, rt.body)
				if rr.Code >= 500 {
					t.Errorf("%s %s: %d %s", rt.method, rt.path, rr.Code, rr.Body.String())
				}
				wantReached := !a.setup && rolesAllow(before[a.name], rt.need)
				if reached := !refusedForAccess(rr); reached != wantReached {
					t.Errorf("%s %s: reached=%v (status %d %s), its roles say %v",
						rt.method, rt.path, reached, rr.Code, strings.TrimSpace(rr.Body.String()), wantReached)
				}
				if a.setup && rr.Code == http.StatusForbidden && !strings.Contains(rr.Body.String(), "setup_required") {
					t.Errorf("%s %s: a session in setup refused with %s, want setup_required", rt.method, rt.path, rr.Body.String())
				}
			}
		})
	}

	if res, err := audit.VerifyDB(db); err != nil || !res.OK {
		t.Fatalf("audit chain: %+v, %v", res, err)
	}
}

// grantsFixture is a server with the admins above, the router, and two
// domains, each with a mailbox.
type grantsFixture struct {
	db     *gorm.DB
	router http.Handler
	admins map[string]*seededAdmin
	a, b   models.Domain
	ma, mb models.Mailbox
}

func newGrantsFixture(t *testing.T, extra ...fixtureAdmin) *grantsFixture {
	t.Helper()
	db := openAccessDB(t)
	f := &grantsFixture{db: db}
	f.a, f.ma = seedDomain(t, db, "a.test")
	f.b, f.mb = seedDomain(t, db, "b.test")
	f.admins = seedAdmins(t, db, append(append([]fixtureAdmin(nil), fixtureAdmins...), extra...))
	f.router = accessRouter(t, db)
	return f
}

func (f *grantsFixture) token(t *testing.T, name string) string {
	t.Helper()
	s := f.admins[name].login(t, f.router)
	if s.status != http.StatusOK {
		t.Fatalf("%s: sign-in = %d %s", name, s.status, s.code)
	}
	return s.AccessToken
}

func (f *grantsFixture) grant(t *testing.T, token string, holder uint, role, scope string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"holder":{"kind":"account","id":%d},"role":%q,"scope":%q}`, holder, role, scope)
	return doRequest(f.router, http.MethodPost, "/api/v1/admin/grants", token, body)
}

func grantID(t *testing.T, rr *httptest.ResponseRecorder) uint {
	t.Helper()
	var env struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil || env.Data.ID == 0 {
		t.Fatalf("no grant in %d %s", rr.Code, rr.Body.String())
	}
	return env.Data.ID
}

func grantErrorCode(rr *httptest.ResponseRecorder) string {
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	return resp.Error.Code
}

// A domain admin on domain:a.test reaches a.test and its mailboxes, and is
// refused on b.test, on lists (which want "*"), and on anything else.
func TestGrants_DomainAdminIsBoundedByItsDomain(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "dom"})
	owner := f.token(t, "owner")
	dom := f.admins["dom"].user.ID
	if rr := f.grant(t, owner, dom, "admin", "domain:a.test"); rr.Code != http.StatusCreated {
		t.Fatalf("grant admin on domain:a.test = %d %s", rr.Code, rr.Body.String())
	}

	s := f.admins["dom"].login(t, f.router)
	if len(s.Capabilities) != 0 {
		t.Errorf("a domain admin is shown %v: nothing is held at *", s.Capabilities)
	}
	id := func(v uint) string { return strconv.FormatUint(uint64(v), 10) }
	cases := []struct {
		method, path, body string
		reached            bool
	}{
		{"GET", "/api/v1/admin/domains/" + id(f.a.ID), "", true},
		{"PATCH", "/api/v1/admin/domains/" + id(f.a.ID), `{}`, true},
		{"GET", "/api/v1/admin/domains/" + id(f.a.ID) + "/blocklist", "", true},
		{"GET", "/api/v1/admin/mailboxes/" + id(f.ma.ID), "", true},
		{"GET", "/api/v1/admin/domains/" + id(f.b.ID), "", false},
		{"PATCH", "/api/v1/admin/domains/" + id(f.b.ID), `{}`, false},
		{"DELETE", "/api/v1/admin/domains/" + id(f.b.ID), "", false},
		{"GET", "/api/v1/admin/domains/" + id(f.b.ID) + "/blocklist", "", false},
		{"GET", "/api/v1/admin/mailboxes/" + id(f.mb.ID), "", false},
		{"DELETE", "/api/v1/admin/mailboxes/" + id(f.mb.ID), "", false},
		{"GET", "/api/v1/admin/domains/999999", "", false},
		{"GET", "/api/v1/admin/domains", "", false},
		{"GET", "/api/v1/admin/mailboxes", "", false},
		{"GET", "/api/v1/admin/queue", "", false},
	}
	for _, c := range cases {
		rr := doRequest(f.router, c.method, c.path, s.AccessToken, c.body)
		if reached := !refusedForAccess(rr); reached != c.reached {
			t.Errorf("%s %s: reached=%v (%d %s), want %v", c.method, c.path, reached, rr.Code, rr.Body.String(), c.reached)
		}
	}
	// b.test is still there.
	var n int64
	f.db.Model(&models.Domain{}).Where("id = ?", f.b.ID).Count(&n)
	if n != 1 {
		t.Fatal("the domain admin deleted another domain")
	}
}

// Eligible grants, and expired ones, give nothing.
func TestGrants_EligibleAndExpiredGrantsDoNothing(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "waiting"}, fixtureAdmin{name: "lapsed"})
	var admin models.Role
	f.db.Where("name = ?", "admin").First(&admin)
	past := time.Now().Add(-time.Minute)
	for _, g := range []models.Grant{
		{HolderKind: models.GrantHolderAccount, HolderID: f.admins["waiting"].user.ID, RoleID: admin.ID, Scope: "*", Mode: models.GrantEligible},
		{HolderKind: models.GrantHolderAccount, HolderID: f.admins["lapsed"].user.ID, RoleID: admin.ID, Scope: "*", Mode: models.GrantActive, ExpiresAt: &past},
	} {
		g.GrantedByKind, g.GrantedAt = models.AuditActorSystem, time.Now()
		if err := f.db.Create(&g).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"waiting", "lapsed"} {
		s := f.admins[name].login(t, f.router)
		if len(s.Capabilities) != 0 {
			t.Errorf("%s is shown %v", name, s.Capabilities)
		}
		if rr := doRequest(f.router, "GET", "/api/v1/admin/domains", s.AccessToken, ""); !refusedForAccess(rr) {
			t.Errorf("%s reached the domains list: %d", name, rr.Code)
		}
	}
}

// Changing grants needs a session that signed in with 2FA, and grants only
// what the granter holds, at a scope within its own.
func TestGrants_ChangingThemNeedsTwoFactorAndOnlyWhatYouHold(t *testing.T) {
	f := newGrantsFixture(t,
		fixtureAdmin{name: "delegate", twoFactor: true},
		fixtureAdmin{name: "target"},
	)
	// A role that may change grants but holds little else.
	delegate := models.Role{Name: "delegate"}
	reader := models.Role{Name: "domain-reader"}
	f.db.Create(&delegate)
	f.db.Create(&reader)
	for role, caps := range map[uint][]string{delegate.ID: {"users:read", "users:write", "domains:read"}, reader.ID: {"domains:read"}} {
		for _, name := range caps {
			var c models.Capability
			f.db.Where("name = ?", name).First(&c)
			f.db.Create(&models.RoleCapability{RoleID: role, CapabilityID: c.ID})
		}
	}
	f.db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: f.admins["delegate"].user.ID, RoleID: delegate.ID,
		Scope: "*", Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()})
	target := f.admins["target"].user.ID

	// root is a superadmin, but signed in without a second factor.
	root := f.token(t, "root")
	if rr := f.grant(t, root, target, "readonly", "*"); rr.Code != http.StatusForbidden || grantErrorCode(rr) != "two_factor_required" {
		t.Fatalf("grant without 2FA = %d %s, want 403 two_factor_required", rr.Code, rr.Body.String())
	}

	del := f.token(t, "delegate")
	for _, role := range []string{"readonly", "admin", "superadmin"} {
		if rr := f.grant(t, del, target, role, "*"); rr.Code != http.StatusForbidden || grantErrorCode(rr) != "grant_exceeds_held" {
			t.Errorf("delegate granting %s = %d %s, want 403 grant_exceeds_held", role, rr.Code, rr.Body.String())
		}
	}
	rr := f.grant(t, del, target, "domain-reader", "domain:a.test")
	if rr.Code != http.StatusCreated {
		t.Fatalf("delegate granting what it holds = %d %s", rr.Code, rr.Body.String())
	}
	readerGrant := grantID(t, rr)
	if rr := f.grant(t, del, target, "domain-reader", "domain:a.test"); rr.Code != http.StatusConflict {
		t.Errorf("the same grant again = %d, want 409", rr.Code)
	}
	if rr := f.grant(t, del, target, "domain-reader", "folder:x"); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("a bad scope = %d, want 422", rr.Code)
	}

	// Removing is held to the same rule: delegate cannot take away the
	// owner's superadmin.
	var ownerSuper models.Grant
	f.db.Joins("JOIN roles r ON r.id = grants.role_id").
		Where("grants.holder_id = ? AND r.name = ?", f.admins["owner"].user.ID, "superadmin").First(&ownerSuper)
	del2 := doRequest(f.router, "DELETE", "/api/v1/admin/grants/"+strconv.FormatUint(uint64(ownerSuper.ID), 10), del, "")
	if del2.Code != http.StatusForbidden || grantErrorCode(del2) != "grant_exceeds_held" {
		t.Errorf("delegate removing superadmin = %d %s, want 403 grant_exceeds_held", del2.Code, del2.Body.String())
	}

	// The grant works at once (the cache is emptied), and stops at once.
	tgt := f.token(t, "target")
	aPath := "/api/v1/admin/domains/" + strconv.FormatUint(uint64(f.a.ID), 10)
	if rr := doRequest(f.router, "GET", aPath, tgt, ""); refusedForAccess(rr) {
		t.Fatalf("the granted domain read was refused: %d", rr.Code)
	}
	owner := f.token(t, "owner")
	if rr := doRequest(f.router, "DELETE", "/api/v1/admin/grants/"+strconv.FormatUint(uint64(readerGrant), 10), root, ""); grantErrorCode(rr) != "two_factor_required" {
		t.Errorf("removing without 2FA = %d %s, want two_factor_required", rr.Code, rr.Body.String())
	}
	if rr := doRequest(f.router, "DELETE", "/api/v1/admin/grants/"+strconv.FormatUint(uint64(readerGrant), 10), owner, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("owner removing the grant = %d %s", rr.Code, rr.Body.String())
	}
	if rr := doRequest(f.router, "GET", aPath, tgt, ""); !refusedForAccess(rr) {
		t.Fatalf("a removed grant still reaches the domain: %d", rr.Code)
	}
}

// Every grant change is in the audit log, written in the change's own
// transaction: what rolls back leaves neither.
func TestGrants_EveryChangeIsAuditedAtomically(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "target"})
	owner := f.token(t, "owner")
	target := f.admins["target"].user.ID

	rr := f.grant(t, owner, target, "readonly", "domain:a.test")
	if rr.Code != http.StatusCreated {
		t.Fatalf("grant = %d %s", rr.Code, rr.Body.String())
	}
	id := grantID(t, rr)
	idStr := strconv.FormatUint(uint64(id), 10)
	if rr := doRequest(f.router, "DELETE", "/api/v1/admin/grants/"+idStr, owner, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("remove = %d %s", rr.Code, rr.Body.String())
	}
	for _, action := range []string{grants.ActionCreate, grants.ActionDelete} {
		var e models.AuditEntry
		err := f.db.Where("action = ? AND target->>'grant_id' = ?", action, idStr).First(&e).Error
		if err != nil {
			t.Fatalf("no %s entry for grant %s: %v", action, idStr, err)
		}
		if e.ActorKind != models.AuditActorAccount || e.ActorID == nil || *e.ActorID != f.admins["owner"].user.ID ||
			e.SessionID == "" || e.Target["scope"] != "domain:a.test" || e.Target["role"] != "readonly" || e.Outcome != models.AuditOutcomeSuccess {
			t.Errorf("%s entry = %+v", action, e)
		}
	}

	// A change whose transaction rolls back leaves no grant and no entry.
	var grantsBefore, entriesBefore int64
	f.db.Model(&models.Grant{}).Count(&grantsBefore)
	f.db.Model(&models.AuditEntry{}).Count(&entriesBefore)
	var readonly models.Role
	f.db.Where("name = ?", "readonly").First(&readonly)
	rollback := fmt.Errorf("roll back")
	err := f.db.Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		g := &models.Grant{HolderKind: models.GrantHolderAccount, HolderID: target, RoleID: readonly.ID, Scope: "*", Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem}
		if err := grants.Add(tx, g, models.AuditEntry{ActorKind: models.AuditActorSystem, ActorName: "system"}); err != nil {
			return err
		}
		return rollback
	})
	if err != rollback {
		t.Fatalf("transaction = %v", err)
	}
	var grantsAfter, entriesAfter int64
	f.db.Model(&models.Grant{}).Count(&grantsAfter)
	f.db.Model(&models.AuditEntry{}).Count(&entriesAfter)
	if grantsAfter != grantsBefore || entriesAfter != entriesBefore {
		t.Fatalf("a rolled-back change left grants %d→%d, entries %d→%d", grantsBefore, grantsAfter, entriesBefore, entriesAfter)
	}
	if res, err := audit.VerifyDB(f.db); err != nil || !res.OK {
		t.Fatalf("audit chain: %+v, %v", res, err)
	}
}
