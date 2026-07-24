package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// openMsgDeleteTestDB connects to the unit-test Postgres and migrates the tables
// the message-delete tests touch. It skips (never fails) when no database is
// reachable, matching the repo's depless-local / DB-in-CI convention.
func openMsgDeleteTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: envIntOr("DB_PORT", 5432),
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("message-delete DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(
		&models.Domain{},
		&models.Mailbox{},
		&models.WebmailAccount{},
		&models.LinkedAccount{},
		&models.Message{},
		&models.Attachment{},
	); err != nil {
		t.Skipf("message-delete DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// msgDeleteFixture is one owner mailbox + webmail account with a known starting
// quota, ready to receive seeded messages.
type msgDeleteFixture struct {
	mailbox models.Mailbox
	account models.WebmailAccount
}

func seedMsgDeleteFixture(t *testing.T, db *gorm.DB, quotaUsed int64) msgDeleteFixture {
	t.Helper()
	domain := models.Domain{Name: "delq.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	mb := models.Mailbox{
		DomainID:       domain.ID,
		LocalPart:      "owner",
		Address:        "owner@delq.test",
		Password:       "x",
		QuotaBytes:     1 << 30,
		QuotaUsedBytes: quotaUsed,
	}
	if err := db.Create(&mb).Error; err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	acct := models.WebmailAccount{PrimaryMailboxID: mb.ID}
	if err := db.Create(&acct).Error; err != nil {
		t.Fatalf("create account: %v", err)
	}
	return msgDeleteFixture{mailbox: mb, account: acct}
}

func quotaUsedBytes(t *testing.T, db *gorm.DB, mailboxID uint) int64 {
	t.Helper()
	var mb models.Mailbox
	if err := db.First(&mb, mailboxID).Error; err != nil {
		t.Fatalf("reload mailbox: %v", err)
	}
	return mb.QuotaUsedBytes
}

func countMessages(t *testing.T, db *gorm.DB, mailboxID uint) int64 {
	t.Helper()
	var n int64
	db.Model(&models.Message{}).Where("mailbox_id = ?", mailboxID).Count(&n)
	return n
}

// TestDeleteMessage_GatewayPermanentReclaimsQuota is the core regression for
// issue #191: an IMAP EXPUNGE / POP3 QUIT delete arrives as a permanent delete
// and MUST reclaim quota and remove the row — not soft-delete it into an
// unreachable, still-counted state.
func TestDeleteMessage_GatewayPermanentReclaimsQuota(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	const size = 4096
	fx := seedMsgDeleteFixture(t, tx, size)
	msg := models.Message{
		MailboxID: fx.mailbox.ID,
		Folder:    "INBOX",
		Sender:    "someone@remote.test",
		SizeBytes: size,
	}
	if err := tx.Create(&msg).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}

	h := NewMessageHandler(tx, nil, nil, "", nil)
	idStr := uintToStr(msg.ID)
	rr := httptest.NewRecorder()
	req := authzRequest(http.MethodDelete,
		"/api/v1/messages/"+idStr+"?permanent=true",
		fx.account.ID, map[string]string{"id": idStr}, "")
	h.DeleteMessage(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("permanent delete: expected 204, got %d (%s)", rr.Code, rr.Body.String())
	}
	if n := countMessages(t, tx, fx.mailbox.ID); n != 0 {
		t.Fatalf("permanent delete must remove the row, still present (count=%d)", n)
	}
	if used := quotaUsedBytes(t, tx, fx.mailbox.ID); used != 0 {
		t.Fatalf("permanent delete must reclaim quota: quota_used_bytes=%d, want 0", used)
	}
}

// TestDeleteMessage_GatewayPermanentReclaimsAttachments confirms attachment
// bytes are reclaimed and the attachment rows removed on a permanent delete.
func TestDeleteMessage_GatewayPermanentReclaimsAttachments(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	const bodySize = 1000
	const attSize = 5000
	fx := seedMsgDeleteFixture(t, tx, bodySize+attSize)
	msg := models.Message{
		MailboxID:      fx.mailbox.ID,
		Folder:         "INBOX",
		Sender:         "someone@remote.test",
		SizeBytes:      bodySize,
		HasAttachments: true,
	}
	if err := tx.Create(&msg).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}
	att := models.Attachment{
		MessageID:  msg.ID,
		Filename:   "a.pdf",
		SizeBytes:  attSize,
		StorageRef: "ref",
	}
	if err := tx.Create(&att).Error; err != nil {
		t.Fatalf("create attachment: %v", err)
	}

	h := NewMessageHandler(tx, nil, nil, "", nil)
	idStr := uintToStr(msg.ID)
	rr := httptest.NewRecorder()
	req := authzRequest(http.MethodDelete,
		"/api/v1/messages/"+idStr+"?permanent=true",
		fx.account.ID, map[string]string{"id": idStr}, "")
	h.DeleteMessage(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("permanent delete: expected 204, got %d (%s)", rr.Code, rr.Body.String())
	}
	if used := quotaUsedBytes(t, tx, fx.mailbox.ID); used != 0 {
		t.Fatalf("permanent delete must reclaim message+attachment bytes: quota_used_bytes=%d, want 0", used)
	}
	var attCount int64
	tx.Model(&models.Attachment{}).Where("message_id = ?", msg.ID).Count(&attCount)
	if attCount != 0 {
		t.Fatalf("permanent delete must remove attachment rows, count=%d", attCount)
	}
}

// TestDeleteMessage_WebmailMoveToTrashVisibleAndCounted pins the webmail
// (non-permanent) first delete: the message moves to Trash as a real, visible
// item (is_deleted stays false) and still counts toward quota because it is
// still stored.
func TestDeleteMessage_WebmailMoveToTrashVisibleAndCounted(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	const size = 2048
	fx := seedMsgDeleteFixture(t, tx, size)
	msg := models.Message{
		MailboxID: fx.mailbox.ID,
		Folder:    "INBOX",
		Sender:    "someone@remote.test",
		SizeBytes: size,
	}
	if err := tx.Create(&msg).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}

	h := NewMessageHandler(tx, nil, nil, "", nil)
	idStr := uintToStr(msg.ID)
	rr := httptest.NewRecorder()
	req := authzRequest(http.MethodDelete,
		"/api/v1/messages/"+idStr,
		fx.account.ID, map[string]string{"id": idStr}, "")
	h.DeleteMessage(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("move to trash: expected 204, got %d (%s)", rr.Code, rr.Body.String())
	}
	var reloaded models.Message
	if err := tx.First(&reloaded, msg.ID).Error; err != nil {
		t.Fatalf("message must still exist after move to trash: %v", err)
	}
	if reloaded.Folder != "Trash" {
		t.Fatalf("expected folder Trash, got %q", reloaded.Folder)
	}
	if reloaded.IsDeleted {
		t.Fatal("move to trash must keep is_deleted=false so the item is visible in Trash")
	}
	if used := quotaUsedBytes(t, tx, fx.mailbox.ID); used != size {
		t.Fatalf("a trashed-but-stored message still counts toward quota: quota_used_bytes=%d, want %d", used, size)
	}
}

// TestDeleteMessage_WebmailPurgeFromTrashReclaims pins the webmail second
// delete: deleting a message already in Trash hard-deletes it and reclaims
// quota.
func TestDeleteMessage_WebmailPurgeFromTrashReclaims(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	const size = 3072
	fx := seedMsgDeleteFixture(t, tx, size)
	msg := models.Message{
		MailboxID: fx.mailbox.ID,
		Folder:    "Trash",
		Sender:    "someone@remote.test",
		SizeBytes: size,
	}
	if err := tx.Create(&msg).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}

	h := NewMessageHandler(tx, nil, nil, "", nil)
	idStr := uintToStr(msg.ID)
	rr := httptest.NewRecorder()
	req := authzRequest(http.MethodDelete,
		"/api/v1/messages/"+idStr,
		fx.account.ID, map[string]string{"id": idStr}, "")
	h.DeleteMessage(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("purge from trash: expected 204, got %d (%s)", rr.Code, rr.Body.String())
	}
	if n := countMessages(t, tx, fx.mailbox.ID); n != 0 {
		t.Fatalf("purge from trash must remove the row, count=%d", n)
	}
	if used := quotaUsedBytes(t, tx, fx.mailbox.ID); used != 0 {
		t.Fatalf("purge from trash must reclaim quota: quota_used_bytes=%d, want 0", used)
	}
}
