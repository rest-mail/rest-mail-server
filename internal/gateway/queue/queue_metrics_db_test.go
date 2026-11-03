package queue

import (
	"database/sql"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/metrics"
)

// TestRefreshQueueSize_QueueBacklogHigh: restmail_queue_size counts every row
// still waiting to leave (pending, deferred, delivering) and none that has
// left, so QueueBacklogHigh (restmail_queue_size > 1000) sees the queue grow.
func TestRefreshQueueSize_QueueBacklogHigh(t *testing.T) {
	gdb := openFailureTestDB(t)
	// One snapshot for the whole test, so rows other tests commit meanwhile do
	// not move the count under it; nothing here is committed.
	tx := gdb.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })

	if err := refreshQueueSize(tx); err != nil {
		t.Fatal(err)
	}
	before := testutil.ToFloat64(metrics.QueueSize)

	now := time.Now()
	add := func(status string, n int) {
		t.Helper()
		rows := make([]models.OutboundQueue, n)
		for i := range rows {
			rows[i] = models.OutboundQueue{
				Sender: "sender@local.test", Recipient: "someone@remote.test", Domain: "remote.test",
				RawMessage: "Subject: hi\r\n\r\nhi\r\n", Status: status, MaxRetries: models.DefaultMaxRetries,
				NextAttempt: now, ExpiresAt: now.Add(models.QueueLifetime),
			}
		}
		if err := tx.CreateInBatches(&rows, 500).Error; err != nil {
			t.Fatal(err)
		}
	}
	add("pending", 900)
	add("deferred", 80)
	add("delivering", 21)
	// History is not backlog.
	add("delivered", 50)
	add("bounced", 50)

	if err := refreshQueueSize(tx); err != nil {
		t.Fatal(err)
	}
	got := testutil.ToFloat64(metrics.QueueSize)
	if got != before+1001 {
		t.Fatalf("restmail_queue_size = %v, want %v (%v before, plus 1001 waiting rows)", got, before+1001, before)
	}
	if got <= 1000 {
		t.Errorf("restmail_queue_size = %v after 1001 waiting rows; QueueBacklogHigh would not fire", got)
	}
}

// TestProcess_QueueWorkerStalled: every attempt the worker runs to an outcome
// counts in restmail_queue_processed_total, so QueueWorkerStalled
// (rate(restmail_queue_processed_total[15m]) == 0 and restmail_queue_size > 0)
// stays quiet while the workers are taking rows, and a failed attempt counts
// in restmail_queue_errors_total too.
func TestProcess_QueueWorkerStalled(t *testing.T) {
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
	NewWorker(gdb, "mail.local.test", 1, time.Second).process(item, now)

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
