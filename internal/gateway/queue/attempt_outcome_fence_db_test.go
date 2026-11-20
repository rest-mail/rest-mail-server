package queue

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// operatorBounce writes what POST /api/v1/admin/queue/{id}/bounce and bulk-bounce
// write: the handler lives in the API process, which this package cannot import,
// so the test repeats its statement (internal/api/handlers/queue.go, queueBounce).
func operatorBounce(t *testing.T, db *gorm.DB, id uint) {
	t.Helper()
	result := db.Model(&models.OutboundQueue{}).
		Where("id = ? AND status IN ?", id, []string{"pending", "deferred", "delivering"}).
		Updates(map[string]interface{}{"status": "bounced", "last_error": "manually bounced by admin"})
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("operator bounce: %v, %d rows", result.Error, result.RowsAffected)
	}
}

// dsnsFor counts the notices delivered into a local sender's mailbox.
func dsnsFor(t *testing.T, db *gorm.DB, mailboxID uint) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.Message{}).Where("mailbox_id = ? AND is_dsn", mailboxID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func suppressionsFor(t *testing.T, db *gorm.DB, domainID uint) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.Suppression{}).Where("domain_id = ?", domainID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// startHeldAttempt has worker claim the one due message and run its attempt in the
// background until the peer reaches its hold step. The returned channel closes
// when the attempt has recorded its outcome.
func startHeldAttempt(t *testing.T, w *Worker, mx *timedMX) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if !w.processOne(0) {
			t.Error("the worker found nothing due")
		}
	}()
	waitUntil(t, "the attempt reaches the peer's hold step", func() bool { return mx.stalledSessions() >= 1 })
	return done
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the attempt did not finish")
	}
}

// An operator who bounces a message while it is being delivered takes the row from
// that attempt (#2105 queue AC05, #1950's delivering-row race). The attempt cannot
// stop what is already on the wire, so a delivery that then succeeds is recorded as
// delivered: the receiver has the message, and "bounced" would say otherwise. A
// failed attempt records nothing: before #2105 a transient failure wrote "deferred"
// over the operator's bounce, so the message the operator gave up on was tried
// again, and a permanent one replaced the operator's record and sent its own notice
// beside whatever the manual bounce does. This is the real worker over a real SMTP
// session that is held at RCPT while the operator acts.
func TestOperatorBounceOfADeliveringRowIsNotUndoneByItsAttempt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rcptReply  string // empty: the peer accepts and the message is delivered
		wantStatus string
		wantError  string
	}{
		{name: "a transient failure leaves it bounced", rcptReply: "451 4.3.0 try again later",
			wantStatus: "bounced", wantError: "manually bounced by admin"},
		{name: "a permanent failure leaves the operator's record and adds no notice", rcptReply: "550 5.1.1 no such user",
			wantStatus: "bounced", wantError: "manually bounced by admin"},
		{name: "a delivery that succeeds is recorded as delivered", wantStatus: "delivered", wantError: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openResetWorkerDB(t)
			migrateDeliveryTables(t, db)
			domain, sender := sendingDomain(t, db)
			opts := timedMXOptions{hold: "RCPT"}
			if tc.rcptReply != "" {
				opts.replies = map[string]string{"RCPT": tc.rcptReply}
			}
			mx := newTimedMX(t, opts)
			w := claimingWorker(t, db, mx, false)
			item := queueTimedRow(t, db, sender.Address, nil)

			done := startHeldAttempt(t, w, mx)
			if got := readRow(t, db, item.ID); got.Status != "delivering" || got.Attempts != 1 {
				t.Fatalf("held attempt: status %q, attempts %d; want delivering, 1", got.Status, got.Attempts)
			}
			operatorBounce(t, db, item.ID)
			mx.releaseHold()
			waitDone(t, done)

			got := readRow(t, db, item.ID)
			if got.Status != tc.wantStatus || got.Attempts != 1 {
				t.Errorf("status %q after %d attempts, want %q after 1", got.Status, got.Attempts, tc.wantStatus)
			}
			if tc.wantError != "" && got.LastError != tc.wantError {
				t.Errorf("last error %q, want the operator's %q", got.LastError, tc.wantError)
			}
			if n := dsnsFor(t, db, sender.ID); n != 0 {
				t.Errorf("the attempt delivered %d notices to the sender of a message the operator had taken over", n)
			}
			if n := suppressionsFor(t, db, domain.ID); n != 0 {
				t.Errorf("the attempt recorded %d suppressions for a message the operator had taken over", n)
			}
			if tc.wantStatus == "delivered" && mx.copiesReceived() != 1 {
				t.Errorf("the peer has %d copies, want 1", mx.copiesReceived())
			}
		})
	}
}

// A worker whose attempt outlives its claim, because its heartbeat stopped and
// another worker reclaimed the row, cannot overwrite what the newer claim recorded
// (#2105 queue AC01: retrying a send cannot duplicate its durable effects). Before
// #2105 the stale attempt's transient failure turned the newer claim's "delivered"
// back into "deferred", so the message was sent again, and its permanent failure
// marked a delivered message bounced, told the sender it had failed and suppressed
// the recipient. Both workers are real, each with its own peer.
func TestAStaleAttemptCannotOverwriteANewerClaim(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rcptReply string
	}{
		{name: "a transient failure", rcptReply: "451 4.3.0 try again later"},
		{name: "a permanent failure", rcptReply: "550 5.1.1 no such user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openResetWorkerDB(t)
			migrateDeliveryTables(t, db)
			domain, sender := sendingDomain(t, db)
			held := newTimedMX(t, timedMXOptions{hold: "RCPT", replies: map[string]string{"RCPT": tc.rcptReply}})
			fast := newTimedMX(t, timedMXOptions{})

			stale := claimingWorker(t, db, held, false)
			stale.heartbeatEvery = time.Hour // the heartbeat that would have kept the claim never beats
			rival := claimingWorker(t, db, fast, false)
			rival.reclaim = 50 * time.Millisecond
			item := queueTimedRow(t, db, sender.Address, nil)

			done := startHeldAttempt(t, stale, held)
			time.Sleep(4 * rival.reclaim)
			if !rival.processOne(0) {
				t.Fatal("the rival worker did not reclaim the row")
			}
			if got := readRow(t, db, item.ID); got.Status != "delivered" || got.Attempts != 2 {
				t.Fatalf("after the reclaim: status %q, attempts %d; want delivered, 2", got.Status, got.Attempts)
			}
			held.releaseHold()
			waitDone(t, done)

			got := readRow(t, db, item.ID)
			if got.Status != "delivered" || got.Attempts != 2 || got.LastError != "" {
				t.Errorf("the stale attempt overwrote the newer claim: status %q, attempts %d, error %q; want delivered, 2, none",
					got.Status, got.Attempts, got.LastError)
			}
			if n := dsnsFor(t, db, sender.ID); n != 0 {
				t.Errorf("the sender of a delivered message got %d failure notices", n)
			}
			if n := suppressionsFor(t, db, domain.ID); n != 0 {
				t.Errorf("a recipient that took the message was suppressed (%d rows)", n)
			}
			if c := fast.copiesReceived(); c != 1 {
				t.Errorf("the reclaiming delivery left %d copies, want 1", c)
			}
		})
	}
}

// A suppressed recipient's bounce is the same kind of outcome, and a claim that no
// longer holds its row cannot write it either: no "bounced" over a newer delivery,
// no attempt count taken off the newer claim's, and no notice to the sender.
func TestAStaleSuppressedBounceCannotOverwriteANewerClaim(t *testing.T) {
	db := openResetWorkerDB(t)
	_, sender := sendingDomain(t, db)
	w := NewWorker(db, "sender.test", 1, time.Second)
	item := queueTimedRow(t, db, sender.Address, func(item *models.OutboundQueue) {
		item.Status, item.Attempts = "delivering", 1
	})
	if err := db.Model(&models.OutboundQueue{}).Where("id = ?", item.ID).
		Updates(map[string]interface{}{"status": "delivered", "attempts": 2}).Error; err != nil {
		t.Fatal(err)
	}
	w.bounceSuppressed(context.Background(), item, models.Suppression{
		FailureCategory: models.FailureBadMailbox, ExpiresAt: time.Now().Add(time.Hour),
	})
	got := readRow(t, db, item.ID)
	if got.Status != "delivered" || got.Attempts != 2 || strings.Contains(got.LastError, "suppressed") {
		t.Errorf("status %q, attempts %d, error %q; want delivered, 2, none", got.Status, got.Attempts, got.LastError)
	}
	if n := dsnsFor(t, db, sender.ID); n != 0 {
		t.Errorf("the sender of a delivered message got %d notices", n)
	}
}
