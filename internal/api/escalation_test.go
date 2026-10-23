package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Escalation (#346), through the real router on a database of the test's own:
// an eligible grant gives nothing until its holder switches it on with a fresh
// code and a reason, works while it is on, stops when it is switched off or
// runs out, and every step is in the audit log.

type escalationGrant struct {
	ID         uint   `json:"id"`
	Role       string `json:"role"`
	Scope      string `json:"scope"`
	Mode       string `json:"mode"`
	Activation *struct {
		ID          uint   `json:"id"`
		ExpiresAt   string `json:"expires_at"`
		Reason      string `json:"reason"`
		SecondsLeft int64  `json:"seconds_left"`
	} `json:"activation"`
}

type escalationResult struct {
	Grant        escalationGrant `json:"grant"`
	Capabilities []string        `json:"capabilities"`
}

func decodeData(t *testing.T, rr *httptest.ResponseRecorder, into any) {
	t.Helper()
	env := struct {
		Data any `json:"data"`
	}{Data: into}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode %d %s: %v", rr.Code, rr.Body.String(), err)
	}
}

// code is a current code from name's authenticator.
func (f *grantsFixture) code(t *testing.T, name string) string {
	t.Helper()
	c, err := auth.GenerateTOTPCode(f.admins[name].secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *grantsFixture) mine(t *testing.T, token string) []escalationGrant {
	t.Helper()
	rr := doRequest(f.router, http.MethodGet, "/api/v1/admin/grants/mine", token, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("grants/mine = %d %s", rr.Code, rr.Body.String())
	}
	var out []escalationGrant
	decodeData(t, rr, &out)
	return out
}

// grantOf is the id of name's grant of role at "*".
func (f *grantsFixture) grantOf(t *testing.T, name, role string) uint {
	t.Helper()
	var g models.Grant
	err := f.db.Joins("JOIN roles r ON r.id = grants.role_id").
		Where("grants.holder_id = ? AND r.name = ? AND grants.scope = '*'", f.admins[name].user.ID, role).First(&g).Error
	if err != nil {
		t.Fatalf("%s's %s grant: %v", name, role, err)
	}
	return g.ID
}

// eligible gives name the role at "*", eligible, as the system.
func (f *grantsFixture) eligible(t *testing.T, name, role string) uint {
	t.Helper()
	var r models.Role
	if err := f.db.Where("name = ?", role).First(&r).Error; err != nil {
		t.Fatal(err)
	}
	var id uint
	err := f.db.Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		g := &models.Grant{HolderKind: models.GrantHolderAccount, HolderID: f.admins[name].user.ID, RoleID: r.ID,
			Scope: grants.All, Mode: models.GrantEligible, GrantedByKind: models.AuditActorSystem}
		err := grants.Add(tx, g, models.AuditEntry{ActorKind: models.AuditActorSystem, ActorName: "system"})
		id = g.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func escalationPath(id uint, step string) string {
	return "/api/v1/admin/grants/" + strconv.FormatUint(uint64(id), 10) + "/" + step
}

func (f *grantsFixture) activate(token string, id uint, body string) *httptest.ResponseRecorder {
	return doRequest(f.router, http.MethodPost, escalationPath(id, "activate"), token, body)
}

func activateBody(reason string, minutes int, code string) string {
	b, _ := json.Marshal(map[string]any{"reason": reason, "minutes": minutes, "totp_code": code})
	return string(b)
}

func (f *grantsFixture) count(t *testing.T, action string) int64 {
	t.Helper()
	var n int64
	if err := f.db.Model(&models.AuditEntry{}).Where("action = ?", action).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// What only "*" reaches among the routes: granting wants users:write, which
// the admin role does not hold.
const superadminOnlyRoute = "/api/v1/admin/grants"

func (f *grantsFixture) reachesSuperadminOnly(token string) bool {
	return !refusedForAccess(doRequest(f.router, http.MethodPost, superadminOnlyRoute, token, `{}`))
}

func TestEscalation_EligibleGrantGivesNothingUntilSwitchedOn(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "climber", roles: []string{"admin"}, twoFactor: true})
	super := f.eligible(t, "climber", "superadmin")

	s := f.admins["climber"].login(t, f.router)
	if contains(s.Capabilities, "*") || !contains(s.Capabilities, "domains:write") {
		t.Fatalf("an eligible superadmin is shown %v, want the admin role's alone", s.Capabilities)
	}
	tok := s.AccessToken
	if f.reachesSuperadminOnly(tok) {
		t.Fatal("an eligible superadmin grant gave users:write before it was switched on")
	}
	mine := f.mine(t, tok)
	if len(mine) != 2 || mine[0].Role != "admin" || mine[0].Mode != "active" || mine[1].Role != "superadmin" ||
		mine[1].Mode != "eligible" || mine[1].ID != super || mine[1].Activation != nil {
		t.Fatalf("grants/mine = %+v", mine)
	}

	// Refused: no code, a wrong code, no reason, too long, too short.
	for _, c := range []struct {
		body, status, code string
	}{
		{`{"reason":"rotate the DKIM keys"}`, "403", "totp_required"},
		{activateBody("rotate the DKIM keys", 60, "not-a-code"), "403", "invalid_totp"},
		{activateBody("  ", 60, f.code(t, "climber")), "422", "validation_failed"},
		{activateBody("rotate the DKIM keys", 241, f.code(t, "climber")), "422", "validation_failed"},
		{activateBody("rotate the DKIM keys", 0, f.code(t, "climber")), "422", "validation_failed"},
	} {
		rr := f.activate(tok, super, c.body)
		if strconv.Itoa(rr.Code) != c.status || grantErrorCode(rr) != c.code {
			t.Errorf("activate %s = %d %s, want %s %s", c.body, rr.Code, rr.Body.String(), c.status, c.code)
		}
	}
	var rows int64
	f.db.Model(&models.GrantActivation{}).Count(&rows)
	if rows != 0 || f.count(t, grants.ActionActivate) != 0 {
		t.Fatalf("refused activations left %d rows and %d entries", rows, f.count(t, grants.ActionActivate))
	}
	if f.reachesSuperadminOnly(tok) {
		t.Fatal("a refused activation switched the grant on")
	}

	// Switched on: it works at once, for this session and the next sign-in.
	rr := f.activate(tok, super, activateBody("rotate the DKIM keys", 30, f.code(t, "climber")))
	if rr.Code != http.StatusCreated {
		t.Fatalf("activate = %d %s", rr.Code, rr.Body.String())
	}
	var on escalationResult
	decodeData(t, rr, &on)
	if on.Grant.Activation == nil || on.Grant.Activation.Reason != "rotate the DKIM keys" ||
		on.Grant.Activation.SecondsLeft <= 29*60 || on.Grant.Activation.SecondsLeft > 30*60 ||
		!reflect.DeepEqual(on.Capabilities, []string{"*"}) {
		t.Fatalf("activate answered %+v", on)
	}
	if !f.reachesSuperadminOnly(tok) {
		t.Fatal("switched on, the grant still does not reach users:write")
	}
	if again := f.admins["climber"].login(t, f.router); !reflect.DeepEqual(again.Capabilities, []string{"*"}) {
		t.Errorf("signed in while switched on, shown %v, want [*]", again.Capabilities)
	}
	if mine := f.mine(t, tok); mine[1].Activation == nil || mine[1].Activation.ID != on.Grant.Activation.ID {
		t.Errorf("grants/mine does not show the activation: %+v", mine)
	}
	if rr := f.activate(tok, super, activateBody("again", 30, f.code(t, "climber"))); rr.Code != http.StatusConflict || grantErrorCode(rr) != "already_active" {
		t.Errorf("activate twice = %d %s, want 409 already_active", rr.Code, rr.Body.String())
	}

	// Switched off: it stops at once.
	rr = doRequest(f.router, http.MethodPost, escalationPath(super, "deactivate"), tok, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("deactivate = %d %s", rr.Code, rr.Body.String())
	}
	var off escalationResult
	decodeData(t, rr, &off)
	if off.Grant.Activation != nil || contains(off.Capabilities, "*") {
		t.Errorf("deactivate answered %+v", off)
	}
	if f.reachesSuperadminOnly(tok) {
		t.Fatal("switched off, the grant still reaches users:write")
	}
	if rr := doRequest(f.router, http.MethodPost, escalationPath(super, "deactivate"), tok, ""); rr.Code != http.StatusConflict || grantErrorCode(rr) != "not_active" {
		t.Errorf("deactivate twice = %d %s, want 409 not_active", rr.Code, rr.Body.String())
	}

	// Each step once in the audit log, with who, which grant, the reason, the
	// start and the end.
	for action, ended := range map[string]bool{grants.ActionActivate: false, grants.ActionDeactivate: true} {
		var e models.AuditEntry
		if err := f.db.Where("action = ?", action).First(&e).Error; err != nil {
			t.Fatalf("no %s entry: %v", action, err)
		}
		climber := f.admins["climber"].user.ID
		if e.ActorKind != models.AuditActorAccount || e.ActorID == nil || *e.ActorID != climber || e.SessionID == "" ||
			e.Target["grant_id"] != strconv.FormatUint(uint64(super), 10) || e.Target["role"] != "superadmin" ||
			e.Target["reason"] != "rotate the DKIM keys" || e.Target["started_at"] == "" || e.Target["ends_at"] == "" ||
			(e.Target["ended_at"] != "") != ended || e.Outcome != models.AuditOutcomeSuccess {
			t.Errorf("%s entry = %+v", action, e)
		}
	}
	if res, err := audit.VerifyDB(f.db); err != nil || !res.OK {
		t.Fatalf("audit chain: %+v, %v", res, err)
	}
}

func TestEscalation_StopsWhenItRunsOutAndTheExpiryIsRecorded(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "climber", roles: []string{"admin"}, twoFactor: true})
	super := f.eligible(t, "climber", "superadmin")
	climber := f.admins["climber"].user.ID
	tok := f.token(t, "climber")
	if rr := f.activate(tok, super, activateBody("restore a mailbox", 60, f.code(t, "climber"))); rr.Code != http.StatusCreated {
		t.Fatalf("activate = %d %s", rr.Code, rr.Body.String())
	}
	var a models.GrantActivation
	if err := f.db.Where("grant_id = ?", super).First(&a).Error; err != nil {
		t.Fatal(err)
	}

	// What the grants give, read just before and just after it runs out.
	before, err := grants.Load(f.db, climber, a.ExpiresAt.Add(-time.Second))
	if err != nil || !before.Allows("users:write", grants.All) {
		t.Fatalf("a second before it runs out: users:write=%v (%v)", before.Allows("users:write", grants.All), err)
	}
	after, err := grants.Load(f.db, climber, a.ExpiresAt)
	if err != nil || after.Allows("users:write", grants.All) || !after.Allows("domains:write", grants.All) {
		t.Fatalf("once it runs out: %v (%v), want the admin role's alone", after.Capabilities(), err)
	}

	// It ran out an hour ago. A router that has not read the grants since
	// (another replica, or this one once its cache entry ends) refuses.
	start, end := time.Now().Add(-2*time.Hour).Truncate(time.Second), time.Now().Add(-time.Hour).Truncate(time.Second)
	f.db.Model(&models.GrantActivation{}).Where("id = ?", a.ID).Updates(map[string]any{"activated_at": start, "expires_at": end})
	f.router = accessRouter(t, f.db)
	if f.reachesSuperadminOnly(tok) {
		t.Fatal("an activation that ran out still reaches users:write")
	}
	if mine := f.mine(t, tok); mine[1].Activation != nil {
		t.Errorf("grants/mine shows an activation that ran out: %+v", mine[1].Activation)
	}

	// grants/mine noticed it, and recorded it once, as the system.
	var e models.AuditEntry
	if err := f.db.Where("action = ?", grants.ActionExpire).First(&e).Error; err != nil {
		t.Fatalf("no %s entry: %v", grants.ActionExpire, err)
	}
	if e.ActorKind != models.AuditActorSystem || e.Target["grant_id"] != strconv.FormatUint(uint64(super), 10) ||
		e.Target["holder_id"] != strconv.FormatUint(uint64(climber), 10) || e.Target["reason"] != "restore a mailbox" ||
		e.Target["started_at"] != start.UTC().Format(time.RFC3339) || e.Target["ended_at"] != end.UTC().Format(time.RFC3339) {
		t.Errorf("expiry entry = %+v", e)
	}
	f.db.First(&a, a.ID)
	if a.EndedAt == nil || !a.EndedAt.Equal(end) {
		t.Errorf("ended_at = %v, want %v", a.EndedAt, end)
	}

	// The sweeper records the next one, once, however many replicas run it.
	if rr := f.activate(tok, super, activateBody("restore another", 60, f.code(t, "climber"))); rr.Code != http.StatusCreated {
		t.Fatalf("activate again = %d %s", rr.Code, rr.Body.String())
	}
	f.db.Model(&models.GrantActivation{}).Where("ended_at IS NULL").Update("expires_at", time.Now().Add(-time.Minute))
	for i, want := range []int{1, 0} {
		if n, err := grants.EndExpired(f.db, time.Now()); err != nil || n != want {
			t.Fatalf("sweep %d recorded %d (%v), want %d", i+1, n, err, want)
		}
	}
	if n := f.count(t, grants.ActionExpire); n != 2 {
		t.Fatalf("expiry entries = %d, want 2", n)
	}
	if res, err := audit.VerifyDB(f.db); err != nil || !res.OK {
		t.Fatalf("audit chain: %+v, %v", res, err)
	}
}

func TestEscalation_OnlyTheHolderSwitchesItOn(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "climber", roles: []string{"admin"}, twoFactor: true})
	super := f.eligible(t, "climber", "superadmin")
	rootReadonly := f.eligible(t, "root", "readonly")
	owner := f.token(t, "owner")

	// Someone else's grant, even a superadmin's asking: no such grant.
	if rr := f.activate(owner, super, activateBody("mine now", 60, f.code(t, "owner"))); rr.Code != http.StatusNotFound {
		t.Errorf("owner activating climber's grant = %d %s, want 404", rr.Code, rr.Body.String())
	}
	if rr := doRequest(f.router, http.MethodPost, escalationPath(super, "deactivate"), owner, ""); rr.Code != http.StatusNotFound {
		t.Errorf("owner deactivating climber's grant = %d, want 404", rr.Code)
	}
	if rr := f.activate(owner, 999999, activateBody("x", 60, f.code(t, "owner"))); rr.Code != http.StatusNotFound {
		t.Errorf("a grant that does not exist = %d, want 404", rr.Code)
	}
	// Binding refuses it before anything is looked up (#644): 422 naming the id.
	if rr := doRequest(f.router, http.MethodPost, "/api/v1/admin/grants/x/activate", owner, `{}`); rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"id":`) {
		t.Errorf("a grant id that is not one = %d %s, want 422 naming id", rr.Code, rr.Body.String())
	}

	// An active grant has nothing to switch on.
	climber := f.token(t, "climber")
	if rr := f.activate(climber, f.grantOf(t, "climber", "admin"), activateBody("x", 60, f.code(t, "climber"))); grantErrorCode(rr) != "not_eligible" {
		t.Errorf("activating an active grant = %d %s, want not_eligible", rr.Code, rr.Body.String())
	}

	// An admin without an authenticator cannot switch anything on.
	root := f.token(t, "root")
	if rr := f.activate(root, rootReadonly, activateBody("x", 60, "123456")); rr.Code != http.StatusForbidden || grantErrorCode(rr) != "two_factor_required" {
		t.Errorf("root without 2FA = %d %s, want 403 two_factor_required", rr.Code, rr.Body.String())
	}

	// An account holding no grant has none to list, and switches on no one
	// else's.
	pair, err := auth.NewJWTService("grants-access-secret", 15*time.Minute, 24*time.Hour).GenerateSession(f.ma.ID, f.ma.Address, 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rr := doRequest(f.router, http.MethodGet, "/api/v1/admin/grants/mine", pair.AccessToken, ""); rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != `{"data":[]}` {
		t.Errorf("grants/mine for an account holding none = %d %s", rr.Code, rr.Body.String())
	}
	if rr := doRequest(f.router, http.MethodPost, escalationPath(super, "activate"), pair.AccessToken, activateBody("not mine", 60, "123456")); rr.Code != http.StatusForbidden && rr.Code != http.StatusNotFound {
		t.Errorf("another's grant switched on by an account holding none = %d %s, want refused", rr.Code, rr.Body.String())
	}
}

func TestEscalation_ARecoveryCodeWorksOnceAndARefusalLeavesItUnused(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "climber", roles: []string{"admin"}, twoFactor: true})
	super := f.eligible(t, "climber", "superadmin")
	readonly := f.eligible(t, "climber", "readonly")
	codes := addRecoveryCodes(t, f.db, f.admins["climber"].user.ID)
	tok := f.token(t, "climber")
	withRecovery := func(reason, code string) string {
		b, _ := json.Marshal(map[string]any{"reason": reason, "recovery_code": code})
		return string(b)
	}

	if rr := f.activate(tok, super, withRecovery("lost my phone", codes[0])); rr.Code != http.StatusCreated {
		t.Fatalf("activate with a recovery code = %d %s", rr.Code, rr.Body.String())
	}
	if rr := f.activate(tok, readonly, withRecovery("again", codes[0])); grantErrorCode(rr) != "invalid_totp" {
		t.Errorf("the same recovery code again = %d %s, want invalid_totp", rr.Code, rr.Body.String())
	}
	// Refused after the code was checked: the code is still there to use.
	if rr := f.activate(tok, super, withRecovery("twice", codes[1])); grantErrorCode(rr) != "already_active" {
		t.Fatalf("activating twice = %d %s, want already_active", rr.Code, rr.Body.String())
	}
	if rr := f.activate(tok, readonly, withRecovery("now this one", codes[1])); rr.Code != http.StatusCreated {
		t.Errorf("a recovery code a refused activation used = %d %s, want it still good", rr.Code, rr.Body.String())
	}
}

// addRecoveryCodes gives adminID's authenticator a set of recovery codes,
// hashed as auth.HashRecoveryCode hashes them but at bcrypt.MinCost, as the
// handlers tests hash theirs: checking a code compares it with each unused one,
// slow at the real cost and many times slower under the race detector.
func addRecoveryCodes(t *testing.T, db *gorm.DB, adminID uint) []string {
	t.Helper()
	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.NewTwoFactorRepository(db).GetActive(adminID); err != nil {
		t.Fatal(err)
	}
	for i, c := range codes {
		hash, err := bcrypt.GenerateFromPassword([]byte(auth.NormalizeRecoveryCode(c)), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.Credential{AccountID: adminID, Kind: models.CredentialKindRecoveryCode, Name: "Recovery code",
			PublicID: fmt.Sprintf("rc%d-%d", adminID, i), SecretHash: "{BLF-CRYPT}" + string(hash),
			Scope: models.CredentialScope{MailboxID: adminID}}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return codes
}

// The owner of a server set up before escalation holds superadmin active, and
// keeps it until they choose otherwise: make-eligible, with a fresh code.
func TestEscalation_TheOwnerOptsIn(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "climber", roles: []string{"admin"}, twoFactor: true})
	owner := f.token(t, "owner")
	super := f.grantOf(t, "owner", "superadmin")
	path := escalationPath(super, "make-eligible")

	if rr := doRequest(f.router, http.MethodPost, path, owner, `{}`); grantErrorCode(rr) != "totp_required" {
		t.Fatalf("make-eligible without a code = %d %s, want totp_required", rr.Code, rr.Body.String())
	}
	climber := f.token(t, "climber")
	if rr := doRequest(f.router, http.MethodPost, path, climber, fmt.Sprintf(`{"totp_code":%q}`, f.code(t, "climber"))); rr.Code != http.StatusNotFound {
		t.Fatalf("someone else making the owner's grant eligible = %d, want 404", rr.Code)
	}
	if !f.reachesSuperadminOnly(owner) {
		t.Fatal("refused requests changed the owner's access")
	}

	rr := doRequest(f.router, http.MethodPost, path, owner, fmt.Sprintf(`{"totp_code":%q}`, f.code(t, "owner")))
	if rr.Code != http.StatusOK {
		t.Fatalf("make-eligible = %d %s", rr.Code, rr.Body.String())
	}
	var res escalationResult
	decodeData(t, rr, &res)
	if res.Grant.Mode != models.GrantEligible || len(res.Capabilities) != 0 {
		t.Fatalf("make-eligible answered %+v", res)
	}
	if f.reachesSuperadminOnly(owner) {
		t.Fatal("an eligible superadmin still reaches users:write")
	}
	var e models.AuditEntry
	if err := f.db.Where("action = ?", grants.ActionMakeEligible).First(&e).Error; err != nil ||
		e.ActorID == nil || *e.ActorID != f.admins["owner"].user.ID || e.Target["mode"] != models.GrantEligible || e.Target["role"] != "superadmin" {
		t.Fatalf("make-eligible entry = %+v (%v)", e, err)
	}
	if rr := doRequest(f.router, http.MethodPost, path, owner, fmt.Sprintf(`{"totp_code":%q}`, f.code(t, "owner"))); grantErrorCode(rr) != "already_eligible" {
		t.Errorf("make-eligible twice = %d %s, want already_eligible", rr.Code, rr.Body.String())
	}

	// And the owner switches it on whenever they need it.
	if rr := f.activate(owner, super, activateBody("add an admin", 60, f.code(t, "owner"))); rr.Code != http.StatusCreated {
		t.Fatalf("activate = %d %s", rr.Code, rr.Body.String())
	}
	if !f.reachesSuperadminOnly(owner) {
		t.Fatal("switched on, the owner's superadmin does not work")
	}
}

// An eligible superadmin counts as one that remains: the others can go, and
// the last one cannot. Removing a grant that is switched on ends its
// activation, recorded.
func TestEscalation_TheLastSuperadminCountsEligibleOnes(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "climber", roles: []string{"admin"}, twoFactor: true})
	owner := f.token(t, "owner")
	ownerSuper := f.grantOf(t, "owner", "superadmin")
	if rr := doRequest(f.router, http.MethodPost, escalationPath(ownerSuper, "make-eligible"), owner,
		fmt.Sprintf(`{"totp_code":%q}`, f.code(t, "owner"))); rr.Code != http.StatusOK {
		t.Fatalf("make-eligible = %d %s", rr.Code, rr.Body.String())
	}
	if rr := f.activate(owner, ownerSuper, activateBody("tidy up superadmins", 60, f.code(t, "owner"))); rr.Code != http.StatusCreated {
		t.Fatalf("activate = %d %s", rr.Code, rr.Body.String())
	}

	// climber's superadmin, switched on, then taken away by the owner.
	climberSuper := f.eligible(t, "climber", "superadmin")
	climber := f.token(t, "climber")
	if rr := f.activate(climber, climberSuper, activateBody("help the owner", 60, f.code(t, "climber"))); rr.Code != http.StatusCreated {
		t.Fatalf("climber activate = %d %s", rr.Code, rr.Body.String())
	}
	if rr := doRequest(f.router, http.MethodDelete, "/api/v1/admin/grants/"+strconv.FormatUint(uint64(climberSuper), 10), owner, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("removing climber's superadmin = %d %s", rr.Code, rr.Body.String())
	}
	if f.reachesSuperadminOnly(climber) {
		t.Fatal("a removed grant's activation still works")
	}
	var e models.AuditEntry
	if err := f.db.Where("action = ? AND target->>'grant_id' = ?", grants.ActionDeactivate, strconv.FormatUint(uint64(climberSuper), 10)).First(&e).Error; err != nil ||
		e.ActorID == nil || *e.ActorID != f.admins["owner"].user.ID || e.Target["reason"] != "help the owner" || e.Target["ended_at"] == "" {
		t.Fatalf("the activation's end with the grant = %+v (%v)", e, err)
	}
	var left int64
	f.db.Model(&models.GrantActivation{}).Where("grant_id = ?", climberSuper).Count(&left)
	if left != 0 {
		t.Errorf("a removed grant left %d activations", left)
	}

	// root's and fresh's active superadmins can go: the owner's eligible one
	// remains. The owner's own cannot.
	for _, name := range []string{"root", "fresh"} {
		path := "/api/v1/admin/grants/" + strconv.FormatUint(uint64(f.grantOf(t, name, "superadmin")), 10)
		if rr := doRequest(f.router, http.MethodDelete, path, owner, ""); rr.Code != http.StatusNoContent {
			t.Fatalf("taking superadmin from %s = %d %s", name, rr.Code, rr.Body.String())
		}
	}
	rr := doRequest(f.router, http.MethodDelete, "/api/v1/admin/grants/"+strconv.FormatUint(uint64(ownerSuper), 10), owner, "")
	if grantErrorCode(rr) != "last_superadmin" {
		t.Fatalf("removing the last superadmin, eligible = %d %s, want last_superadmin", rr.Code, rr.Body.String())
	}
	if !f.reachesSuperadminOnly(owner) {
		t.Fatal("the refused removal ended the owner's activation")
	}
	if res, err := audit.VerifyDB(f.db); err != nil || !res.OK {
		t.Fatalf("audit chain: %+v, %v", res, err)
	}
}

// An eligible superadmin counts as one that remains only if its holder has an
// authenticator to switch it on with.
func TestEscalation_AnEligibleSuperadminCountsOnlyWithAnAuthenticator(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "waiting"})
	f.eligible(t, "waiting", "superadmin")
	owner := f.token(t, "owner")
	for _, name := range []string{"root", "fresh"} {
		path := "/api/v1/admin/grants/" + strconv.FormatUint(uint64(f.grantOf(t, name, "superadmin")), 10)
		if rr := doRequest(f.router, http.MethodDelete, path, owner, ""); rr.Code != http.StatusNoContent {
			t.Fatalf("taking superadmin from %s = %d %s", name, rr.Code, rr.Body.String())
		}
	}
	ownerSuper := "/api/v1/admin/grants/" + strconv.FormatUint(uint64(f.grantOf(t, "owner", "superadmin")), 10)
	if rr := doRequest(f.router, http.MethodDelete, ownerSuper, owner, ""); grantErrorCode(rr) != "last_superadmin" {
		t.Fatalf("removing the last usable superadmin = %d %s, want last_superadmin: waiting has no authenticator", rr.Code, rr.Body.String())
	}
	enrollTOTP(t, f.db, f.admins["waiting"].user.ID)
	if rr := doRequest(f.router, http.MethodDelete, ownerSuper, owner, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("removing the owner's superadmin once waiting can switch its own on = %d %s", rr.Code, rr.Body.String())
	}
}

// A step whose transaction rolls back leaves neither an activation nor an
// entry.
func TestEscalation_ARolledBackActivationLeavesNothing(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "climber", roles: []string{"admin"}, twoFactor: true})
	super := f.eligible(t, "climber", "superadmin")
	var entries int64
	f.db.Model(&models.AuditEntry{}).Count(&entries)
	rollback := fmt.Errorf("roll back")
	err := f.db.Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		var g models.Grant
		if err := tx.First(&g, super).Error; err != nil {
			return err
		}
		if _, err := grants.Activate(tx, &g, time.Hour, "never", models.AuditEntry{ActorKind: models.AuditActorSystem, ActorName: "system"}); err != nil {
			return err
		}
		return rollback
	})
	if err != rollback {
		t.Fatalf("transaction = %v", err)
	}
	var rows, after int64
	f.db.Model(&models.GrantActivation{}).Count(&rows)
	f.db.Model(&models.AuditEntry{}).Count(&after)
	if rows != 0 || after != entries {
		t.Fatalf("a rolled-back activation left %d rows, entries %d→%d", rows, entries, after)
	}
}

// accessProbe is how one admin's sign-in and every route answered.
type accessProbe struct {
	signIn       string
	capabilities []string
	routes       []string
}

func probeAccess(t *testing.T, router http.Handler, a *seededAdmin, routes []accessRoute) accessProbe {
	t.Helper()
	s := a.login(t, router)
	p := accessProbe{signIn: fmt.Sprintf("%d %s %v", s.status, s.code, s.SetupRequired), capabilities: s.Capabilities}
	if s.status != http.StatusOK {
		return p
	}
	for _, rt := range routes {
		rr := doRequest(router, rt.method, rt.path, s.AccessToken, rt.body)
		p.routes = append(p.routes, fmt.Sprintf("%s %s %d %s", rt.method, rt.path, rr.Code, grantErrorCode(rr)))
	}
	return p
}

// TestEscalation_NobodyLosesAccess is the proof that starting the server
// changes nobody's access: each admin, signed in before and after a start,
// gets the same sign-in, is shown the same capabilities, and gets the same
// answer from every route, which is what its roles hold. A start changes no
// grant: an owner's active superadmin stays active.
func TestEscalation_NobodyLosesAccess(t *testing.T) {
	db := openAccessDB(t)
	domain, mailbox := seedDomain(t, db, "a.test")
	admins := seedAdmins(t, db, fixtureAdmins)
	held := map[string][]string{}
	for name, a := range admins {
		held[name] = roleCapabilities(t, db, a.user.ID)
	}
	type grantRow struct {
		ID, HolderID, RoleID uint
		HolderKind, Scope    string
		Mode                 string
		ExpiresAt            *time.Time
	}
	readGrants := func() []grantRow {
		var rows []grantRow
		if err := db.Model(&models.Grant{}).Order("id").Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		return rows
	}
	routes := append(accessRoutes(domain.ID, mailbox.ID), accessRoute{"GET", "/api/v1/admin/grants", "", "users:read"})

	// Before: as the last start left the database.
	grantsBefore := readGrants()
	before := map[string]accessProbe{}
	router := accessRouter(t, db)
	for _, a := range fixtureAdmins {
		before[a.name] = probeAccess(t, router, admins[a.name], routes)
	}

	// The server starts again: the bootstrap account (fresh, still in setup,
	// its password still deployed), and a sweep.
	if outcome, err := bootstrap.EnsureFirstAdmin(db, "fresh@access.test", accessPassword, true, time.Now()); err != nil || outcome != bootstrap.FirstAdminUnchanged {
		t.Fatalf("bootstrap admin on start: %v %v, want unchanged", outcome, err)
	}
	if _, err := grants.EndExpired(db, time.Now()); err != nil {
		t.Fatal(err)
	}
	if after := readGrants(); !reflect.DeepEqual(after, grantsBefore) {
		t.Fatalf("starting changed grants:\nbefore %+v\nafter  %+v", grantsBefore, after)
	}

	router = accessRouter(t, db)
	for _, a := range fixtureAdmins {
		a := admins[a.name]
		t.Run(a.name, func(t *testing.T) {
			after := probeAccess(t, router, a, routes)
			if after.signIn != before[a.name].signIn || !reflect.DeepEqual(after.capabilities, before[a.name].capabilities) {
				t.Errorf("sign-in %q %v, before %q %v", after.signIn, after.capabilities, before[a.name].signIn, before[a.name].capabilities)
			}
			if strings.Join(after.routes, "\n") != strings.Join(before[a.name].routes, "\n") {
				t.Errorf("routes answered\n%s\nbefore\n%s", strings.Join(after.routes, "\n"), strings.Join(before[a.name].routes, "\n"))
			}
			// And what the roles hold.
			if a.inactive || a.setup {
				return
			}
			if !displayEqual(held[a.name], after.capabilities) {
				t.Errorf("capabilities %v, its roles hold %v", after.capabilities, held[a.name])
			}
			s := a.login(t, router)
			for _, rt := range routes {
				rr := doRequest(router, rt.method, rt.path, s.AccessToken, rt.body)
				want := rolesAllow(held[a.name], rt.need)
				if reached := !refusedForAccess(rr); reached != want {
					t.Errorf("%s %s: reached=%v (%d), its roles say %v", rt.method, rt.path, reached, rr.Code, want)
				}
			}
		})
	}
	if res, err := audit.VerifyDB(db); err != nil || !res.OK {
		t.Fatalf("audit chain: %+v, %v", res, err)
	}
}
