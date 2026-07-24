package smtp

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func dsEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func dsEnvIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// openStoreTestDB connects to the unit-test Postgres and migrates the tables the
// dbStore touches, skipping (never failing) when none is reachable — matching
// the repo's DB-in-CI convention.
func openStoreTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: dsEnvOr("DB_HOST", "localhost"),
		DBPort: dsEnvIntOr("DB_PORT", 5432),
		DBName: dsEnvOr("DB_NAME", "restmail"),
		DBUser: dsEnvOr("DB_USER", "restmail"),
		DBPass: dsEnvOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("smtp dbStore test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.Message{}, &models.OutboundQueue{}); err != nil {
		t.Skipf("smtp dbStore test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// TestDBStore_PersistSubmittedMessageLinksQueue proves the DSN-provenance wiring
// end to end at the storage layer: a submission is persisted as a "Sent" message
// owned by the sender's mailbox, and an outbound queue row enqueued with that
// reference id carries the link in OutboundQueue.MessageID — the column the
// bounce/DSN sender-auth check reads.
func TestDBStore_PersistSubmittedMessageLinksQueue(t *testing.T) {
	db := openStoreTestDB(t)

	suffix := time.Now().UnixNano()
	domainName := fmt.Sprintf("dsn-prov-%d.test", suffix)
	senderAddr := "alice@" + domainName

	dom := models.Domain{Name: domainName, Active: true}
	if err := db.Create(&dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	sender := models.Mailbox{DomainID: dom.ID, LocalPart: "alice", Address: senderAddr, Password: "x", Active: true, QuotaBytes: 1 << 30}
	if err := db.Create(&sender).Error; err != nil {
		t.Fatalf("create sender mailbox: %v", err)
	}
	t.Cleanup(func() {
		db.Where("sender = ?", senderAddr).Delete(&models.OutboundQueue{})
		db.Where("mailbox_id = ?", sender.ID).Delete(&models.Message{})
		db.Where("id = ?", sender.ID).Delete(&models.Mailbox{})
		db.Where("id = ?", dom.ID).Delete(&models.Domain{})
	})

	store := NewStore(db)

	ref, err := store.PersistSubmittedMessage(SubmittedMessage{
		Sender:     senderAddr,
		MessageID:  "<msg-abc@" + domainName + ">",
		Subject:    "Hello",
		BodyText:   "body",
		RawMessage: "raw bytes",
	})
	if err != nil {
		t.Fatalf("PersistSubmittedMessage: %v", err)
	}
	if ref == nil {
		t.Fatalf("PersistSubmittedMessage returned nil id for a local sender")
	}

	// The persisted row must be a Sent message owned by the sender's mailbox.
	var stored models.Message
	if err := db.First(&stored, *ref).Error; err != nil {
		t.Fatalf("load persisted message %d: %v", *ref, err)
	}
	if stored.MailboxID != sender.ID {
		t.Errorf("persisted message mailbox_id = %d, want sender %d", stored.MailboxID, sender.ID)
	}
	if stored.Folder != "Sent" {
		t.Errorf("persisted message folder = %q, want Sent", stored.Folder)
	}

	// An outbound row enqueued with the reference must carry the link.
	if err := store.EnqueueOutbound(OutboundMessage{
		Sender:     senderAddr,
		Recipient:  "carol@remote.test",
		Domain:     "remote.test",
		RawMessage: "raw bytes",
		MessageID:  ref,
	}); err != nil {
		t.Fatalf("EnqueueOutbound: %v", err)
	}
	var q models.OutboundQueue
	if err := db.Where("sender = ?", senderAddr).First(&q).Error; err != nil {
		t.Fatalf("load outbound queue row: %v", err)
	}
	if q.MessageID == nil {
		t.Fatalf("outbound queue MessageID is nil; want %d", *ref)
	}
	if *q.MessageID != *ref {
		t.Errorf("outbound queue MessageID = %d, want %d", *q.MessageID, *ref)
	}
}

// TestDBStore_PersistSubmittedMessageNoSenderMailbox confirms that when the
// sender has no local mailbox there is nothing to attribute the message to, so
// PersistSubmittedMessage returns (nil, nil) rather than erroring — delivery
// proceeds without a linked reference.
func TestDBStore_PersistSubmittedMessageNoSenderMailbox(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)

	ref, err := store.PersistSubmittedMessage(SubmittedMessage{
		Sender:    fmt.Sprintf("nobody-%d@nowhere.test", time.Now().UnixNano()),
		MessageID: "<x@nowhere.test>",
	})
	if err != nil {
		t.Fatalf("PersistSubmittedMessage (no mailbox) returned error: %v", err)
	}
	if ref != nil {
		t.Errorf("PersistSubmittedMessage (no mailbox) returned id %d, want nil", *ref)
	}
}
