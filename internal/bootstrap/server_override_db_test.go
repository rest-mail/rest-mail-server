package bootstrap

import (
	"os"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// configureServerEnv sets RESTMAIL_SERVER_CAPABILITIES the way cmd/api reads
// it (present or absent) and applies it against the scratch database. The
// process-wide override and the environment are restored when the test ends,
// so these tests must not run in parallel.
func configureServerEnv(t *testing.T, db *gorm.DB, raw string, present bool) error {
	t.Helper()
	t.Setenv(grants.ServerCapabilitiesEnv, raw)
	if !present {
		if err := os.Unsetenv(grants.ServerCapabilitiesEnv); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = grants.ConfigureServerOverride(nil, "", false) })
	value, set := os.LookupEnv(grants.ServerCapabilitiesEnv)
	return grants.ConfigureServerOverride(db, value, set)
}

func mustConfigureServerEnv(t *testing.T, db *gorm.DB, raw string, present bool) {
	t.Helper()
	if err := configureServerEnv(t, db, raw, present); err != nil {
		t.Fatalf("%s=%q: %v", grants.ServerCapabilitiesEnv, raw, err)
	}
}

type overrideFixture struct {
	db      *gorm.DB
	server  models.Grant
	domain  models.Domain
	owner   models.Mailbox
	other   models.Mailbox
	admin   models.Mailbox
	super   models.Mailbox
	readCap models.Capability
}

func newOverrideFixture(t *testing.T) overrideFixture {
	t.Helper()
	db := openBootstrapTestDB(t)
	mustRBAC(t, db)
	f := overrideFixture{db: db, server: serverGrant(t, db)}
	f.domain = models.Domain{Name: "server-override.test", Active: true}
	if err := db.Create(&f.domain).Error; err != nil {
		t.Fatal(err)
	}
	account := func(local, role string) models.Mailbox {
		t.Helper()
		a := models.Mailbox{DomainID: f.domain.ID, LocalPart: local, Address: local + "@" + f.domain.Name, Active: true}
		if err := db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
		if role != "" {
			var r models.Role
			if err := db.Where("name = ?", role).First(&r).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: a.ID,
				RoleID: r.ID, Scope: grants.All, Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
				t.Fatal(err)
			}
		}
		return a
	}
	f.owner = account("owner", "")
	f.other = account("other", "")
	f.admin = account("admin", "admin")
	f.super = account("root", "superadmin")
	if err := db.Where("name = ?", models.CapMailRead).First(&f.readCap).Error; err != nil {
		t.Fatal(err)
	}
	return f
}

// denyStoredRead removes mail:read from the stored server ceiling so the
// database and the environment disagree.
func (f overrideFixture) denyStoredRead(t *testing.T) {
	t.Helper()
	if err := f.db.Where("role_id = ? AND capability_id = ?", f.server.RoleID, f.readCap.ID).
		Delete(&models.RoleCapability{}).Error; err != nil {
		t.Fatal(err)
	}
}

// allows checks both the uncached core path and a long-lived cache, which
// must apply the ceiling on every call.
func (f overrideFixture) allows(t *testing.T, cache *grants.Cache, a models.Mailbox, capability, scope string) bool {
	t.Helper()
	loaded, err := grants.Load(f.db, a.ID, time.Now())
	if err != nil {
		t.Fatalf("Load %s: %v", a.Address, err)
	}
	cached, err := cache.Held(a.ID)
	if err != nil {
		t.Fatalf("Cache.Held %s: %v", a.Address, err)
	}
	l, c := loaded.Allows(capability, scope), cached.Allows(capability, scope)
	if l != c {
		t.Fatalf("%s %s at %s: Load=%v, Cache.Held=%v", a.Address, capability, scope, l, c)
	}
	return l
}

func TestServerOverrideDB_UnsetHonorsStoredCeiling(t *testing.T) {
	f := newOverrideFixture(t)
	mustConfigureServerEnv(t, f.db, "mail:read", false)
	cache := grants.NewCache(f.db, time.Hour)
	own := grants.Mailbox(f.owner.Address)
	if !f.allows(t, cache, f.owner, models.CapMailRead, own) {
		t.Fatal("default stored ceiling refused the owner's own mail:read")
	}
	f.denyStoredRead(t)
	if f.allows(t, cache, f.owner, models.CapMailRead, own) {
		t.Fatal("absent override ignored the stored ceiling's removal of mail:read")
	}
	if !f.allows(t, cache, f.owner, models.CapMailWrite, own) {
		t.Fatal("absent override removed a capability the stored ceiling still allows")
	}
}

func TestServerOverrideDB_EnvReplacesDisagreeingStoredCeiling(t *testing.T) {
	f := newOverrideFixture(t)
	cache := grants.NewCache(f.db, time.Hour)
	own := grants.Mailbox(f.owner.Address)
	f.denyStoredRead(t)
	if f.allows(t, cache, f.owner, models.CapMailRead, own) {
		t.Fatal("stored ceiling without mail:read allowed it before the override")
	}

	mustConfigureServerEnv(t, f.db, "mail:read, domains:read", true)

	// The environment allows mail:read although the stored ceiling denies it.
	if !f.allows(t, cache, f.owner, models.CapMailRead, own) {
		t.Fatal("override did not replace the stored ceiling's denial of mail:read")
	}
	// The stored ceiling allows mail:write and mail:send; the environment omits them.
	for _, capability := range []string{models.CapMailWrite, models.CapMailSend} {
		if f.allows(t, cache, f.owner, capability, own) {
			t.Errorf("override omitted %s but the stored ceiling's permission survived", capability)
		}
	}
	// The ceiling never widens the actor's own grants.
	if f.allows(t, cache, f.owner, models.CapMailRead, grants.Mailbox(f.other.Address)) {
		t.Error("override granted mail:read on a mailbox the actor does not own")
	}
	if f.allows(t, cache, f.owner, "domains:read", grants.Domain(f.domain.Name)) {
		t.Error("override granted domains:read to an actor that lacks it")
	}
	if !f.allows(t, cache, f.admin, "domains:read", grants.Domain(f.domain.Name)) {
		t.Error("override refused domains:read to an admin that holds it")
	}
	if f.allows(t, cache, f.admin, "domains:write", grants.Domain(f.domain.Name)) {
		t.Error("override omitted domains:write but the admin still holds it")
	}

	// The override writes nothing: the stored ceiling still lacks mail:read.
	got, err := grants.RoleCapabilities(f.db, f.server.RoleID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if c == models.CapMailRead {
			t.Fatalf("override wrote to the stored ceiling: %v", got)
		}
	}
}

func TestServerOverrideDB_ExplicitEmptyDeniesOrdinaryActors(t *testing.T) {
	f := newOverrideFixture(t)
	mustConfigureServerEnv(t, f.db, "", true)
	cache := grants.NewCache(f.db, time.Hour)
	if f.allows(t, cache, f.owner, models.CapMailRead, grants.Mailbox(f.owner.Address)) {
		t.Error("explicit empty override allowed the owner's mail:read")
	}
	if f.allows(t, cache, f.admin, "domains:read", grants.Domain(f.domain.Name)) {
		t.Error("explicit empty override allowed the admin's domains:read")
	}
}

func TestServerOverrideDB_SuperadminExempt(t *testing.T) {
	f := newOverrideFixture(t)
	f.denyStoredRead(t)
	mustConfigureServerEnv(t, f.db, "", true)
	cache := grants.NewCache(f.db, time.Hour)
	for _, capability := range []string{models.CapMailRead, "domains:write"} {
		if !f.allows(t, cache, f.super, capability, grants.All) {
			t.Errorf("superadmin capped by the override: %s", capability)
		}
	}
}

// TestServerOverrideDB_InvalidValuesNeverWiden: a refused value leaves the
// previous ceiling in place instead of widening it.
func TestServerOverrideDB_InvalidValuesNeverWiden(t *testing.T) {
	f := newOverrideFixture(t)
	cache := grants.NewCache(f.db, time.Hour)
	own := grants.Mailbox(f.owner.Address)
	mustConfigureServerEnv(t, f.db, "mail:read", true)
	for _, raw := range []string{"mail:reed", "*", "mail:read,*", "mail:read,,mail:send", "mail:read,mail:send,"} {
		if err := configureServerEnv(t, f.db, raw, true); err == nil {
			t.Errorf("%q accepted, want refusal", raw)
		}
		if f.allows(t, cache, f.owner, models.CapMailSend, own) {
			t.Fatalf("refused %q widened the ceiling to mail:send", raw)
		}
		if !f.allows(t, cache, f.owner, models.CapMailRead, own) {
			t.Fatalf("refused %q replaced the previous override", raw)
		}
	}
}
