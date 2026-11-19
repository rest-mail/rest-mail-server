package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// A locked domain (#2012, decided on #1870; made absolute on #2098): an API key
// may lock it; nobody may delete it while it is locked, a person with a fresh
// second factor included; an API key or a person's session without a fresh
// second factor cannot lift the lock; a person with one can, and may then
// delete the domain. Every case goes through the production router to real
// PostgreSQL, and what is stored is read back, not taken from the answer.
func TestDomainAutomationLock(t *testing.T) {
	fixture := newGrantsFixture(t)
	owner := fixture.admins["owner"].user
	ownerSession := fixture.token(t, "owner")
	passwordOnly := fixture.token(t, "root")
	stale := staleTwoFactorSession(t, ownerSession)

	_, key, err := repositories.NewCredentialRepository(fixture.db).CreateAPIKey(owner.ID, owner.ID, "Domain robot",
		[]auth.ScopedPermission{
			{Capability: middleware.CapDomainsWrite, Scope: "*"},
			{Capability: middleware.CapDomainsDelete, Scope: "*"},
		}, nil)
	if err != nil {
		t.Fatal(err)
	}

	newDomain := func(t *testing.T, name string) models.Domain {
		t.Helper()
		d := models.Domain{Name: name, ServerType: "restmail"}
		if err := fixture.db.Create(&d).Error; err != nil {
			t.Fatal(err)
		}
		return d
	}
	path := func(d models.Domain) string { return fmt.Sprintf("/api/v1/admin/domains/%d", d.ID) }
	stored := func(t *testing.T, d models.Domain) models.Domain {
		t.Helper()
		var row models.Domain
		if err := fixture.db.First(&row, d.ID).Error; err != nil {
			t.Fatalf("domain %s: %v", d.Name, err)
		}
		return row
	}
	retired := func(t *testing.T, d models.Domain) bool {
		t.Helper()
		var n int64
		if err := fixture.db.Model(&models.DomainRetirement{}).Where("domain_id = ?", d.ID).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	audited := func(t *testing.T, action string, d models.Domain) int64 {
		t.Helper()
		var n int64
		if err := fixture.db.Model(&models.AuditEntry{}).Where("action = ? AND target->>'domain_id' = ?", action, fmt.Sprint(d.ID)).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	refused := func(t *testing.T, rr *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if rr.Code != status || grantErrorCode(rr) != code {
			t.Fatalf("got %d %s, want %d %s", rr.Code, rr.Body, status, code)
		}
	}
	lock := func(t *testing.T, token string, d models.Domain) map[string]any {
		t.Helper()
		rr := doRequest(fixture.router, http.MethodPatch, path(d), token, `{"automation_locked":true}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("lock: %d %s", rr.Code, rr.Body)
		}
		var env struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		return env.Data
	}

	t.Run("an API key locks a domain, and the answer and the row say who and when", func(t *testing.T) {
		d := newDomain(t, "lock-by-key.test")
		before := time.Now().Add(-time.Second)
		data := lock(t, key, d)
		held, ok := data["automation_lock"].(map[string]any)
		if !ok || held["locked_by"] != owner.Address {
			t.Fatalf("answer has no automation lock by %s: %v", owner.Address, data)
		}
		row := stored(t, d)
		if row.AutomationLockedAt == nil || row.AutomationLockedAt.Before(before) || row.AutomationLockedBy != owner.Address {
			t.Fatalf("stored lock %v by %q", row.AutomationLockedAt, row.AutomationLockedBy)
		}
		if n := audited(t, "domain.automation_lock", d); n != 1 {
			t.Fatalf("%d lock audit entries, want 1", n)
		}

		// Locking it again keeps the first lock and writes nothing.
		first := *row.AutomationLockedAt
		lock(t, ownerSession, d)
		if again := stored(t, d); !again.AutomationLockedAt.Equal(first) || again.AutomationLockedBy != owner.Address {
			t.Fatalf("relocking changed the lock: %v by %q", again.AutomationLockedAt, again.AutomationLockedBy)
		}
		if n := audited(t, "domain.automation_lock", d); n != 1 {
			t.Fatalf("%d lock audit entries after relocking, want 1", n)
		}
	})

	t.Run("nobody can delete a locked domain, a person with a fresh second factor included", func(t *testing.T) {
		d := newDomain(t, "locked-delete.test")
		lock(t, ownerSession, d)
		for _, tc := range []struct{ who, token string }{
			{"API key", key},
			{"password-only session", passwordOnly},
			{"stale two-factor session", stale},
			{"fresh two-factor session", ownerSession},
		} {
			refused(t, doRequest(fixture.router, http.MethodDelete, path(d), tc.token, ""), http.StatusForbidden, "automation_locked")
			if retired(t, d) || stored(t, d).AutomationLockedAt == nil {
				t.Fatalf("%s: a refused delete changed the domain", tc.who)
			}
		}
	})

	t.Run("a person lifts the lock and then deletes the domain", func(t *testing.T) {
		d := newDomain(t, "locked-lift-then-delete.test")
		lock(t, key, d)
		refused(t, doRequest(fixture.router, http.MethodDelete, path(d), ownerSession, ""), http.StatusForbidden, "automation_locked")
		if rr := doRequest(fixture.router, http.MethodPatch, path(d), ownerSession, `{"automation_locked":false}`); rr.Code != http.StatusOK {
			t.Fatalf("lift: %d %s", rr.Code, rr.Body)
		}
		if rr := doRequest(fixture.router, http.MethodDelete, path(d), ownerSession, ""); rr.Code != http.StatusNoContent {
			t.Fatalf("person's delete after lifting the lock: %d %s", rr.Code, rr.Body)
		}
		if !retired(t, d) {
			t.Fatal("deleted domain was not retired")
		}
	})

	t.Run("nothing but a fresh second factor lifts the lock", func(t *testing.T) {
		d := newDomain(t, "locked-lift.test")
		lock(t, ownerSession, d)
		for _, tc := range []struct{ who, token, code string }{
			{"API key", key, "two_factor_required"},
			{"password-only session", passwordOnly, "two_factor_required"},
			{"stale two-factor session", stale, "recent_two_factor_required"},
		} {
			// The lift is refused whole: a change sent with it is not made either.
			rr := doRequest(fixture.router, http.MethodPatch, path(d), tc.token, `{"automation_locked":false,"sending_policy":"bulk"}`)
			if rr.Code != http.StatusForbidden || grantErrorCode(rr) != tc.code {
				t.Fatalf("%s: got %d %s, want 403 %s", tc.who, rr.Code, rr.Body, tc.code)
			}
			if row := stored(t, d); row.AutomationLockedAt == nil || row.SendingPolicy != "" {
				t.Fatalf("%s: refused lift left lock %v, sending policy %q", tc.who, row.AutomationLockedAt, row.SendingPolicy)
			}
		}
		if n := audited(t, "domain.automation_unlock", d); n != 0 {
			t.Fatalf("%d unlock audit entries after refusals, want 0", n)
		}

		rr := doRequest(fixture.router, http.MethodPatch, path(d), ownerSession, `{"automation_locked":false}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("fresh two-factor lift: %d %s", rr.Code, rr.Body)
		}
		var env struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if _, present := env.Data["automation_lock"]; present {
			t.Fatalf("lifted lock still answered: %v", env.Data)
		}
		if row := stored(t, d); row.AutomationLockedAt != nil || row.AutomationLockedBy != "" {
			t.Fatalf("lifted lock still stored: %v by %q", row.AutomationLockedAt, row.AutomationLockedBy)
		}
		if n := audited(t, "domain.automation_unlock", d); n != 1 {
			t.Fatalf("%d unlock audit entries, want 1", n)
		}

		// Control: once lifted, the same key deletes the domain.
		if rr := doRequest(fixture.router, http.MethodDelete, path(d), key, ""); rr.Code != http.StatusNoContent {
			t.Fatalf("key delete after unlock: %d %s", rr.Code, rr.Body)
		}
		if !retired(t, d) {
			t.Fatal("deleted domain was not retired")
		}
	})

	t.Run("an API key asking to unlock an unlocked domain changes nothing", func(t *testing.T) {
		d := newDomain(t, "never-locked.test")
		if rr := doRequest(fixture.router, http.MethodPatch, path(d), key, `{"automation_locked":false}`); rr.Code != http.StatusOK {
			t.Fatalf("got %d %s", rr.Code, rr.Body)
		}
		if n := audited(t, "domain.automation_unlock", d); n != 0 {
			t.Fatalf("%d unlock audit entries, want 0", n)
		}
	})

	t.Run("restoring a domain brings back the lock it was archived with", func(t *testing.T) {
		// A locked domain can no longer be deleted, so the archive is given the
		// lock directly: what is proved is that restore keeps the column.
		d := newDomain(t, "restored-lock.test")
		if rr := doRequest(fixture.router, http.MethodDelete, path(d), ownerSession, ""); rr.Code != http.StatusNoContent {
			t.Fatalf("delete: %d %s", rr.Code, rr.Body)
		}
		if err := fixture.db.Exec(`UPDATE domain_retirements SET state = jsonb_set(jsonb_set(state,
			'{domains,0,automation_locked_at}', to_jsonb(now())), '{domains,0,automation_locked_by}', to_jsonb(?::text))
			WHERE domain_id = ?`, owner.Address, d.ID).Error; err != nil {
			t.Fatal(err)
		}
		if rr := doRequest(fixture.router, http.MethodPost, path(d)+"/restore", ownerSession, ""); rr.Code != http.StatusOK {
			t.Fatalf("restore: %d %s", rr.Code, rr.Body)
		}
		if row := stored(t, d); row.AutomationLockedAt == nil || row.AutomationLockedBy != owner.Address {
			t.Fatalf("restored domain lost its lock: %v by %q", row.AutomationLockedAt, row.AutomationLockedBy)
		}
		refused(t, doRequest(fixture.router, http.MethodDelete, path(d), ownerSession, ""), http.StatusForbidden, "automation_locked")
	})
}

// staleTwoFactorSession is the session token was signed in with, refreshed after
// its second factor has aged out of the fresh window: the same session, still
// two-factor, whose proof is an hour old.
func staleTwoFactorSession(t *testing.T, token string) string {
	t.Helper()
	jwt := auth.NewJWTService("grants-access-secret", 15*time.Minute, 24*time.Hour)
	claims, err := jwt.ValidateToken(token)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := jwt.GenerateSession(claims.AccountID, claims.Address, claims.CredentialEpoch, claims.AMR, false,
		auth.ContinueSession(claims.SessionID), auth.ContinueAuthentication(jwtlib.NewNumericDate(time.Now().Add(-time.Hour))))
	if err != nil {
		t.Fatal(err)
	}
	return pair.AccessToken
}

// The named pairs of the lock, sent as written through the production router to
// real PostgreSQL, each answer compared byte for byte in meaning with its pair.
// The database clock is fixed, so created_at, updated_at and locked_at are the
// pair's own.
func TestDomainAutomationLockExamples(t *testing.T) {
	fixture := newGrantsFixture(t)
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fixture.db.NowFunc = func() time.Time { return stamp }
	owner := fixture.admins["owner"].user
	ownerSession := fixture.token(t, "owner")
	_, key, err := repositories.NewCredentialRepository(fixture.db).CreateAPIKey(owner.ID, owner.ID, "Domain robot",
		[]auth.ScopedPermission{
			{Capability: middleware.CapDomainsWrite, Scope: "*"},
			{Capability: middleware.CapDomainsDelete, Scope: "*"},
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&models.Domain{ID: 65010, Name: "locked.test", ServerType: "restmail"}).Error; err != nil {
		t.Fatal(err)
	}

	tokens := map[string]string{"key": key, "stale": staleTwoFactorSession(t, ownerSession), "person": ownerSession}
	for _, step := range automationLockSteps {
		t.Run(step.op+"/"+step.name, func(t *testing.T) {
			examplepairs.Proven(t, step.op, step.name)
			pair, ok := pairs[step.op][step.name]
			if !ok {
				t.Fatalf("no pair %s of %s", step.name, step.op)
			}
			body := ""
			if pair.Request.Body != nil {
				encoded, err := json.Marshal(pair.Request.Body)
				if err != nil {
					t.Fatal(err)
				}
				body = string(encoded)
			}
			rr := doRequest(fixture.router, step.method, fmt.Sprintf("/api/v1/admin/domains/%v", pair.Request.Path["id"]), tokens[step.sender], body)
			if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("got %d %q %s, want %d %q", rr.Code, rr.Header().Get("Content-Type"), rr.Body, pair.Response.Status, pair.Response.MediaType)
			}
			actual := rr.Body.Bytes()
			switch {
			case rr.Code == http.StatusNoContent:
				if len(actual) != 0 || len(pair.Response.Body) != 0 {
					t.Fatalf("204 with body %q, pair body %s", actual, pair.Response.Body)
				}
			case rr.Code == http.StatusOK:
				assertPublicExampleBody(t, pair.Response.Body, atStamp(t, actual, stamp))
			default:
				assertPublicExampleBody(t, pair.Response.Body, actual)
			}
			var retirements int64
			if err := fixture.db.Model(&models.DomainRetirement{}).Where("domain_id = ?", 65010).Count(&retirements).Error; err != nil {
				t.Fatal(err)
			}
			if step.deletes {
				if retirements != 1 {
					t.Fatalf("after %s the domain is not archived", step.name)
				}
				return
			}
			var row models.Domain
			if err := fixture.db.First(&row, 65010).Error; err != nil || retirements != 0 {
				t.Fatalf("the domain is gone (%d archived): %v", retirements, err)
			}
			if wantLocked := step.name != "automation_unlock"; (row.AutomationLockedAt != nil) != wantLocked {
				t.Fatalf("after %s the stored lock is %v, want locked=%v", step.name, row.AutomationLockedAt, wantLocked)
			}
		})
	}
}

// automationLockSteps are the lock's named pairs, in the order
// TestDomainAutomationLockExamples sends them: each step leaves the domain as
// the next pair needs it. The fake-driver harnesses of TestDomainMutationExamples
// and TestDomainRemovalExamples prove every other pair of these operations and
// leave these to it: they need real row locks, the audit chain and a stored lock.
var automationLockSteps = []struct {
	op, name, method, sender string
	deletes                  bool
}{
	{"updateDomain", "automation_lock", http.MethodPatch, "key", false},
	{"deleteDomain", "automation_locked", http.MethodDelete, "key", false},
	{"deleteDomain", "automation_locked_person", http.MethodDelete, "person", false},
	{"updateDomain", "automation_unlock_key", http.MethodPatch, "key", false},
	{"updateDomain", "automation_unlock_stale", http.MethodPatch, "stale", false},
	{"updateDomain", "automation_unlock", http.MethodPatch, "person", false},
	{"deleteDomain", "automation_unlocked", http.MethodDelete, "person", true},
}

// provenByAutomationLockExamples reports whether TestDomainAutomationLockExamples
// sends the pair name of op.
func provenByAutomationLockExamples(op, name string) bool {
	for _, step := range automationLockSteps {
		if step.op == op && step.name == name {
			return true
		}
	}
	return false
}

// atStamp checks every time a domain answer carries is exactly stamp, the fixed
// database clock, and writes each in UTC, as the pair does: the server writes
// them in the database connection's zone.
func atStamp(t *testing.T, body []byte, stamp time.Time) []byte {
	t.Helper()
	var answer map[string]any
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatal(err)
	}
	data, ok := answer["data"].(map[string]any)
	if !ok {
		t.Fatalf("no domain in %s", body)
	}
	fields := []struct {
		object map[string]any
		name   string
	}{{data, "created_at"}, {data, "updated_at"}}
	if held, ok := data["automation_lock"].(map[string]any); ok {
		fields = append(fields, struct {
			object map[string]any
			name   string
		}{held, "locked_at"})
	}
	for _, field := range fields {
		text, _ := field.object[field.name].(string)
		at, err := time.Parse(time.RFC3339Nano, text)
		if err != nil || !at.Equal(stamp) {
			t.Fatalf("%s %q, want exactly %s: %v", field.name, text, stamp.Format(time.RFC3339), err)
		}
		field.object[field.name] = at.UTC().Format(time.RFC3339Nano)
	}
	out, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
