package smtp

import (
	"fmt"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

// TestDBStore_AccountMaySendFollowsServerCeiling: AccountMaySend reads the
// account's grants within the server's capability ceiling on every call. The
// ceiling is set with the process-local operator override, so the shared test
// database's stored ceiling is never changed; the global superadmin is exempt.
func TestDBStore_AccountMaySendFollowsServerCeiling(t *testing.T) {
	db := openStoreTestDB(t)
	if err := db.AutoMigrate(&models.Role{}, &models.Capability{}, &models.RoleCapability{},
		&models.Grant{}, &models.GrantActivation{}); err != nil {
		t.Skipf("smtp dbStore test skipped: migrate failed (%v)", err)
	}
	for _, name := range []string{models.CapMailRead, models.CapMailSend, grants.Wildcard} {
		if err := db.Where("name = ?", name).FirstOrCreate(&models.Capability{Name: name}).Error; err != nil {
			t.Fatal(err)
		}
	}

	suffix := time.Now().UnixNano()
	dom := models.Domain{Name: fmt.Sprintf("ceiling-send-%d.test", suffix), Active: true}
	if err := db.Create(&dom).Error; err != nil {
		t.Fatal(err)
	}
	account := func(local string) models.Mailbox {
		t.Helper()
		a := models.Mailbox{DomainID: dom.ID, LocalPart: local, Address: local + "@" + dom.Name, Active: true, QuotaBytes: 1 << 30}
		if err := db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
		return a
	}
	alice, root := account("alice"), account("root")

	var wildcard models.Capability
	if err := db.Where("name = ?", grants.Wildcard).First(&wildcard).Error; err != nil {
		t.Fatal(err)
	}
	superadmin := models.Role{Name: fmt.Sprintf("ceiling-send-superadmin-%d", suffix)}
	if err := db.Create(&superadmin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.RoleCapability{RoleID: superadmin.ID, CapabilityID: wildcard.ID}).Error; err != nil {
		t.Fatal(err)
	}
	grant := models.Grant{HolderKind: models.GrantHolderAccount, HolderID: root.ID,
		RoleID: superadmin.ID, Scope: grants.All, Mode: models.GrantActive,
		GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = grants.ConfigureServerOverride(nil, "", false)
		db.Delete(&grant)
		db.Where("role_id = ?", superadmin.ID).Delete(&models.RoleCapability{})
		db.Delete(&superadmin)
		db.Where("domain_id = ?", dom.ID).Delete(&models.Mailbox{})
		db.Delete(&dom)
	})

	store := NewStore(db, "")
	maySend := func(a models.Mailbox) bool {
		t.Helper()
		ok, err := store.AccountMaySend(a.ID)
		if err != nil {
			t.Fatalf("AccountMaySend %s: %v", a.Address, err)
		}
		return ok
	}
	ceiling := func(raw string) {
		t.Helper()
		if err := grants.ConfigureServerOverride(db, raw, true); err != nil {
			t.Fatal(err)
		}
	}

	ceiling(models.CapMailRead + "," + models.CapMailSend)
	if !maySend(alice) || !maySend(root) {
		t.Fatal("refused while the server allows mail:send")
	}
	ceiling(models.CapMailRead)
	if maySend(alice) {
		t.Error("allowed after the server lost mail:send")
	}
	if !maySend(root) {
		t.Error("the superadmin was refused; it is exempt from the server ceiling")
	}
	ceiling(models.CapMailRead + "," + models.CapMailSend)
	if !maySend(alice) {
		t.Error("refused after mail:send was restored")
	}
}
