package api

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/examplepairs"
	"github.com/restmail/restmail/internal/grants"
)

// The server capability ceiling pairs run the real router on a scratch database
// holding a superadmin and an administrator who is not superadmin, both signed in
// with a second factor, and check what the request changed as well as the answer.
// They skip, and prove nothing, when no database is reachable.

const serverCapabilitiesPath = "/api/v1/admin/server-capabilities"

type serverCapabilitiesExampleFixture struct {
	*credentialFixture
	superadmin, admin string // each one's Authorization header
}

func newServerCapabilitiesExampleFixture(t *testing.T) serverCapabilitiesExampleFixture {
	t.Helper()
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test"})
	var domain models.Domain
	if err := f.db.First(&domain, "name = ?", "example.test").Error; err != nil {
		t.Fatal(err)
	}
	return serverCapabilitiesExampleFixture{
		credentialFixture: f,
		superadmin:        serverCapabilitiesHolder(f, domain.ID, "root", "superadmin", grants.Wildcard),
		admin:             serverCapabilitiesHolder(f, domain.ID, "admin", "admin", middleware.CapUsersRead, middleware.CapUsersWrite),
	}
}

// serverCapabilitiesHolder seeds a mailbox holding role at "*" in the database,
// gives the route checks the same capabilities (they read the test grants), and
// signs it in with a password and a second factor.
func serverCapabilitiesHolder(f *credentialFixture, domainID uint, local, role string, caps ...string) string {
	f.t.Helper()
	id := f.seedMailbox(domainID, local)
	var r models.Role
	if err := f.db.Where("name = ?", role).First(&r).Error; err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: id, RoleID: r.ID, Scope: grants.All,
		Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		f.t.Fatal(err)
	}
	entries := make([]grants.Entry, len(caps))
	for i, c := range caps {
		entries[i] = grants.Entry{Scope: grants.All, Capability: c}
	}
	testGrants.Lock()
	previous, existed := testGrants.held[id]
	testGrants.held[id] = grants.HeldOf(entries...)
	testGrants.Unlock()
	f.t.Cleanup(func() {
		testGrants.Lock()
		defer testGrants.Unlock()
		if existed {
			testGrants.held[id] = previous
		} else {
			delete(testGrants.held, id)
		}
	})
	session, err := f.jwt.GenerateSession(id, local+"@example.test", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		f.t.Fatal(err)
	}
	return "Bearer " + session.AccessToken
}

// stored is the ceiling the database holds, the override aside.
func (f serverCapabilitiesExampleFixture) stored() []string {
	f.t.Helper()
	names, overridden, err := grants.ServerCeiling(f.db, time.Now())
	if err != nil || overridden {
		f.t.Fatalf("stored ceiling: overridden=%v err=%v", overridden, err)
	}
	return names
}

func TestServerCapabilitiesExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("getServerCapabilities/not_superadmin", func(t *testing.T) {
		examplepairs.Proven(t, "getServerCapabilities", "not_superadmin")
		pair := pairs["getServerCapabilities"]["not_superadmin"]
		f := newServerCapabilitiesExampleFixture(t)
		rr := f.do(http.MethodGet, serverCapabilitiesPath, f.admin, string(pair.Request.Body)) // without_capability: not superadmin
		assertRequirementResponse(t, pair, rr)
	})

	refused := func(t *testing.T, f serverCapabilitiesExampleFixture, before []string) {
		t.Helper()
		if got := f.stored(); !reflect.DeepEqual(got, before) {
			t.Fatalf("refused update changed the stored ceiling from %v to %v", before, got)
		}
		if n := len(f.entries(grants.ActionServerCapabilitiesUpdate)); n != 0 {
			t.Fatalf("refused update audited %d changes", n)
		}
	}

	t.Run("updateServerCapabilities/not_superadmin", func(t *testing.T) {
		examplepairs.Proven(t, "updateServerCapabilities", "not_superadmin")
		pair := pairs["updateServerCapabilities"]["not_superadmin"]
		f := newServerCapabilitiesExampleFixture(t)
		before := f.stored()
		rr := f.do(http.MethodPut, serverCapabilitiesPath, f.admin, string(pair.Request.Body)) // without_capability: not superadmin
		assertRequirementResponse(t, pair, rr)
		refused(t, f, before)
	})

	t.Run("updateServerCapabilities/replaced", func(t *testing.T) {
		examplepairs.Proven(t, "updateServerCapabilities", "replaced")
		pair := pairs["updateServerCapabilities"]["replaced"]
		f := newServerCapabilitiesExampleFixture(t)
		rr := f.do(http.MethodPut, serverCapabilitiesPath, f.superadmin, string(pair.Request.Body)) // two_factor_session
		assertRequirementResponse(t, pair, rr)
		if got, want := f.stored(), []string{"mail:read", "mail:send"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("stored ceiling = %v, want %v", got, want)
		}
		if n := len(f.entries(grants.ActionServerCapabilitiesUpdate)); n != 1 {
			t.Fatalf("%d changes audited, want 1", n)
		}
	})

	t.Run("updateServerCapabilities/unknown_capability", func(t *testing.T) {
		examplepairs.Proven(t, "updateServerCapabilities", "unknown_capability")
		pair := pairs["updateServerCapabilities"]["unknown_capability"]
		f := newServerCapabilitiesExampleFixture(t)
		before := f.stored()
		rr := f.do(http.MethodPut, serverCapabilitiesPath, f.superadmin, string(pair.Request.Body)) // two_factor_session
		assertRequirementResponse(t, pair, rr)
		refused(t, f, before)
	})

	t.Run("updateServerCapabilities/from_environment", func(t *testing.T) {
		examplepairs.Proven(t, "updateServerCapabilities", "from_environment")
		pair := pairs["updateServerCapabilities"]["from_environment"]
		f := newServerCapabilitiesExampleFixture(t)
		before := f.stored()
		// RESTMAIL_SERVER_CAPABILITIES is set.
		if err := grants.ConfigureServerOverride(f.db, "mail:read", true); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = grants.ConfigureServerOverride(nil, "", false) })
		rr := f.do(http.MethodPut, serverCapabilitiesPath, f.superadmin, string(pair.Request.Body)) // two_factor_session
		assertRequirementResponse(t, pair, rr)
		if err := grants.ConfigureServerOverride(nil, "", false); err != nil {
			t.Fatal(err)
		}
		refused(t, f, before)
	})
}
