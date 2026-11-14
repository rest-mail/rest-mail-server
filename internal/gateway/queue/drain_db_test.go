package queue

import (
	"strconv"
	"strings"
	"testing"
	"time"

	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// waitForCopies waits until the peer has taken want messages, and reports whether
// it did before the deadline.
func waitForCopies(mx *timedMX, want int, deadline time.Duration) bool {
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		if mx.copiesReceived() >= want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return mx.copiesReceived() >= want
}

// A backlog goes as fast as it can be delivered, not one message per poll (#1583).
// One worker has eight messages due, polling every two seconds: taken one a tick
// they would need sixteen seconds, so all of them within four polls is only
// possible if the worker keeps going until none is left. This is the real loop
// over a real database and a real SMTP conversation, with the reset barrier a
// non-production gateway runs under as well as without it.
func TestBacklogIsDeliveredWithoutWaitingForAPollPerMessage(t *testing.T) {
	for _, testReset := range []bool{false, true} {
		t.Run("testReset="+strconv.FormatBool(testReset), func(t *testing.T) {
			db := openResetWorkerDB(t)
			migrateDeliveryTables(t, db)
			_, sender := sendingDomain(t, db)
			mx := newTimedMX(t, timedMXOptions{})
			const backlog = 8
			rows := make([]models.OutboundQueue, backlog)
			for i := range rows {
				rows[i] = queueTimedRow(t, db, sender.Address, nil)
			}

			w := claimingWorker(t, db, mx, testReset)
			w.pollInterval = 2 * time.Second
			w.Start()

			delivered := waitForCopies(mx, backlog, 4*w.pollInterval)
			w.Shutdown()
			if !delivered {
				t.Fatalf("%d of %d messages delivered after 4 polls of one worker; one a poll would be 4", mx.copiesReceived(), backlog)
			}
			for _, row := range rows {
				if got := readRow(t, db, row.ID); got.Status != "delivered" || got.Attempts != 1 {
					t.Errorf("message %d: status %q after %d attempts, want delivered after 1", row.ID, got.Status, got.Attempts)
				}
			}
		})
	}
}

// Working through a backlog must not become a tight loop over a message that keeps
// failing: its attempt defers it, so it is not due, and the worker goes idle.
func TestDrainTriesAFailingMessageOnce(t *testing.T) {
	db := openResetWorkerDB(t)
	migrateDeliveryTables(t, db)
	_, sender := sendingDomain(t, db)
	mx := newTimedMX(t, timedMXOptions{})
	row := queueTimedRow(t, db, sender.Address, nil)

	w := claimingWorker(t, db, mx, false)
	w.smtpAddr = func(string) string { return "127.0.0.1:1" } // nothing listens there
	w.drain(0)

	got := readRow(t, db, row.ID)
	if got.Status != "deferred" || got.Attempts != 1 {
		t.Fatalf("status %q after %d attempts, want deferred after 1", got.Status, got.Attempts)
	}
	if !got.NextAttempt.After(time.Now()) {
		t.Fatalf("next attempt %s is already due", got.NextAttempt)
	}
}

// Shutdown does not wait for a backlog: the worker stops after the message in hand.
func TestShutdownStopsWorkingThroughABacklog(t *testing.T) {
	db := openResetWorkerDB(t)
	migrateDeliveryTables(t, db)
	_, sender := sendingDomain(t, db)
	mx := newTimedMX(t, timedMXOptions{delay: map[string]time.Duration{"final": 100 * time.Millisecond}})
	const backlog = 50
	for range backlog {
		queueTimedRow(t, db, sender.Address, nil)
	}

	w := claimingWorker(t, db, mx, false)
	w.pollInterval = 50 * time.Millisecond
	w.Start()
	if !waitForCopies(mx, 1, 30*time.Second) {
		w.Shutdown()
		t.Fatal("nothing was delivered")
	}
	w.Shutdown()

	var left int64
	if err := db.Model(&models.OutboundQueue{}).Where("status = ?", "pending").Count(&left).Error; err != nil {
		t.Fatal(err)
	}
	if left == 0 {
		t.Fatalf("all %d messages were sent during Shutdown", backlog)
	}
	var mid int64
	if err := db.Model(&models.OutboundQueue{}).Where("status = ?", "delivering").Count(&mid).Error; err != nil {
		t.Fatal(err)
	}
	if mid != 0 {
		t.Fatalf("%d messages left delivering after Shutdown", mid)
	}
}

// A claim does not get slower with every message that has ever left (#1583). They
// stay in the table with the oldest next_attempt there is, so with a backlog due,
// when a claim matters most, the plain next_attempt index looks like the quick way
// to the oldest row, and the claim walks past all of them to reach it. The schema
// here is the real migration.
func TestClaimDoesNotWalkPastMailThatHasLeft(t *testing.T) {
	db := openResetWorkerDB(t)
	if err := rmdb.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO outbound_queue (sender, recipient, domain, raw_message, status, next_attempt, expires_at, max_retries)
		SELECT 'queued@sender.test', 'someone@remote.test', 'remote.test', 'x',
		       CASE WHEN g <= 20000 THEN 'pending' WHEN g % 2 = 0 THEN 'delivered' ELSE 'bounced' END,
		       CASE WHEN g <= 20000 THEN now() - interval '1 minute' + g * interval '1 millisecond' ELSE now() - interval '30 days' + g * interval '1 second' END,
		       now() + interval '1 day', 10
		  FROM generate_series(1, 120000) g`).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	queued := func(status string, due time.Time, lastAttempt *time.Time) models.OutboundQueue {
		t.Helper()
		item := models.OutboundQueue{Sender: "queued@sender.test", Recipient: "someone@remote.test", Domain: "remote.test",
			RawMessage: "x", Status: status, NextAttempt: due, LastAttempt: lastAttempt, ExpiresAt: now.Add(time.Hour), MaxRetries: 10}
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
		return item
	}
	stale := now.Add(-time.Hour)
	crashed := queued("delivering", now.Add(-3*time.Minute), &stale)
	oldest := queued("pending", now.Add(-2*time.Minute), nil)
	if err := db.Exec("ANALYZE outbound_queue").Error; err != nil {
		t.Fatal(err)
	}

	var plan []string
	if err := db.Raw("EXPLAIN (ANALYZE) "+claimSQL, now, now.Add(-time.Minute), now).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	for _, line := range plan {
		if _, removed, ok := strings.Cut(line, "Rows Removed by Filter: "); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(removed)); err != nil || n > 1000 {
				t.Errorf("the claim looked at %s rows it could not take:\n%s", removed, strings.Join(plan, "\n"))
			}
		}
	}

	// It still takes the oldest due first, and a crashed worker's row among them.
	w := NewWorker(db, "sender.test", 1, time.Second)
	for _, want := range []uint{crashed.ID, oldest.ID} {
		item, err := w.claim(now)
		if err != nil || item.ID != want {
			t.Fatalf("claimed %d (%v), want %d", item.ID, err, want)
		}
	}
}
