package handlers

import (
	"errors"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
	"gorm.io/gorm"
)

// Scratch databases copy a template, so this proves what a fresh database gave: a test's
// rows and schema changes reach neither the next test's copy nor the template.
func TestScratchDatabasesAreSeparateCopies(t *testing.T) {
	first := openCustomFilterCreateScratchDB(t, &models.Ban{})
	if err := first.Create(&models.Domain{Name: "first-copy.test"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := first.Migrator().DropTable(&models.Ban{}); err != nil {
		t.Fatal(err)
	}

	second := openCustomFilterCreateScratchDB(t, &models.Ban{})
	var domains int64
	if err := second.Model(&models.Domain{}).Count(&domains).Error; err != nil {
		t.Fatal(err)
	}
	if domains != 0 {
		t.Errorf("the next copy holds %d domains from the one before", domains)
	}
	if !second.Migrator().HasTable(&models.Ban{}) {
		t.Error("the next copy lost the table the one before dropped")
	}
	var names []string
	for _, db := range []*gorm.DB{first, second} {
		var name string
		if err := db.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if names[0] == names[1] {
		t.Errorf("both tests got database %s", names[0])
	}
}

// ensureRBAC skips bootstrap.EnsureRBAC only where it would change nothing, so whatever
// a test takes out of the role tables must still come back.
func TestEnsureRBACRunsAgainAfterTheRolesChange(t *testing.T) {
	shared, err := connectTestDB()
	if err != nil {
		testdb.Unreachable(t, "no database reachable (%v)", err)
	}
	if err := ensureRBAC(shared); err != nil {
		t.Fatal(err)
	}
	tx := shared.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })

	var capability models.Capability
	if err := tx.First(&capability, "name = ?", "domains:read").Error; err != nil {
		t.Fatal(err)
	}
	var admin models.Role
	if err := tx.First(&admin, "name = ?", "admin").Error; err != nil {
		t.Fatal(err)
	}
	link := func() error {
		return tx.First(&models.RoleCapability{}, "role_id = ? AND capability_id = ?", admin.ID, capability.ID).Error
	}
	if err := link(); err != nil {
		t.Fatalf("admin does not hold domains:read before the test: %v", err)
	}
	if err := tx.Exec("DELETE FROM role_capabilities WHERE role_id = ? AND capability_id = ?", admin.ID, capability.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := link(); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("the link is still there after deleting it: %v", err)
	}
	if err := ensureRBAC(tx); err != nil {
		t.Fatal(err)
	}
	if err := link(); err != nil {
		t.Errorf("ensureRBAC left out a link a test removed: %v", err)
	}
}
