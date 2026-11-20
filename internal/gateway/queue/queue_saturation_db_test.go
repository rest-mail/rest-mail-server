package queue

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/metrics"
	"gorm.io/gorm"
)

// The gauges that tell queue saturation from a destination held back by
// QUEUE_MAX_PER_DESTINATION (#2117, AC04b). Each test reads them inside one
// repeatable-read snapshot, before and after adding its own rows, so rows other
// tests commit meanwhile do not move the counts; the destinations are unique to
// the test for the same reason.

// splitGauges is what refreshQueueSize says at now with the given limit.
type splitGauges struct {
	size, due, waiting, oldestWaiting, held float64
}

// backlog is QueueBacklogHigh's left-hand side:
// restmail_queue_size - restmail_queue_held_by_destination.
func (g splitGauges) backlog() float64 { return g.size - g.held }

func readSplit(t *testing.T, tx *gorm.DB, now time.Time, limit int) splitGauges {
	t.Helper()
	if err := refreshQueueSize(tx, now, staleDeliveringTimeout, limit); err != nil {
		t.Fatal(err)
	}
	return splitGauges{
		size:          testutil.ToFloat64(metrics.QueueSize),
		due:           testutil.ToFloat64(metrics.QueueDue),
		waiting:       testutil.ToFloat64(metrics.QueueWaitingForWorker),
		oldestWaiting: testutil.ToFloat64(metrics.QueueWaitingForWorkerOldest),
		held:          testutil.ToFloat64(metrics.QueueHeldByDestination),
	}
}

// addTo inserts n rows for one destination: status, next attempt, and for a
// delivering row the time of its last heartbeat.
func addTo(t *testing.T, tx *gorm.DB, destination, sender, status string, n int, next, lastAttempt, now time.Time) {
	t.Helper()
	for range n {
		row := models.OutboundQueue{
			Sender: sender, Recipient: "someone@" + destination, Domain: destination,
			RawMessage: "Subject: hi\r\n\r\nhi\r\n", Status: status, MaxRetries: models.DefaultMaxRetries,
			NextAttempt: next, ExpiresAt: now.Add(models.QueueLifetime),
		}
		if status == "delivering" {
			row.LastAttempt = &lastAttempt
		}
		if err := tx.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// unique names a destination no other test or run uses.
func unique(t *testing.T, label string) string {
	return fmt.Sprintf("%s-%d.saturation.test", label, time.Now().UnixNano())
}

// TestQueueGauges_DestinationHeldBackIsNotABacklog: one destination with its
// QUEUE_MAX_PER_DESTINATION messages in flight and ten more due. A worker may
// take none of the ten, so they are held by the destination: not waiting for a
// worker, not ageing restmail_queue_waiting_for_worker_oldest_seconds, and not
// in QueueBacklogHigh's count. restmail_queue_due counts all twelve, which is
// how it misread a held destination as a backlog before #2117.
func TestQueueGauges_DestinationHeldBackIsNotABacklog(t *testing.T) {
	gdb := openFailureTestDB(t)
	tx := snapshotTx(t, gdb)
	now := time.Now()
	const limit = 2
	before := readSplit(t, tx, now, limit)

	capped := unique(t, "capped")
	addTo(t, tx, capped, "sender@local.test", "delivering", limit, now.Add(-time.Hour), now.Add(-time.Minute), now)
	addTo(t, tx, capped, "sender@local.test", "pending", 10, now.Add(-30*time.Minute), time.Time{}, now)

	after := readSplit(t, tx, now, limit)
	if got := after.held - before.held; got != 10 {
		t.Errorf("restmail_queue_held_by_destination rose by %v, want 10 (every due row of a destination at its limit)", got)
	}
	if got := after.waiting - before.waiting; got != 0 {
		t.Errorf("restmail_queue_waiting_for_worker rose by %v, want 0: no worker may take a row of a destination at its limit", got)
	}
	if after.oldestWaiting != before.oldestWaiting {
		t.Errorf("restmail_queue_waiting_for_worker_oldest_seconds went from %v to %v; rows held for 30 minutes by the destination cap must not age it", before.oldestWaiting, after.oldestWaiting)
	}
	if got := after.backlog() - before.backlog(); got != limit {
		t.Errorf("QueueBacklogHigh's count rose by %v, want %d (the rows in flight, not the 10 held back)", got, limit)
	}
	if got := after.due - before.due; got != 12 {
		t.Errorf("restmail_queue_due rose by %v, want 12: it still counts held rows, which is why no alert may read it as a backlog", got)
	}
}

// TestQueueGauges_SaturatedWorkersAreWaitingForAWorker: the same ten due rows,
// each to its own destination with nothing in flight, due for 30 minutes. Any
// worker may take each of them, so a queue that has left them there is
// saturated: they wait for a worker, the oldest has waited 30 minutes (over
// QueueWorkersSaturated's 300 seconds), none is held, and all ten count in
// QueueBacklogHigh's count.
func TestQueueGauges_SaturatedWorkersAreWaitingForAWorker(t *testing.T) {
	gdb := openFailureTestDB(t)
	tx := snapshotTx(t, gdb)
	now := time.Now()
	const limit = 2
	before := readSplit(t, tx, now, limit)

	for i := range 10 {
		addTo(t, tx, unique(t, fmt.Sprintf("open%d", i)), "sender@local.test", "pending", 1, now.Add(-30*time.Minute), time.Time{}, now)
	}

	after := readSplit(t, tx, now, limit)
	if got := after.waiting - before.waiting; got != 10 {
		t.Errorf("restmail_queue_waiting_for_worker rose by %v, want 10", got)
	}
	if got := after.held - before.held; got != 0 {
		t.Errorf("restmail_queue_held_by_destination rose by %v, want 0: each destination has a place free", got)
	}
	if after.oldestWaiting < 30*60 || after.oldestWaiting > 30*60+1 {
		t.Errorf("restmail_queue_waiting_for_worker_oldest_seconds = %v, want 1800 (due 30 minutes ago)", after.oldestWaiting)
	}
	if after.oldestWaiting <= 300 {
		t.Errorf("restmail_queue_waiting_for_worker_oldest_seconds = %v; QueueWorkersSaturated (> 300) would not fire", after.oldestWaiting)
	}
	if got := after.backlog() - before.backlog(); got != 10 {
		t.Errorf("QueueBacklogHigh's count rose by %v, want 10", got)
	}
}

// TestQueueGauges_DestinationPlacesSplitItsRows: a destination with one place
// free of two and five rows due has one waiting for a worker and four held. A
// limit of zero (no cap) holds none back. A delivering row whose heartbeat is
// older than the reclaim time holds no place and is itself waiting for a worker,
// due since it went stale.
func TestQueueGauges_DestinationPlacesSplitItsRows(t *testing.T) {
	gdb := openFailureTestDB(t)
	tx := snapshotTx(t, gdb)
	now := time.Now()
	before := readSplit(t, tx, now, 2)
	beforeUncapped := readSplit(t, tx, now, 0)

	half := unique(t, "half")
	addTo(t, tx, half, "sender@local.test", "delivering", 1, now.Add(-time.Hour), now.Add(-time.Minute), now)
	addTo(t, tx, half, "sender@local.test", "pending", 5, now.Add(-time.Minute), time.Time{}, now)
	stale := unique(t, "stale")
	wentStale := 10 * time.Minute
	addTo(t, tx, stale, "sender@local.test", "delivering", 1, now.Add(-time.Hour), now.Add(-staleDeliveringTimeout-wentStale), now)

	after := readSplit(t, tx, now, 2)
	if got := after.waiting - before.waiting; got != 2 {
		t.Errorf("restmail_queue_waiting_for_worker rose by %v, want 2 (one place free at %s, and the stale row)", got, half)
	}
	if got := after.held - before.held; got != 4 {
		t.Errorf("restmail_queue_held_by_destination rose by %v, want 4", got)
	}
	if after.oldestWaiting < wentStale.Seconds() {
		t.Errorf("restmail_queue_waiting_for_worker_oldest_seconds = %v, want at least %v: the stale row has been claimable that long", after.oldestWaiting, wentStale.Seconds())
	}

	uncapped := readSplit(t, tx, now, 0)
	if got := uncapped.waiting - beforeUncapped.waiting; got != 6 {
		t.Errorf("with no cap, restmail_queue_waiting_for_worker rose by %v, want 6", got)
	}
	if got := uncapped.held - beforeUncapped.held; got != 0 {
		t.Errorf("with no cap, restmail_queue_held_by_destination rose by %v, want 0", got)
	}
}

// TestQueueGauges_WaitingIsWhatAWorkerTakes checks the gauges against the
// production claim, an oracle that does not share their arithmetic: a worker
// claims until nothing is left it may take, and the rows it took from this
// test's destinations are exactly restmail_queue_waiting_for_worker's rise.
// Afterwards nothing is waiting for a worker anywhere, and the rows the cap
// holds back are still held. A row sent as a domain whose outbound mail is
// stopped is neither: the claim never takes it (#1590).
func TestQueueGauges_WaitingIsWhatAWorkerTakes(t *testing.T) {
	gdb := openFailureTestDB(t)
	tx := snapshotTx(t, gdb)
	now := time.Now()
	const limit = 2
	before := readSplit(t, tx, now, limit)

	full := unique(t, "full")
	addTo(t, tx, full, "sender@local.test", "delivering", 2, now.Add(-time.Hour), now.Add(-time.Minute), now)
	addTo(t, tx, full, "sender@local.test", "pending", 5, now.Add(-20*time.Minute), time.Time{}, now)
	room := unique(t, "room")
	addTo(t, tx, room, "sender@local.test", "deferred", 3, now.Add(-10*time.Minute), time.Time{}, now)
	var singles []string
	for i := range 4 {
		d := unique(t, fmt.Sprintf("single%d", i))
		singles = append(singles, d)
		addTo(t, tx, d, "sender@local.test", "pending", 1, now.Add(-5*time.Minute), time.Time{}, now)
	}
	stoppedAt := now.Add(-time.Hour)
	stoppedSender := unique(t, "stopped")
	if err := tx.Create(&models.Domain{Name: stoppedSender, OutboundStoppedAt: &stoppedAt}).Error; err != nil {
		t.Fatal(err)
	}
	stoppedTo := unique(t, "fromstopped")
	addTo(t, tx, stoppedTo, "sender@"+stoppedSender, "pending", 1, now.Add(-time.Hour), time.Time{}, now)

	after := readSplit(t, tx, now, limit)
	waiting := after.waiting - before.waiting
	held := after.held - before.held
	// full: 0 free, 5 held. room: 2 free of 3. singles: 1 each. stopped: neither.
	if waiting != 6 || held != 6 {
		t.Fatalf("waiting rose by %v and held by %v, want 6 and 6", waiting, held)
	}

	w := NewWorker(tx, "mail.local.test", 1, time.Second)
	w.SetDestinationLimit(limit)
	mine := map[string]bool{full: true, room: true, stoppedTo: true}
	for _, d := range singles {
		mine[d] = true
	}
	claimed := map[string]int{}
	for {
		item, err := w.claim(now)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if mine[item.Domain] {
			claimed[item.Domain]++
		}
	}
	total := 0
	for _, n := range claimed {
		total += n
	}
	if float64(total) != waiting {
		t.Errorf("a worker took %d rows (%v); restmail_queue_waiting_for_worker said %v", total, claimed, waiting)
	}
	if claimed[full] != 0 || claimed[room] != 2 || claimed[stoppedTo] != 0 {
		t.Errorf("claimed %v: want none from %s (at its limit), 2 from %s, none sent as a stopped domain", claimed, full, room)
	}

	drained := readSplit(t, tx, now, limit)
	if drained.waiting != 0 || drained.oldestWaiting != 0 {
		t.Errorf("after the worker took all it may, restmail_queue_waiting_for_worker = %v and its oldest = %vs, want 0 and 0", drained.waiting, drained.oldestWaiting)
	}
	// Held now: full's 5, and room's last row behind the 2 just claimed.
	if got := drained.held - before.held; got < 6 {
		t.Errorf("after the claims, restmail_queue_held_by_destination rose by %v from before, want at least 6 (this test's held rows)", got)
	}
}
