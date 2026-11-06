package smtp

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
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
		testdb.Unreachable(t, "smtp dbStore test skipped: no database reachable (%v)", err)
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
	sender := models.Mailbox{DomainID: dom.ID, LocalPart: "alice", Address: senderAddr, Active: true, QuotaBytes: 1 << 30}
	if err := db.Create(&sender).Error; err != nil {
		t.Fatalf("create sender mailbox: %v", err)
	}
	t.Cleanup(func() {
		db.Where("sender = ?", senderAddr).Delete(&models.OutboundQueue{})
		db.Where("mailbox_id = ?", sender.ID).Delete(&models.Message{})
		db.Where("id = ?", sender.ID).Delete(&models.Mailbox{})
		db.Where("id = ?", dom.ID).Delete(&models.Domain{})
	})

	store := NewStore(db, "")

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

	// A local recipient that refused the submission is kept on the persisted row.
	if err := store.RecordLocalRefusals(*ref, []models.LocalRefusal{{Address: "bob@example.test", Category: models.FailureMailboxFull}}); err != nil {
		t.Fatalf("RecordLocalRefusals: %v", err)
	}
	if err := db.First(&stored, *ref).Error; err != nil {
		t.Fatalf("reload persisted message %d: %v", *ref, err)
	}
	if want := `"category": "mailbox_full"`; !strings.Contains(string(stored.LocalRefusals), want) {
		t.Errorf("persisted refusals = %s, want %s", stored.LocalRefusals, want)
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
	store := NewStore(db, "")

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

// TestDBStore_RelayBounce: a bounce to an SRS address becomes a queue row like the
// DSN #715 queues for a sender outside: a null reverse path, marked as a DSN, sent
// as the forwarding domain, and held to the per-recipient limit in any spelling.
func TestDBStore_RelayBounce(t *testing.T) {
	gdb := openStoreTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	store := NewStore(tx, "")

	recipient := fmt.Sprintf("alice-%d@orig.example", time.Now().UnixNano())
	msg := BounceRelay{
		// A next hop may have changed the case of the address the domain comes from.
		Recipient: recipient, SendingDomain: "FORWARD.Example",
		RawMessage: "Subject: Undelivered\r\n\r\nNo such user.\r\n", BodyType: "8BITMIME",
		MaxPerRecipient: 2, Window: time.Hour,
	}
	if err := store.RelayBounce(msg); err != nil {
		t.Fatal(err)
	}
	var rows []models.OutboundQueue
	if err := tx.Where("recipient = ?", recipient).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("queued %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.Sender != "" || !row.IsDSN || row.SendingDomain != "forward.example" || row.Domain != "orig.example" ||
		row.Status != "pending" || row.RawMessage != msg.RawMessage || row.BodyType != "8BITMIME" ||
		row.MaxRetries != models.DefaultMaxRetries || !row.ExpiresAt.After(time.Now()) {
		t.Fatalf("row = %+v", row)
	}

	// An address is valid in any case, so another spelling is the same recipient.
	upper := msg
	upper.Recipient = strings.ToUpper(recipient)
	for i := 0; i < 3; i++ {
		if err := store.RelayBounce(upper); err != nil {
			t.Fatal(err)
		}
	}
	var n int64
	if err := tx.Model(&models.OutboundQueue{}).Where("LOWER(recipient) = ?", strings.ToLower(recipient)).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("queued %d rows, want the limit of 2", n)
	}

	// No limit configured means none.
	unlimited := msg
	unlimited.MaxPerRecipient = 0
	if err := store.RelayBounce(unlimited); err != nil {
		t.Fatal(err)
	}
	if err := tx.Model(&models.OutboundQueue{}).Where("LOWER(recipient) = ?", strings.ToLower(recipient)).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("queued %d rows with no limit, want 3", n)
	}
}

// TestDBStore_HostedDomain finds a hosted domain in any spelling, Unicode included,
// whether or not it is in service, and answers no for anything else.
func TestDBStore_HostedDomain(t *testing.T) {
	gdb := openStoreTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	stamp := time.Now().UnixNano()
	plain := fmt.Sprintf("hosted-%d.test", stamp)
	idn := fmt.Sprintf("xn--bcher-kva.hosted-%d.test", stamp)
	for _, name := range []string{plain, idn} {
		if err := tx.Create(&models.Domain{Name: name}).Error; err != nil {
			t.Fatal(err)
		}
	}
	store := NewStore(tx, "")
	for domain, want := range map[string]bool{
		plain:                  true,
		strings.ToUpper(plain): true,
		idn:                    true,
		fmt.Sprintf("bücher.hosted-%d.test", stamp): true,
		"elsewhere.example":                         false,
		"":                                          false,
		"not a domain":                              false,
	} {
		got, err := store.HostedDomain(domain)
		if err != nil {
			t.Errorf("HostedDomain(%q): %v", domain, err)
		}
		if got != want {
			t.Errorf("HostedDomain(%q) = %v, want %v", domain, got, want)
		}
	}
}
