package queue

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/metrics"
	"go.yaml.in/yaml/v2"
	"gorm.io/gorm"
)

// snapshotTx opens one repeatable-read snapshot for a test, so rows other
// tests commit meanwhile do not move the counts under it; nothing in it is
// committed.
func snapshotTx(t *testing.T, gdb *gorm.DB) *gorm.DB {
	t.Helper()
	tx := gdb.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

// addQueued inserts n outbound rows with the given status and next attempt.
func addQueued(t *testing.T, tx *gorm.DB, status string, n int, next, now time.Time) {
	t.Helper()
	rows := make([]models.OutboundQueue, n)
	for i := range rows {
		rows[i] = models.OutboundQueue{
			Sender: "sender@local.test", Recipient: "someone@remote.test", Domain: "remote.test",
			RawMessage: "Subject: hi\r\n\r\nhi\r\n", Status: status, MaxRetries: models.DefaultMaxRetries,
			NextAttempt: next, ExpiresAt: now.Add(models.QueueLifetime),
		}
	}
	if err := tx.CreateInBatches(&rows, 500).Error; err != nil {
		t.Fatal(err)
	}
}

// queueGauges recounts the queue at now and returns restmail_queue_size and
// restmail_queue_due.
func queueGauges(t *testing.T, tx *gorm.DB, now time.Time) (size, due float64) {
	t.Helper()
	if err := refreshQueueSize(tx, now); err != nil {
		t.Fatal(err)
	}
	return testutil.ToFloat64(metrics.QueueSize), testutil.ToFloat64(metrics.QueueDue)
}

// TestRefreshQueueSize_QueueBacklogHigh: restmail_queue_size counts every row
// still waiting to leave (pending, deferred, delivering) and none that has
// left, so QueueBacklogHigh (restmail_queue_size > 1000) sees the queue grow.
func TestRefreshQueueSize_QueueBacklogHigh(t *testing.T) {
	gdb := openFailureTestDB(t)
	tx := snapshotTx(t, gdb)
	now := time.Now()
	before, _ := queueGauges(t, tx, now)

	addQueued(t, tx, "pending", 900, now, now)
	addQueued(t, tx, "deferred", 80, now, now)
	addQueued(t, tx, "delivering", 21, now, now)
	// History is not backlog.
	addQueued(t, tx, "delivered", 50, now, now)
	addQueued(t, tx, "bounced", 50, now, now)

	got, _ := queueGauges(t, tx, now)
	if got != before+1001 {
		t.Fatalf("restmail_queue_size = %v, want %v (%v before, plus 1001 waiting rows)", got, before+1001, before)
	}
	if got <= 1000 {
		t.Errorf("restmail_queue_size = %v after 1001 waiting rows; QueueBacklogHigh would not fire", got)
	}
}

// TestRefreshQueueSize_QueueDue: restmail_queue_due counts only the rows a
// worker should be taking now — pending or deferred rows whose next attempt
// has come, and rows being sent — while restmail_queue_size counts every row
// still waiting. A deferred row whose retry is hours away is in the size and
// not in the due count; history is in neither.
func TestRefreshQueueSize_QueueDue(t *testing.T) {
	gdb := openFailureTestDB(t)
	tx := snapshotTx(t, gdb)
	now := time.Now()
	sizeBefore, dueBefore := queueGauges(t, tx, now)

	addQueued(t, tx, "pending", 2, now.Add(-time.Minute), now)
	addQueued(t, tx, "deferred", 3, now, now)
	addQueued(t, tx, "delivering", 1, now.Add(-time.Hour), now)
	// Waiting for a retry after a remote outage: queued, but not due.
	addQueued(t, tx, "deferred", 4, now.Add(6*time.Hour), now)
	// History is neither, however old its next attempt.
	addQueued(t, tx, "delivered", 2, now.Add(-time.Hour), now)
	addQueued(t, tx, "bounced", 2, now.Add(-time.Hour), now)

	size, due := queueGauges(t, tx, now)
	if size != sizeBefore+10 {
		t.Errorf("restmail_queue_size = %v, want %v (%v before, plus 10 waiting rows)", size, sizeBefore+10, sizeBefore)
	}
	if due != dueBefore+6 {
		t.Errorf("restmail_queue_due = %v, want %v (%v before, plus 6 due rows; the 4 far-future retries excluded)", due, dueBefore+6, dueBefore)
	}
}

// alertExpr returns the expression of the named rule in
// helm/restmail/files/alerting-rules.yml.
func alertExpr(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("../../../helm/restmail/files/alerting-rules.yml")
	if err != nil {
		t.Fatal(err)
	}
	var rules struct {
		Groups []struct {
			Rules []struct {
				Alert string `yaml:"alert"`
				Expr  string `yaml:"expr"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(raw, &rules); err != nil {
		t.Fatal(err)
	}
	for _, g := range rules.Groups {
		for _, r := range g.Rules {
			if r.Alert == name {
				return strings.Join(strings.Fields(r.Expr), " ")
			}
		}
	}
	t.Fatalf("no %s rule in helm/restmail/files/alerting-rules.yml", name)
	return ""
}

// TestQueueAlerts_ReadTheRightGauge: QueueWorkerStalled reads the rows due
// now and QueueBacklogHigh the whole queue, as the condition tests below
// assume.
func TestQueueAlerts_ReadTheRightGauge(t *testing.T) {
	if got, want := alertExpr(t, "QueueWorkerStalled"), "rate(restmail_queue_processed_total[15m]) == 0 and restmail_queue_due > 0"; got != want {
		t.Errorf("QueueWorkerStalled expr = %q, want %q", got, want)
	}
	if got, want := alertExpr(t, "QueueBacklogHigh"), "restmail_queue_size > 1000"; got != want {
		t.Errorf("QueueBacklogHigh expr = %q, want %q", got, want)
	}
}

// workerStalled is QueueWorkerStalled's condition:
// rate(restmail_queue_processed_total[15m]) == 0 and restmail_queue_due > 0.
func workerStalled(processed, due float64) bool { return processed == 0 && due > 0 }

// TestProcess_QueueWorkerStalled: every attempt the worker runs to an outcome
// counts in restmail_queue_processed_total, so QueueWorkerStalled
// (rate(restmail_queue_processed_total[15m]) == 0 and restmail_queue_due > 0)
// stays quiet while the workers are taking rows, and a failed attempt counts
// in restmail_queue_errors_total too. It also stays quiet for a queue holding
// only retries that are not yet due (#1630), and fires for due rows that no
// worker takes. The condition is checked on the rows and attempts each
// subtest adds, inside one snapshot, so other tests' rows do not count.
func TestProcess_QueueWorkerStalled(t *testing.T) {
	t.Run("only future retries: no stall", func(t *testing.T) {
		gdb := openFailureTestDB(t)
		tx := snapshotTx(t, gdb)
		now := time.Now()
		processed := testutil.ToFloat64(metrics.QueueProcessed)
		sizeBefore, dueBefore := queueGauges(t, tx, now)

		// After a remote outage every row backs off for hours, so the
		// workers are correctly idle.
		addQueued(t, tx, "deferred", 5, now.Add(20*time.Minute), now)
		addQueued(t, tx, "deferred", 5, now.Add(4*time.Hour), now)

		size, due := queueGauges(t, tx, now)
		attempts := testutil.ToFloat64(metrics.QueueProcessed) - processed
		if size-sizeBefore != 10 {
			t.Fatalf("restmail_queue_size rose by %v, want 10", size-sizeBefore)
		}
		if workerStalled(attempts, due-dueBefore) {
			t.Errorf("QueueWorkerStalled fires on a queue of only future retries (due rose by %v, %v attempts)", due-dueBefore, attempts)
		}
	})

	t.Run("due rows, no attempts: stall", func(t *testing.T) {
		gdb := openFailureTestDB(t)
		tx := snapshotTx(t, gdb)
		now := time.Now()
		processed := testutil.ToFloat64(metrics.QueueProcessed)
		_, dueBefore := queueGauges(t, tx, now)

		addQueued(t, tx, "pending", 2, now.Add(-20*time.Minute), now)
		addQueued(t, tx, "deferred", 3, now.Add(-time.Minute), now)

		_, due := queueGauges(t, tx, now)
		attempts := testutil.ToFloat64(metrics.QueueProcessed) - processed
		if due-dueBefore != 5 {
			t.Fatalf("restmail_queue_due rose by %v, want 5", due-dueBefore)
		}
		if !workerStalled(attempts, due-dueBefore) {
			t.Errorf("QueueWorkerStalled does not fire for 5 due rows and %v attempts", attempts)
		}
	})

	t.Run("an attempt counts as processed", testProcessCountsAttempt)
}

func testProcessCountsAttempt(t *testing.T) {
	gdb := openFailureTestDB(t)
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.Suppression{}, &models.DomainRelay{}); err != nil {
		t.Skipf("queue metrics DB test skipped: migrate failed (%v)", err)
	}
	now := time.Now()
	// A null sender gets no DSN, and a recipient carrying CR/LF fails
	// permanently before anything is dialled: one attempt, one bounce.
	item := models.OutboundQueue{
		Sender: "", Recipient: "someone\r\n@remote.test", Domain: "remote.test",
		RawMessage: "Subject: hi\r\n\r\nhi\r\n", Status: "delivering", Attempts: 1,
		MaxRetries: models.DefaultMaxRetries, NextAttempt: now, ExpiresAt: now.Add(models.QueueLifetime),
	}
	if err := gdb.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gdb.Delete(&models.OutboundQueue{}, item.ID) })

	processed := testutil.ToFloat64(metrics.QueueProcessed)
	errs := testutil.ToFloat64(metrics.QueueErrors)
	NewWorker(gdb, "mail.local.test", 1, time.Second).process(context.Background(), item, now)

	if got := testutil.ToFloat64(metrics.QueueProcessed); got != processed+1 {
		t.Errorf("restmail_queue_processed_total = %v after one attempt, want %v", got, processed+1)
	}
	if got := testutil.ToFloat64(metrics.QueueErrors); got != errs+1 {
		t.Errorf("restmail_queue_errors_total = %v after one failed attempt, want %v", got, errs+1)
	}
	var got models.OutboundQueue
	if err := gdb.First(&got, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != "bounced" {
		t.Errorf("status %q, want bounced", got.Status)
	}
}
