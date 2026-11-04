package db_test

import (
	"testing"

	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// A row stored before pop3_utf8_size existed holds a downgraded_size that counted the stored
// line breaks. The backfill measures both from the stored source and changes nothing else.
func TestBackfillPOP3Sizes(t *testing.T) {
	database := openDKIMRotationTestDB(t)
	domain := models.Domain{Name: "pop3-sizes.test"}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	box := models.Mailbox{DomainID: domain.ID, Address: "a@pop3-sizes.test", LocalPart: "a"}
	if err := database.Create(&box).Error; err != nil {
		t.Fatal(err)
	}
	store := func(raw string) models.Message {
		t.Helper()
		m := models.Message{MailboxID: box.ID, Folder: "INBOX", MsgID: raw, ThreadID: raw, RawMessage: raw, RawSize: len(raw)}
		if err := database.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
		return m
	}
	const bareLF, noBreak = "Subject: hi\n\nline one\nline two\n", "Subject: Grüße\r\n\r\nbody"
	for _, raw := range []string{bareLF, noBreak} {
		m := store(raw)
		if err := database.Model(&m).UpdateColumns(map[string]any{"downgraded_size": len(raw), "pop3_utf8_size": 0}).Error; err != nil {
			t.Fatal(err)
		}
	}
	measured := store("Subject: measured\r\n\r\nbody\r\n")
	if err := database.Model(&measured).UpdateColumn("downgraded_size", 12345).Error; err != nil {
		t.Fatal(err)
	}

	if err := rmdb.BackfillPOP3Sizes(database); err != nil {
		t.Fatal(err)
	}

	for _, raw := range []string{bareLF, noBreak} {
		var got models.Message
		if err := database.Where("message_id = ?", raw).First(&got).Error; err != nil {
			t.Fatal(err)
		}
		if got.DowngradedSize != rmail.DowngradedSize(raw) || got.POP3UTF8Size != rmail.POP3WireSize(raw) {
			t.Fatalf("%q: downgraded_size %d, pop3_utf8_size %d", raw, got.DowngradedSize, got.POP3UTF8Size)
		}
		if got.RawMessage != raw || got.RawSize != len(raw) {
			t.Fatalf("stored message changed: %q, raw_size %d", got.RawMessage, got.RawSize)
		}
	}
	var again models.Message
	if err := database.First(&again, measured.ID).Error; err != nil {
		t.Fatal(err)
	}
	if again.DowngradedSize != 12345 {
		t.Fatalf("a measured row was read again: downgraded_size %d", again.DowngradedSize)
	}
}
