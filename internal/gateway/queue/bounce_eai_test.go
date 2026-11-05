package queue

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// TestGenerateBounce_EAISender proves a bounce reaches the hosted mailbox a
// submission was attributed to when the envelope sender is a Unicode-domain,
// decomposed or upper-case spelling of the stored identity (#153), and that the
// DSN addresses the sender as submitted.
func TestGenerateBounce_EAISender(t *testing.T) {
	db := openDSNTestDB(t)
	if err := db.AutoMigrate(&models.Domain{}, &models.Mailbox{}); err != nil {
		t.Skipf("migrate failed (%v)", err)
	}

	suffix := time.Now().UnixNano()
	unicodeDomain := fmt.Sprintf("bücher-%d.test", suffix)
	asciiDomain, err := rmail.DomainASCII(unicodeDomain)
	if err != nil {
		t.Fatalf("DomainASCII: %v", err)
	}
	sender := "JOSÉ@" + unicodeDomain

	dom := models.Domain{Name: asciiDomain, Active: true}
	if err := db.Create(&dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	mb := models.Mailbox{DomainID: dom.ID, LocalPart: "josé", Address: "josé@" + asciiDomain, Active: true, QuotaBytes: 1 << 30}
	if err := db.Create(&mb).Error; err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	sent := models.Message{MailboxID: mb.ID, Folder: "Sent", Sender: sender, RecipientsTo: models.JSONB("[]"),
		RecipientsCc: models.JSONB("[]"), ReceivedAt: time.Now()}
	if err := db.Create(&sent).Error; err != nil {
		t.Fatalf("create sent message: %v", err)
	}
	t.Cleanup(func() {
		db.Where("mailbox_id = ?", mb.ID).Delete(&models.Message{})
		db.Where("id = ?", mb.ID).Delete(&models.Mailbox{})
		db.Where("id = ?", dom.ID).Delete(&models.Domain{})
	})

	w := NewWorker(db, "mx.bounce-eai.test", 1, time.Second)
	w.generateBounce(models.OutboundQueue{
		Sender:    sender,
		Recipient: "carol@remote.test",
		Domain:    "remote.test",
		MessageID: &sent.ID,
	}, &SMTPError{Code: 550, Enhanced: "5.1.1", Message: "no such user"})

	var dsn models.Message
	if err := db.Where("mailbox_id = ? AND is_dsn = ?", mb.ID, true).First(&dsn).Error; err != nil {
		t.Fatalf("no DSN delivered to the stored identity: %v", err)
	}
	if !dsn.IsDSN || dsn.Folder != "INBOX" {
		t.Errorf("DSN folder = %q, is_dsn = %v; want INBOX, true", dsn.Folder, dsn.IsDSN)
	}
	digest, err := dsn.SealDigest()
	if err != nil || dsn.SealedHash == "" || digest != dsn.SealedHash {
		t.Fatalf("DSN receipt not sealed: stored %q, computed %q, error %v", dsn.SealedHash, digest, err)
	}
	// getMessage links a message to its thread, so the receipt needs one to link to.
	if dsn.MsgID == "" || dsn.ThreadID != dsn.MsgID {
		t.Errorf("DSN message_id %q, thread_id %q; want one thread named by the DSN's own Message-ID", dsn.MsgID, dsn.ThreadID)
	}
	if !strings.Contains(dsn.RawMessage, "Message-ID: "+dsn.MsgID+"\r\n") {
		t.Errorf("DSN message_id %q is not the Message-ID in its source", dsn.MsgID)
	}
}
