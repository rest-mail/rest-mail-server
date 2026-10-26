package handlers

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func TestTestEmailSendPersistence(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	if err := db.AutoMigrate(&models.Mailbox{}, &models.Message{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "restmail.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	sender := models.Mailbox{DomainID: domain.ID, LocalPart: "postmaster", Address: "postmaster@restmail.test", DisplayName: "Fixture Sender", Active: true}
	recipient := models.Mailbox{DomainID: domain.ID, LocalPart: "admin", Address: "admin@restmail.test", Active: true}
	for _, row := range []*models.Mailbox{&sender, &recipient} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	h := NewTestHandler(db, &config.Config{Environment: "test"})
	r := httptest.NewRequest("POST", "/api/v1/admin/test/send", nil)
	in := sendTestEmailRequest{From: sender.Address, To: recipient.Address, Subject: "Fixture ✓", Body: "你好 ✉️"}
	count := func() int64 {
		t.Helper()
		var n int64
		if err := db.Model(&models.Message{}).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	const callback = "test_email_sent_copy_failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if message, ok := tx.Statement.Dest.(*models.Message); ok && message.Folder == "Sent" {
			_ = tx.AddError(errors.New("private Sent insertion detail"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	if _, err := h.sendTestEmail(r, in); err == nil {
		t.Fatal("failed Sent copy reported success")
	}
	if count() != 0 {
		t.Fatal("Sent failure left the recipient INBOX copy behind")
	}
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	result, err := h.sendTestEmail(r, in)
	end := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	var rows []models.Message
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != result.MessageID || rows[0].MailboxID != recipient.ID || rows[0].Folder != "INBOX" || rows[0].IsRead || rows[1].MailboxID != sender.ID || rows[1].Folder != "Sent" || !rows[1].IsRead {
		t.Fatal("test injection lost its two mailbox/folder/read-state copies")
	}
	for _, row := range rows {
		if row.MsgID != result.MsgID || row.ThreadID != result.MsgID || row.Sender != sender.Address || row.SenderName != sender.DisplayName || row.Subject != in.Subject || row.BodyText != in.Body || row.SizeBytes != len(in.Body) || row.ReceivedAt.Before(start) || row.ReceivedAt.After(end) {
			t.Fatal("persisted test copy lost shared identifiers, supplied content, byte size or timestamp")
		}
	}
	self, err := h.sendTestEmail(r, sendTestEmailRequest{From: sender.Address, To: sender.Address})
	if err != nil || self.Subject != "Test email from REST Mail" {
		t.Fatalf("default/self send subject %q, error %v", self.Subject, err)
	}
	var selfRows []models.Message
	if err := db.Where("message_id = ?", self.MsgID).Order("id").Find(&selfRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(selfRows) != 2 || selfRows[0].ID != self.MessageID || selfRows[0].MailboxID != sender.ID || selfRows[1].MailboxID != sender.ID || selfRows[0].Folder != "INBOX" || selfRows[1].Folder != "Sent" || !strings.HasPrefix(selfRows[0].BodyText, "This is a test email sent at ") || selfRows[0].BodyText != selfRows[1].BodyText {
		t.Fatal("self send or generated default body lost one of its copies")
	}
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	if _, err := h.sendTestEmail(r.WithContext(ctx), in); err == nil || count() != 4 {
		t.Fatal("cancelled test injection wrote messages or reported success")
	}
}
