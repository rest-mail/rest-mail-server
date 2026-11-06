package bootstrap

import (
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func TestFreshDomainProvisionsPostmaster(t *testing.T) {
	database := openBootstrapTestDB(t)
	mustRBAC(t, database)
	if outcome, err := EnsureFirstAdmin(database, adminAddr, strongPassword, true, t0); err != nil || outcome != FirstAdminCreated {
		t.Fatalf("bootstrap: %v, %v", outcome, err)
	}
	var account models.Mailbox
	if err := database.Where("address = ?", "postmaster@example.com").First(&account).Error; err != nil {
		t.Fatal(err)
	}
	if !account.Active || account.QuotaBytes != 1<<30 || account.OwnerID != nil || account.DisplayName != "" {
		t.Fatalf("ordinary account defaults: %+v", account)
	}
	if allowed, err := grants.CanReceive(database, &account); err != nil || !allowed {
		t.Fatalf("receive grant: %v, %v", allowed, err)
	}
	var usage models.QuotaUsage
	if err := database.First(&usage, "mailbox_id = ?", account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if password := passwordOf(t, database, account.ID); password != "" {
		t.Fatal("generated postmaster has a password")
	}
}

func TestBootstrapPostmasterCanBeFirstAdmin(t *testing.T) {
	database := openBootstrapTestDB(t)
	mustRBAC(t, database)
	const address = "postmaster@example.com"
	if outcome, err := EnsureFirstAdmin(database, address, strongPassword, true, t0); err != nil || outcome != FirstAdminCreated {
		t.Fatalf("bootstrap: %v, %v", outcome, err)
	}
	account := adminNamed(t, database, address)
	if err := auth.CheckPassword(strongPassword, passwordOf(t, database, account.ID)); err != nil {
		t.Fatalf("bootstrap password: %v", err)
	}
	if allowed, err := grants.CanReceive(database, &account); err != nil || !allowed {
		t.Fatalf("receive grant: %v, %v", allowed, err)
	}
	if outcome, err := EnsureFirstAdmin(database, address, strongPassword, true, t0); err != nil || outcome != FirstAdminUnchanged {
		t.Fatalf("restart: %v, %v", outcome, err)
	}
}
