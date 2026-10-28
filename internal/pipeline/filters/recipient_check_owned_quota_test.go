package filters

import (
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"github.com/restmail/restmail/internal/pipeline"
)

func TestRecipientCheckOwnedQuota(t *testing.T) {
	db := openRecipientCheckTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: "owned-quota-recipient.test", Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	owner := models.Mailbox{DomainID: domain.ID, LocalPart: "owner", Address: "owner@" + domain.Name, QuotaBytes: 100, QuotaUsedBytes: 40}
	if err := tx.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	child := models.Mailbox{DomainID: domain.ID, LocalPart: "child", Address: "child@" + domain.Name, OwnerID: &owner.ID}
	if err := tx.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Model(&child).Update("quota_bytes", 0).Error; err != nil {
		t.Fatal(err)
	}
	sibling := models.Mailbox{DomainID: domain.ID, LocalPart: "sibling", Address: "sibling@" + domain.Name, OwnerID: &owner.ID, QuotaUsedBytes: 60}
	if err := tx.Create(&sibling).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Model(&sibling).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	for _, mb := range []*models.Mailbox{&owner, &child} {
		if err := grants.EnsureReceive(tx, mb); err != nil {
			t.Fatal(err)
		}
	}
	// The owner holds the owner role on the account it owns, which bounds it.
	var ownerRole models.Role
	if err := tx.Where("name = ?", models.RoleOwner).First(&ownerRole).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: owner.ID, RoleID: ownerRole.ID,
		Scope: "mailbox:" + child.Address, Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{child.Address, owner.Address} {
		got := runRecipientCheck(t, tx, address)
		if got.Action != pipeline.ActionReject || !strings.HasPrefix(got.RejectMsg, "452 4.2.2") {
			t.Fatalf("shared full quota: %+v", got)
		}
	}
	if err := tx.Model(&sibling).Update("quota_used_bytes", 59).Error; err != nil {
		t.Fatal(err)
	}
	if got := runRecipientCheck(t, tx, child.Address); got.Action != pipeline.ActionContinue {
		t.Fatalf("reclaimed space: %+v", got)
	}
}
