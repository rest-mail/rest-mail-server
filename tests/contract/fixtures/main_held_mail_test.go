package main

import (
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestHeldMailTargetsAreFreshAndLeaveOtherRowsAlone(t *testing.T) {
	database := fixtureDB(t)
	domain := models.Domain{Name: "restmail.test", Active: true}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	admin := models.Mailbox{DomainID: domain.ID, LocalPart: "admin", Address: "admin@restmail.test", Active: true}
	if err := database.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	queue := models.OutboundQueue{Sender: admin.Address, Recipient: "untouched@example.net", Domain: "example.net", Status: "deferred"}
	if err := database.Create(&queue).Error; err != nil {
		t.Fatal(err)
	}
	var previous heldMailTargets
	for attempt := 0; attempt < 3; attempt++ {
		targets, err := prepareHeldMailTargets(database, "restmail.test")
		if err != nil {
			t.Fatal(err)
		}
		if targets.AccountID != admin.ID || targets.DomainID != domain.ID || targets.MessageID <= previous.MessageID || targets.SuppressionID <= previous.SuppressionID {
			t.Fatalf("targets are not new rows for the fixture admin: %+v, previous %+v", targets, previous)
		}
		var message models.Quarantine
		if err := database.First(&message, targets.MessageID).Error; err != nil {
			t.Fatal(err)
		}
		if message.MailboxID != admin.ID || message.Released || message.RawMessage == "" {
			t.Fatalf("wrong held message: %+v", message)
		}
		var suppression models.Suppression
		if err := database.First(&suppression, targets.SuppressionID).Error; err != nil {
			t.Fatal(err)
		}
		if suppression.DomainID != domain.ID || suppression.Reason != models.SuppressionHardBounce {
			t.Fatalf("wrong suppression: %+v", suppression)
		}
		previous = targets
	}
	var count int64
	if err := database.Model(&models.OutboundQueue{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("held-mail setup changed the queue: count %d, error %v", count, err)
	}
	var untouched models.OutboundQueue
	if err := database.First(&untouched, queue.ID).Error; err != nil || untouched.Status != "deferred" {
		t.Fatalf("held-mail setup changed an existing queue entry: %+v, error %v", untouched, err)
	}
	if targets, err := prepareHeldMailTargets(database, "production.example"); err == nil || targets != (heldMailTargets{}) {
		t.Fatalf("non-fixture domain was accepted: %+v, error %v", targets, err)
	}
}
