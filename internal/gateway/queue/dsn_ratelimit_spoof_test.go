package queue

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envIntOr(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// openDSNTestDB connects to the unit-test Postgres and migrates the messages
// table. It skips (never fails) when no database is reachable, matching the
// repo's depless-local / DB-in-CI convention.
func openDSNTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("DSN rate-limit test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Message{}); err != nil {
		t.Skipf("DSN rate-limit test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// TestRecentDSNCount_IgnoresSpoofedInboundMail is the red-green guard for the
// "DSN rate-limit spoof" backlog item. The per-recipient DSN counter must count
// ONLY locally generated DSNs (is_dsn = true), not inbound mail that merely
// carries a mailer-daemon@<hostname> envelope-from. If it counted the latter, an
// external sender could inflate a target mailbox's counter and suppress that
// user's legitimate bounces.
//
// The test seeds one genuine DSN and several spoofed inbound rows (same sender
// address, is_dsn = false) into the same mailbox, then asserts recentDSNCount
// returns only the genuine one. Against the pre-fix sender-based query it would
// return all of them.
func TestRecentDSNCount_IgnoresSpoofedInboundMail(t *testing.T) {
	gdb := openDSNTestDB(t)

	const hostname = "mail.spooftest.invalid"
	// A mailbox id unlikely to collide with other rows in a shared test DB.
	mailboxID := uint(900000000 + time.Now().UnixNano()%100000000)
	now := time.Now()

	insert := func(isDSN bool) {
		row := map[string]interface{}{
			"mailbox_id":    mailboxID,
			"folder":        "INBOX",
			"sender":        "mailer-daemon@" + hostname,
			"sender_name":   "Mail Delivery System",
			"recipients_to": `["victim@` + hostname + `"]`,
			"recipients_cc": "[]",
			"subject":       "test",
			"raw_message":   "raw",
			"is_read":       false,
			"is_dsn":        isDSN,
			"size_bytes":    3,
			"received_at":   now,
			"created_at":    now,
			"updated_at":    now,
		}
		if err := gdb.Table("messages").Create(row).Error; err != nil {
			t.Fatalf("insert row (is_dsn=%v): %v", isDSN, err)
		}
	}
	t.Cleanup(func() {
		gdb.Exec("DELETE FROM messages WHERE mailbox_id = ?", mailboxID)
	})

	insert(true)          // one genuine, locally generated DSN
	for i := 0; i < 5; i++ {
		insert(false) // five spoofed inbound messages claiming mailer-daemon@
	}

	w := &Worker{db: gdb, hostname: hostname, bounceWindow: time.Hour}
	got := w.recentDSNCount(mailboxID)
	if got != 1 {
		t.Fatalf("recentDSNCount = %d, want 1 (must count only locally generated DSNs, not spoofed inbound mail)", got)
	}
}
