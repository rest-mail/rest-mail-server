package filters

import (
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
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
