package queue

import (
	"fmt"
	"net/textproto"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func openFailureTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("failure-handling DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.OutboundQueue{}); err != nil {
		t.Skipf("failure-handling DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// queued puts a message on the queue as the claim leaves it: attempts counts
// the attempt now failing. Its sender has no mailbox here, so a bounce sends no
// DSN and the test reads only the row.
func queued(t *testing.T, gdb *gorm.DB, attempts int, age time.Duration, now time.Time) models.OutboundQueue {
	t.Helper()
	item := models.OutboundQueue{
		Sender:    fmt.Sprintf("sender-%d@nowhere.invalid", now.UnixNano()),
		Recipient: "someone@remote.test", Domain: "remote.test", RawMessage: "Subject: hi\r\n\r\nhi\r\n",
		Status: "delivering", Attempts: attempts, MaxRetries: models.DefaultMaxRetries,
		ExpiresAt: now.Add(models.QueueLifetime - age),
	}
	if err := gdb.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gdb.Delete(&models.OutboundQueue{}, item.ID) })
	// CreatedAt is set on create; move it back to the message's age.
	item.CreatedAt = now.Add(-age)
	if err := gdb.Model(&item).Update("created_at", item.CreatedAt).Error; err != nil {
		t.Fatal(err)
	}
	return item
}

func rcptRefusal(code int, msg string) error {
	return fmt.Errorf("all MX hosts failed: %w", error(parseSMTPError(&textproto.Error{Code: code, Msg: msg})))
}

func TestRecordFailure(t *testing.T) {
	gdb := openFailureTestDB(t)
	w := NewWorker(gdb, "mail.local.test", 1, time.Second)
	now := time.Now()
	reload := func(item models.OutboundQueue) models.OutboundQueue {
		t.Helper()
		var got models.OutboundQueue
		if err := gdb.First(&got, item.ID).Error; err != nil {
			t.Fatal(err)
		}
		return got
	}

	t.Run("a permanent failure bounces at once", func(t *testing.T) {
		item := queued(t, gdb, 1, time.Minute, now)
		w.recordFailure(item, rcptRefusal(550, "5.1.1 The email account that you tried to reach does not exist."), now)
		got := reload(item)
		if got.Status != "bounced" || got.FailureCategory != models.FailureBadMailbox || got.LastErrorCode != 550 {
			t.Errorf("status %q, category %q, code %d; want bounced, bad_mailbox, 550", got.Status, got.FailureCategory, got.LastErrorCode)
		}
	})
	t.Run("a transient failure is deferred on its category's schedule", func(t *testing.T) {
		item := queued(t, gdb, 1, time.Minute, now)
		w.recordFailure(item, rcptRefusal(452, "4.2.2 The recipient's inbox is out of storage space."), now)
		got := reload(item)
		if got.Status != "deferred" || got.FailureCategory != models.FailureMailboxFull {
			t.Fatalf("status %q, category %q; want deferred, mailbox_full", got.Status, got.FailureCategory)
		}
		if d := got.NextAttempt.Sub(now); d < 48*time.Minute || d > 72*time.Minute {
			t.Errorf("next attempt in %s, want an hour ± 20%%", d)
		}
	})
	t.Run("a transient failure bounces once its category's maximum age is up", func(t *testing.T) {
		item := queued(t, gdb, 6, 116*time.Hour, now) // 8h to the next try, past five days
		w.recordFailure(item, rcptRefusal(452, "4.2.2 The recipient's inbox is out of storage space."), now)
		if got := reload(item); got.Status != "bounced" || got.FailureCategory != models.FailureMailboxFull {
			t.Errorf("status %q, category %q; want bounced, mailbox_full", got.Status, got.FailureCategory)
		}
	})
}
