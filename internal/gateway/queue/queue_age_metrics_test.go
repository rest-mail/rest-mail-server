package queue

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/metrics"
	"gorm.io/gorm"
)

// TestOldestAge: no waiting row is age 0, and a database clock ahead of this
// process's never makes the age negative.
func TestOldestAge(t *testing.T) {
	now := time.Now()
	if got := oldestAge(nil, now); got != 0 {
		t.Errorf("oldestAge(nil) = %v, want 0", got)
	}
	old := now.Add(-90 * time.Minute)
	if got := oldestAge(&old, now); got != 5400 {
		t.Errorf("oldestAge(90m ago) = %v, want 5400", got)
	}
	ahead := now.Add(time.Second)
	if got := oldestAge(&ahead, now); got != 0 {
		t.Errorf("oldestAge(in the future) = %v, want 0", got)
	}
}

// addQueuedAt inserts one outbound row with the given status, created at.
func addQueuedAt(t *testing.T, tx *gorm.DB, status string, created, now time.Time) {
	t.Helper()
	row := models.OutboundQueue{
		Sender: "sender@local.test", Recipient: "someone@remote.test", Domain: "remote.test",
		RawMessage: "Subject: hi\r\n\r\nhi\r\n", Status: status, MaxRetries: models.DefaultMaxRetries,
		NextAttempt: now, ExpiresAt: now.Add(models.QueueLifetime), CreatedAt: created,
	}
	if err := tx.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}

// TestRefreshQueueSize_QueueOldestMessageOld: restmail_queue_oldest_age_seconds
// is the age of the oldest pending or deferred row, so QueueOldestMessageOld
// (restmail_queue_oldest_age_seconds > 6 * 3600) fires for a message waiting
// seven hours. Rows being sent or already gone do not count, however old.
func TestRefreshQueueSize_QueueOldestMessageOld(t *testing.T) {
	gdb := openFailureTestDB(t)
	tx := snapshotTx(t, gdb)
	now := time.Now()
	// Other tests' waiting rows: clear them in this snapshot, which is
	// rolled back, so the age is this test's alone.
	if err := tx.Exec(`DELETE FROM outbound_queue`).Error; err != nil {
		t.Fatal(err)
	}

	age := func() float64 {
		t.Helper()
		if err := refreshQueueSize(tx, now); err != nil {
			t.Fatal(err)
		}
		return testutil.ToFloat64(metrics.QueueOldestAge)
	}
	if got := age(); got != 0 {
		t.Fatalf("restmail_queue_oldest_age_seconds = %v for an empty queue, want 0", got)
	}

	addQueuedAt(t, tx, "delivering", now.Add(-48*time.Hour), now)
	addQueuedAt(t, tx, "delivered", now.Add(-72*time.Hour), now)
	addQueuedAt(t, tx, "bounced", now.Add(-72*time.Hour), now)
	if got := age(); got != 0 {
		t.Fatalf("restmail_queue_oldest_age_seconds = %v with only rows being sent or gone, want 0", got)
	}

	addQueuedAt(t, tx, "pending", now.Add(-10*time.Minute), now)
	if got := age(); got < 599 || got > 601 {
		t.Fatalf("restmail_queue_oldest_age_seconds = %v, want 600 (a pending row 10 minutes old)", got)
	}
	if got := age(); got > 6*3600 {
		t.Errorf("QueueOldestMessageOld would fire for a 10-minute-old queue (%v s)", got)
	}

	addQueuedAt(t, tx, "deferred", now.Add(-7*time.Hour), now)
	got := age()
	if got < 7*3600-1 || got > 7*3600+1 {
		t.Fatalf("restmail_queue_oldest_age_seconds = %v, want %v (a deferred row 7 hours old)", got, 7*3600)
	}
	if got <= 6*3600 {
		t.Errorf("QueueOldestMessageOld would not fire for a message waiting 7 hours (%v s)", got)
	}
}
