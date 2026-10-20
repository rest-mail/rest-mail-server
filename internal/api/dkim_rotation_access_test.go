package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

func TestDKIMRotation_MigrationDefaultsExistingDomainsToDisabled(t *testing.T) {
	db := createScratchDB(t, "dkim_rotation_migration")
	if err := db.AutoMigrate(&models.Domain{}); err != nil {
		t.Fatal(err)
	}
	installed := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	legacy := models.Domain{Name: "legacy-rotation.test", DKIMSelector: "mail", DKIMPrivateKey: "stored-key-sentinel", DKIMCreatedAt: &installed}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	// Recreate the pre-interval schema, keeping the existing domain and key.
	if err := db.Migrator().DropColumn(&models.Domain{}, "DKIMRotationIntervalDays"); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Domain{}); err != nil {
		t.Fatal(err)
	}
	var migrated models.Domain
	if err := db.First(&migrated, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if migrated.DKIMRotationIntervalDays != 0 || migrated.DKIMSelector != legacy.DKIMSelector ||
		migrated.DKIMPrivateKey != legacy.DKIMPrivateKey || migrated.DKIMCreatedAt == nil || !migrated.DKIMCreatedAt.Equal(installed) {
		t.Fatal("migration enabled rotation or changed the existing signing key")
	}
	if err := db.AutoMigrate(&models.Domain{}); err != nil {
		t.Fatalf("repeated migration: %v", err)
	}
}

func TestDKIMRotation_DomainAdminOnlyConfiguresOwnDomain(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "dkimdom"})
	owner := f.token(t, "owner")
	if rr := f.grant(t, owner, f.admins["dkimdom"].user.ID, "admin", "domain:a.test"); rr.Code != http.StatusCreated {
		t.Fatalf("grant domain admin = %d %s", rr.Code, rr.Body.String())
	}
	token := f.admins["dkimdom"].login(t, f.router).AccessToken
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		for _, tc := range []struct {
			id      uint
			allowed bool
		}{{f.a.ID, true}, {f.b.ID, false}} {
			path := fmt.Sprintf("/api/v1/admin/dkim/%d/rotation", tc.id)
			rr := doRequest(f.router, method, path, token, `{"rotation_interval_days":90}`)
			if tc.allowed && rr.Code != http.StatusOK {
				t.Fatalf("%s own settings = %d %s", method, rr.Code, rr.Body.String())
			}
			if !tc.allowed && !refusedForAccess(rr) {
				t.Fatalf("%s other domain settings = %d %s, want refused", method, rr.Code, rr.Body.String())
			}
		}
	}
	var other models.Domain
	if err := f.db.First(&other, f.b.ID).Error; err != nil {
		t.Fatal(err)
	}
	if other.DKIMRotationIntervalDays != 0 {
		t.Fatal("domain admin changed another domain's interval")
	}
}
