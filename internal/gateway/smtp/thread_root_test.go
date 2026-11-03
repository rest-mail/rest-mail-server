package smtp

import (
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestDBStoreThreadRoot(t *testing.T) {
	database := openStoreTestDB(t)
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: "smtp-thread-root.test", Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	owner := models.Mailbox{DomainID: domain.ID, LocalPart: "owner", Address: "owner@" + domain.Name, Active: true}
	other := models.Mailbox{DomainID: domain.ID, LocalPart: "other", Address: "other@" + domain.Name, Active: true}
	if err := tx.Create(&[]models.Mailbox{owner, other}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Where("address = ?", owner.Address).First(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Where("address = ?", other.Address).First(&other).Error; err != nil {
		t.Fatal(err)
	}
	parents := []models.Message{
		{MailboxID: owner.ID, MsgID: "parent@smtp.test", ThreadID: "<root@smtp.test>", Sender: owner.Address},
		{MailboxID: other.ID, MsgID: "<foreign@smtp.test>", ThreadID: "<private@smtp.test>", Sender: other.Address},
	}
	if err := tx.Create(&parents).Error; err != nil {
		t.Fatal(err)
	}
	store := NewStore(tx, "")
	for _, test := range []struct{ name, parent, refs, want string }{
		{"known parent", "<parent@smtp.test>", "", "<root@smtp.test>"},
		{"known reference", "", "<untrusted@smtp.test> <parent@smtp.test>", "<root@smtp.test>"},
		{"unknown parent", "unknown@smtp.test", "", "<unknown@smtp.test>"},
		{"unknown references", "unknown@smtp.test", "<header-root@smtp.test> <unknown@smtp.test>", "<header-root@smtp.test>"},
		{"foreign reference", "", "<foreign@smtp.test>", "<foreign@smtp.test>"},
	} {
		t.Run(test.name, func(t *testing.T) {
			id, err := store.PersistSubmittedMessage(SubmittedMessage{Sender: owner.Address, MessageID: "<reply@smtp.test>", InReplyTo: test.parent, References: test.refs, Subject: "reply"})
			if err != nil || id == nil {
				t.Fatalf("persist: %v, %v", id, err)
			}
			var row models.Message
			if err := tx.First(&row, *id).Error; err != nil {
				t.Fatal(err)
			}
			if row.ThreadID != test.want {
				t.Errorf("thread_id = %q, want %q", row.ThreadID, test.want)
			}
		})
	}
}
