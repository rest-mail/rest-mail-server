package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// serverCeilingFixture is a scratch database with the server ceiling
// initialised, a superadmin and an administrator who may manage accounts
// everywhere but is not superadmin.
type serverCeilingFixture struct {
	db                *gorm.DB
	handler           *GrantHandler
	superadmin, admin *auth.Claims
}

func newServerCeilingFixture(t *testing.T) serverCeilingFixture {
	t.Helper()
	db := openCustomFilterCreateScratchDB(t)
	if err := db.AutoMigrate(&models.Mailbox{}, &models.Credential{}, &models.Role{}, &models.Capability{}, &models.RoleCapability{}, &models.Grant{}, &models.GrantActivation{}, &models.AuditEntry{}); err != nil {
		t.Fatal(err)
	}
	create := func(v interface{}) {
		t.Helper()
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	d := models.Domain{Name: "example.org"}
	create(&d)
	root := models.Mailbox{DomainID: d.ID, LocalPart: "root", Address: "root@example.org", Active: true}
	admin := models.Mailbox{DomainID: d.ID, LocalPart: "admin", Address: "admin@example.org", Active: true}
	create(&root)
	create(&admin)
	caps := map[string]*models.Capability{}
	for _, name := range []string{grants.Wildcard, "admin:session", "mail:read", "mail:send", "mailbox:session", "users:read", "users:write"} {
		c := &models.Capability{Name: name}
		create(c)
		caps[name] = c
	}
	superRole := models.Role{Name: "superadmin"}
	adminRole := models.Role{Name: "admin"}
	create(&superRole)
	create(&adminRole)
	create(&models.RoleCapability{RoleID: superRole.ID, CapabilityID: caps[grants.Wildcard].ID})
	for _, name := range []string{"admin:session", "mail:read", "mail:send", "mailbox:session", "users:read", "users:write"} {
		create(&models.RoleCapability{RoleID: adminRole.ID, CapabilityID: caps[name].ID})
	}
	create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: root.ID, RoleID: superRole.ID, Scope: grants.All, Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()})
	create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: admin.ID, RoleID: adminRole.ID, Scope: grants.All, Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()})
	if err := grants.EnsureServerCeiling(db); err != nil {
		t.Fatal(err)
	}
	otp := []string{auth.AMRPassword, auth.AMROTP}
	return serverCeilingFixture{
		db:         db,
		handler:    NewGrantHandler(db, grants.NewCache(db, time.Minute), ""),
		superadmin: &auth.Claims{AccountID: root.ID, Address: root.Address, AMR: otp},
		admin:      &auth.Claims{AccountID: admin.ID, Address: admin.Address, AMR: otp},
	}
}

func (f serverCeilingFixture) do(t *testing.T, method, body string, claims *auth.Claims) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	grantRoutes(f.handler).ServeHTTP(rr, withClaims(httptest.NewRequest(method, "/api/v1/admin/server-capabilities", strings.NewReader(body)), claims))
	return rr
}

func (f serverCeilingFixture) stored(t *testing.T) []string {
	t.Helper()
	got, overridden, err := grants.ServerCeiling(f.db, time.Now())
	if err != nil || overridden {
		t.Fatalf("stored ceiling: overridden=%v err=%v", overridden, err)
	}
	return got
}

func (f serverCeilingFixture) audited(t *testing.T) []models.AuditEntry {
	t.Helper()
	var entries []models.AuditEntry
	if err := f.db.Where("action = ?", grants.ActionServerCapabilitiesUpdate).Order("seq").Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	return entries
}

func decodeServerCapabilities(t *testing.T, rr *httptest.ResponseRecorder) serverCapabilitiesResponse {
	t.Helper()
	var got struct {
		Data serverCapabilitiesResponse `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, rr.Body.String())
	}
	return got.Data
}

func ceilingErrorCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body respond.ValidationErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, rr.Body.String())
	}
	return body.Error.Code
}

func TestServerCapabilitiesSuperadminUpdateIsAppliedAndAudited(t *testing.T) {
	f := newServerCeilingFixture(t)
	rr := f.do(t, http.MethodGet, "", f.superadmin)
	if rr.Code != http.StatusOK {
		t.Fatalf("read: %d %s", rr.Code, rr.Body.String())
	}
	if got := decodeServerCapabilities(t, rr); got.Source != serverCapabilitiesFromDatabase || !reflect.DeepEqual(got.Capabilities, []string{"admin:session", "mail:read", "mail:send", "mailbox:session", "users:read", "users:write"}) {
		t.Fatalf("initial ceiling = %+v", got)
	}

	rr = f.do(t, http.MethodPut, `{"capabilities":["users:write","mail:read","mail:read","mailbox:session","admin:session"]}`, f.superadmin)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rr.Code, rr.Body.String())
	}
	want := []string{"admin:session", "mail:read", "mailbox:session", "users:write"}
	if got := decodeServerCapabilities(t, rr); !reflect.DeepEqual(got.Capabilities, want) {
		t.Fatalf("response = %+v, want %v", got, want)
	}
	if got := f.stored(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("stored = %v, want %v", got, want)
	}
	// The administrator is capped by the new ceiling at once.
	held, err := grants.Load(f.db, f.admin.AccountID, time.Now())
	if err != nil || held.Allows("mail:send", grants.All) || !held.Allows("mail:read", grants.All) {
		t.Fatalf("administrator not capped by the new ceiling: %v", err)
	}

	entries := f.audited(t)
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.ActorID == nil || *e.ActorID != f.superadmin.AccountID || e.Outcome != models.AuditOutcomeSuccess {
		t.Fatalf("audit actor/outcome = %+v", e)
	}
	for k, v := range map[string]string{"before": "admin:session,mail:read,mail:send,mailbox:session,users:read,users:write", "after": "admin:session,mail:read,mailbox:session,users:write", "added": "", "removed": "mail:send,users:read"} {
		if e.Target[k] != v {
			t.Errorf("audit target %s = %q, want %q", k, e.Target[k], v)
		}
	}

	// An empty set is allowed: it denies every ordinary capability.
	if rr := f.do(t, http.MethodPut, `{"capabilities":[]}`, f.superadmin); rr.Code != http.StatusOK {
		t.Fatalf("empty update: %d %s", rr.Code, rr.Body.String())
	}
	if got := f.stored(t); len(got) != 0 {
		t.Fatalf("stored after empty update = %v", got)
	}
}

func TestServerCapabilitiesRefusesNonSuperadmin(t *testing.T) {
	f := newServerCeilingFixture(t)
	before := f.stored(t)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		rr := f.do(t, method, `{"capabilities":["mail:read"]}`, f.admin)
		if rr.Code != http.StatusForbidden || ceilingErrorCode(t, rr) != "superadmin_required" {
			t.Fatalf("%s: %d %s", method, rr.Code, rr.Body.String())
		}
	}
	if got := f.stored(t); !reflect.DeepEqual(got, before) {
		t.Fatalf("refused update changed the ceiling: %v", got)
	}
	if n := len(f.audited(t)); n != 0 {
		t.Fatalf("refused update audited %d changes", n)
	}
}

func TestServerCapabilitiesRefusesUnknownAndWildcard(t *testing.T) {
	f := newServerCeilingFixture(t)
	before := f.stored(t)
	for _, body := range []string{`{"capabilities":["mail:reed"]}`, `{"capabilities":["*"]}`, `{"capabilities":["mail:read",""]}`} {
		rr := f.do(t, http.MethodPut, body, f.superadmin)
		if rr.Code != http.StatusUnprocessableEntity || ceilingErrorCode(t, rr) != "validation_failed" {
			t.Fatalf("%s: %d %s", body, rr.Code, rr.Body.String())
		}
	}
	if got := f.stored(t); !reflect.DeepEqual(got, before) {
		t.Fatalf("refused update changed the ceiling: %v", got)
	}
	if n := len(f.audited(t)); n != 0 {
		t.Fatalf("refused update audited %d changes", n)
	}
}

func TestServerCapabilitiesEnvironmentOverrideRefusesUpdate(t *testing.T) {
	f := newServerCeilingFixture(t)
	before := f.stored(t)
	if err := grants.ConfigureServerOverride(f.db, "mail:read", true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = grants.ConfigureServerOverride(nil, "", false) })

	rr := f.do(t, http.MethodGet, "", f.superadmin)
	if got := decodeServerCapabilities(t, rr); rr.Code != http.StatusOK || got.Source != serverCapabilitiesFromEnvironment || !reflect.DeepEqual(got.Capabilities, []string{"mail:read"}) {
		t.Fatalf("read under override: %d %s", rr.Code, rr.Body.String())
	}
	rr = f.do(t, http.MethodPut, `{"capabilities":["mail:send"]}`, f.superadmin)
	if rr.Code != http.StatusConflict || ceilingErrorCode(t, rr) != "server_capabilities_from_environment" {
		t.Fatalf("update under override: %d %s", rr.Code, rr.Body.String())
	}
	if err := grants.ConfigureServerOverride(nil, "", false); err != nil {
		t.Fatal(err)
	}
	if got := f.stored(t); !reflect.DeepEqual(got, before) {
		t.Fatalf("refused update changed the stored ceiling: %v", got)
	}
	if n := len(f.audited(t)); n != 0 {
		t.Fatalf("refused update audited %d changes", n)
	}
}

// With no database configured, reaching storage would panic: these refusals
// come from the registered route's checks and binding, before a transaction.
func TestRegisteredServerCapabilitiesValidation(t *testing.T) {
	otp := &auth.Claims{AccountID: 1, AMR: []string{auth.AMRPassword, auth.AMROTP}}
	password := &auth.Claims{AccountID: 1, AMR: []string{auth.AMRPassword}}
	for _, tc := range []struct {
		name, body  string
		claims      *auth.Claims
		status      int
		code, field string
	}{
		{name: "two-factor", body: `{"capabilities":[]}`, claims: password, status: 403, code: "two_factor_required"},
		{name: "malformed body", body: "broken", claims: otp, status: 400, code: "bad_request"},
		{name: "missing capabilities", body: `{}`, claims: otp, status: 422, code: "validation_failed", field: "capabilities"},
		{name: "unknown field", body: `{"capabilities":[],"extra":1}`, claims: otp, status: 422, code: "validation_failed", field: "extra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			grantRoutes(NewGrantHandler(nil, nil, "")).ServeHTTP(rr, withClaims(httptest.NewRequest(http.MethodPut, "/api/v1/admin/server-capabilities", strings.NewReader(tc.body)), tc.claims))
			var body respond.ValidationErrorResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if rr.Code != tc.status || body.Error.Code != tc.code {
				t.Fatalf("got %d %s", rr.Code, rr.Body.String())
			}
			if tc.field != "" && body.Error.Details.Fields[tc.field] == "" {
				t.Fatalf("no message for field %s: %s", tc.field, rr.Body.String())
			}
		})
	}
}
