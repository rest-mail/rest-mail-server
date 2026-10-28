package bootstrap

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

func serverGrant(t *testing.T, db *gorm.DB) models.Grant {
	t.Helper()
	var g models.Grant
	if err := db.Where("holder_kind = ? AND holder_id = ?", "server", 1).First(&g).Error; err != nil {
		t.Fatalf("installation-wide server grant: %v", err)
	}
	if g.Scope != grants.All || g.Mode != models.GrantActive || g.ExpiresAt != nil {
		t.Fatalf("server grant must be permanent and active at *: %+v", g)
	}
	return g
}

func TestServerCeiling_DefaultsAndRestartPreserveRestrictions(t *testing.T) {
	for _, migrated := range []bool{false, true} {
		name := "fresh"
		if migrated {
			name = "migrated"
		}
		t.Run(name, func(t *testing.T) {
			db := openBootstrapTestDB(t)
			if migrated {
				// An existing installation already has its roles and capabilities.
				if err := db.Create(&models.Capability{Name: "legacy:operation"}).Error; err != nil {
					t.Fatal(err)
				}
				if err := rmdb.AutoMigrate(db); err != nil {
					t.Fatal(err)
				}
			}
			mustRBAC(t, db)
			g := serverGrant(t, db)
			got, err := grants.RoleCapabilities(db, g.RoleID)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			if err := db.Model(&models.Capability{}).Where("name <> ?", grants.Wildcard).Order("name").Pluck("name", &want).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("default ceiling = %v, want every explicit capability %v", got, want)
			}
			if err := db.Where("role_id = ? AND capability_id = (SELECT id FROM capabilities WHERE name = ?)", g.RoleID, models.CapMailSend).
				Delete(&models.RoleCapability{}).Error; err != nil {
				t.Fatal(err)
			}
			mustRBAC(t, db)
			if err := rmdb.AutoMigrate(db); err != nil {
				t.Fatal(err)
			}
			got, err = grants.RoleCapabilities(db, serverGrant(t, db).RoleID)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range got {
				if c == models.CapMailSend || c == grants.Wildcard {
					t.Fatalf("restart restored a revoked capability: %v", got)
				}
			}
			if err := db.Where("role_id = ?", g.RoleID).Delete(&models.RoleCapability{}).Error; err != nil {
				t.Fatal(err)
			}
			mustRBAC(t, db)
			got, err = grants.RoleCapabilities(db, g.RoleID)
			if err != nil || len(got) != 0 {
				t.Fatalf("restart changed an empty ceiling: %v, %v", got, err)
			}
		})
	}
}

func TestServerCeiling_ExistingSessionSeesRemovalOnNextRequest(t *testing.T) {
	db := openBootstrapTestDB(t)
	mustRBAC(t, db)
	g := serverGrant(t, db)
	domain := models.Domain{Name: "server-ceiling.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	account := func(local, role, scope string) models.Mailbox {
		t.Helper()
		a := models.Mailbox{DomainID: domain.ID, LocalPart: local, Address: local + "@" + domain.Name, Active: true}
		if err := db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
		if role != "" {
			var r models.Role
			if err := db.Where("name = ?", role).First(&r).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: a.ID,
				RoleID: r.ID, Scope: scope, Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
				t.Fatal(err)
			}
		}
		return a
	}
	admin := account("admin", "admin", grants.All)
	super := account("root", "superadmin", grants.All)
	scoped := account("scoped", "superadmin", grants.Domain(domain.Name))
	owner := account("owner", "", "")
	jwt := auth.NewJWTService("server-ceiling-test", time.Hour, 24*time.Hour)
	tokens := map[uint]string{}
	for _, a := range []models.Mailbox{admin, super, scoped, owner} {
		pair, err := jwt.GenerateSession(a.ID, a.Address, 0, []string{auth.AMRPassword}, false)
		if err != nil {
			t.Fatal(err)
		}
		tokens[a.ID] = pair.AccessToken
	}
	// Two replicas cache the actor's grants for an hour; no invalidation or
	// token replacement may be needed for a server restriction to take effect.
	caches := []*grants.Cache{grants.NewCache(db, time.Hour), grants.NewCache(db, time.Hour)}
	request := func(cache *grants.Cache, a models.Mailbox, capability, scope string) int {
		t.Helper()
		h := middleware.RequireCapability(cache.Held, capability, func(*http.Request) (string, bool, error) { return scope, true, nil })(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		r := httptest.NewRequest(http.MethodGet, "/protected", nil)
		r.Header.Set("Authorization", "Bearer "+tokens[a.ID])
		w := httptest.NewRecorder()
		middleware.JWTMiddleware(jwt)(h).ServeHTTP(w, r)
		return w.Code
	}
	for _, cache := range caches {
		for _, a := range []models.Mailbox{admin, super, scoped} {
			if got := request(cache, a, "domains:read", grants.Domain(domain.Name)); got != http.StatusNoContent {
				t.Fatalf("before restriction, %s: HTTP %d", a.Address, got)
			}
		}
		if got := request(cache, owner, models.CapMailRead, grants.Mailbox(owner.Address)); got != http.StatusNoContent {
			t.Fatalf("owner before restriction: HTTP %d", got)
		}
		if got := request(cache, owner, "domains:read", grants.Domain(domain.Name)); got != http.StatusForbidden {
			t.Fatalf("server capabilities granted actor extra access: HTTP %d", got)
		}
	}
	if err := db.Where("role_id = ? AND capability_id IN (SELECT id FROM capabilities WHERE name IN ?)",
		g.RoleID, []string{"domains:read", models.CapMailRead}).Delete(&models.RoleCapability{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, cache := range caches {
		for _, a := range []models.Mailbox{admin, scoped} {
			if got := request(cache, a, "domains:read", grants.Domain(domain.Name)); got != http.StatusForbidden {
				t.Errorf("next request after removal, %s: HTTP %d, want 403", a.Address, got)
			}
		}
		if got := request(cache, owner, models.CapMailRead, grants.Mailbox(owner.Address)); got != http.StatusForbidden {
			t.Errorf("owner's implicit permission escaped the ceiling: HTTP %d", got)
		}
		if got := request(cache, super, "domains:read", grants.Domain(domain.Name)); got != http.StatusNoContent {
			t.Errorf("superadmin recovery exception: HTTP %d", got)
		}
	}
	held, err := grants.Load(db, admin.ID, time.Now())
	if err != nil || held.Allows("domains:read", grants.All) {
		t.Errorf("uncached core Load escaped ceiling: held=%v err=%v", held, err)
	}
	for _, name := range []string{"domains:read", models.CapMailRead} {
		var capability models.Capability
		if err := db.Where("name = ?", name).First(&capability).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.RoleCapability{RoleID: g.RoleID, CapabilityID: capability.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, cache := range caches {
		if got := request(cache, admin, "domains:read", grants.All); got != http.StatusNoContent {
			t.Errorf("restored ceiling did not restore existing session: HTTP %d", got)
		}
		if got := request(cache, owner, models.CapMailRead, grants.Mailbox(owner.Address)); got != http.StatusNoContent {
			t.Errorf("restored ceiling did not restore implicit owner permission: HTTP %d", got)
		}
	}
	if err := db.Where("role_id = ?", g.RoleID).Delete(&models.RoleCapability{}).Error; err != nil {
		t.Fatal(err)
	}
	if got := request(caches[0], super, "domains:read", grants.All); got != http.StatusNoContent {
		t.Errorf("empty ceiling locked out superadmin: HTTP %d", got)
	}
	if got := request(caches[0], admin, "domains:write", grants.All); got != http.StatusForbidden {
		t.Errorf("empty ceiling allowed admin: HTTP %d", got)
	}
	const callback = "server-ceiling:unavailable"
	if err := db.Callback().Row().Before("gorm:row").Register(callback, func(tx *gorm.DB) {
		// Actor grants are already cached; the only query is the ceiling.
		_ = tx.AddError(errors.New("server ceiling unavailable"))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Row().Remove(callback) })
	if got := request(caches[0], admin, "domains:write", grants.All); got != http.StatusServiceUnavailable {
		t.Errorf("unreadable ceiling must fail closed: HTTP %d", got)
	}
	if got := request(caches[0], super, "domains:write", grants.All); got != http.StatusNoContent {
		t.Errorf("unreadable ceiling locked out cached superadmin: HTTP %d", got)
	}
}

func TestServerCeiling_AccountGrantChangesCannotEditServer(t *testing.T) {
	db := openBootstrapTestDB(t)
	mustRBAC(t, db)
	g := serverGrant(t, db)
	if err := grants.Remove(db, &g, models.AuditEntry{}); !errors.Is(err, grants.ErrExceedsHeld) {
		t.Fatalf("account grant removal of server ceiling: %v, want ErrExceedsHeld", err)
	}
	if err := grants.Add(db, &g, models.AuditEntry{}); !errors.Is(err, grants.ErrExceedsHeld) {
		t.Fatalf("account grant addition to server ceiling: %v, want ErrExceedsHeld", err)
	}
	if got := serverGrant(t, db); got.ID != g.ID {
		t.Fatal("refused operation changed the server grant")
	}
	if err := db.First(&g.Role, g.RoleID).Error; err != nil {
		t.Fatal(err)
	}
	g.HolderKind = models.GrantHolderAccount
	g.ID = 0
	if err := grants.Add(db, &g, models.AuditEntry{}); !errors.Is(err, grants.ErrExceedsHeld) {
		t.Fatalf("server role assigned to account: %v, want ErrExceedsHeld", err)
	}
}
