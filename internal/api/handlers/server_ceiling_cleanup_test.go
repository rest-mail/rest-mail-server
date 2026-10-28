package handlers

import (
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// Minimal persistence fixtures seed their own actor roles instead of running
// bootstrap. Give them the same explicit, default-allow server object too.
func ensureDefaultServerCeiling(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, name := range models.OwnerCapabilities {
		capability := models.Capability{Name: name}
		if err := db.Where("name = ?", name).FirstOrCreate(&capability).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := grants.EnsureServerCeiling(db); err != nil {
		t.Fatal(err)
	}
}

func TestClearTestDataPreservesServerCeiling(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	if err := db.AutoMigrate(&models.Role{}, &models.Grant{}); err != nil {
		t.Fatal(err)
	}
	role := models.Role{Name: models.RoleServer, SystemRole: true}
	if err := db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	grant := models.Grant{HolderKind: models.GrantHolderServer, HolderID: models.ServerHolderID,
		RoleID: role.ID, Scope: "*", Mode: models.GrantActive,
		GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	// There need not be an administrator with the same numeric ID as the
	// server object. Clearing accounts must preserve grants of other kinds.
	if err := db.Transaction(clearTestData); err != nil {
		t.Fatal(err)
	}
	var after models.Grant
	if err := db.First(&after, grant.ID).Error; err != nil {
		t.Fatalf("test-data cleanup removed the server ceiling: %v", err)
	}
	if after.HolderKind != grant.HolderKind || after.RoleID != grant.RoleID {
		t.Fatal("test-data cleanup changed the server ceiling")
	}
}
