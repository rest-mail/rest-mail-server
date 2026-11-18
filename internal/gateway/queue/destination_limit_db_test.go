package queue

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// destinationMXes gives each recipient domain an MX of its own: the domain's name
// is its MX host, and the map says which peer answers there.
type destinationMXes map[string]*timedMX

func (d destinationMXes) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	return []*net.MX{{Host: name + ".", Pref: 10}}, nil
}

func (d destinationMXes) LookupHost(context.Context, string) ([]string, error) {
	return []string{"127.0.0.1"}, nil
}

func (d destinationMXes) addr(host string) string { return d[host].addr(host) }

// queueFor queues a message for domain, due at the given time.
func queueFor(t *testing.T, db *gorm.DB, sender, domain string, due time.Time) models.OutboundQueue {
	t.Helper()
	return queueTimedRow(t, db, sender, func(item *models.OutboundQueue) {
		item.Domain = domain
		item.Recipient = "someone@" + domain
		item.NextAttempt = due
	})
}

// queueDelivering puts a row in the state a worker's claim leaves it in, with its
// last heartbeat at the given time.
func queueDelivering(t *testing.T, db *gorm.DB, domain string, heartbeat time.Time) models.OutboundQueue {
	t.Helper()
	return queueTimedRow(t, db, "queued@sender.test", func(item *models.OutboundQueue) {
		item.Domain = domain
		item.Recipient = "someone@" + domain
		item.Status = "delivering"
		item.Attempts = 1
		item.LastAttempt = &heartbeat
		item.NextAttempt = heartbeat.Add(-time.Hour)
	})
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A destination that stops answering holds only its limit of connections, however
// much mail it has due and however early, and the other workers carry on with
// everyone else's mail (#1966). What it could not take is untouched: still due, no
// attempt charged, no retry delay. The connections are counted at the peer. This
// is the real worker loop over real SMTP sessions, with and without the reset
// barrier a non-production gateway runs under.
func TestStalledDestinationHoldsOnlyItsLimit(t *testing.T) {
	for _, testReset := range []bool{false, true} {
		t.Run("testReset="+strconv.FormatBool(testReset), func(t *testing.T) {
			db := openResetWorkerDB(t)
			migrateDeliveryTables(t, db)
			_, sender := sendingDomain(t, db)
			stalled := newTimedMX(t, timedMXOptions{stall: "MAIL"})
			fast := newTimedMX(t, timedMXOptions{})
			mxes := destinationMXes{"stalled.test": stalled, "fast.test": fast}

			// All the stalled destination's mail is due first, so without a limit
			// every worker takes one of its messages and nothing else moves.
			now := time.Now()
			var stuck, others []models.OutboundQueue
			for i := range 6 {
				stuck = append(stuck, queueFor(t, db, sender.Address, "stalled.test", now.Add(-time.Hour+time.Duration(i)*time.Second)))
			}
			for i := range 5 {
				others = append(others, queueFor(t, db, sender.Address, "fast.test", now.Add(-time.Minute+time.Duration(i)*time.Second)))
			}
			before := map[uint]models.OutboundQueue{}
			for _, row := range stuck {
				before[row.ID] = readRow(t, db, row.ID)
			}

			w := claimingWorker(t, db, fast, testReset)
			w.numWorkers = 4
			w.pollInterval = 20 * time.Millisecond
			w.resolver = mxes
			w.smtpAddr = mxes.addr
			w.SetDestinationLimit(2)
			w.Start()
			var stop sync.Once
			shutdown := func() {
				stop.Do(func() {
					stalled.releaseStalled()
					w.Shutdown()
				})
			}
			t.Cleanup(shutdown)

			if !waitForCopies(fast, len(others), 30*time.Second) {
				t.Fatalf("the other destination got %d of %d messages while one was stalled (%d sessions held there)",
					fast.copiesReceived(), len(others), stalled.stalledSessions())
			}
			waitUntil(t, "the stalled destination holds its limit", func() bool { return stalled.stalledSessions() >= 2 })
			// Workers are idle now, with mail due: give them polls to overstep.
			time.Sleep(20 * w.pollInterval)
			if got := stalled.stalledSessions(); got != 2 {
				t.Errorf("the stalled destination holds %d connections, want its limit of 2", got)
			}

			delivering, heldBack := 0, 0
			for _, row := range stuck {
				got := readRow(t, db, row.ID)
				switch got.Status {
				case "delivering":
					delivering++
				case "pending":
					heldBack++
					want := before[row.ID]
					if got.Attempts != 0 || got.LastAttempt != nil || !got.NextAttempt.Equal(want.NextAttempt) || got.LastError != "" {
						t.Errorf("held-back message %d was charged: attempts %d, last attempt %v, next attempt %s (was %s), error %q",
							row.ID, got.Attempts, got.LastAttempt, got.NextAttempt, want.NextAttempt, got.LastError)
					}
				default:
					t.Errorf("message %d for the stalled destination is %q", row.ID, got.Status)
				}
			}
			if delivering != 2 || heldBack != 4 {
				t.Errorf("%d delivering and %d held back, want 2 and 4", delivering, heldBack)
			}
			shutdown()
			for _, row := range others {
				if got := readRow(t, db, row.ID); got.Status != "delivered" || got.Attempts != 1 {
					t.Errorf("message %d: status %q after %d attempts, want delivered after 1", row.ID, got.Status, got.Attempts)
				}
			}
		})
	}
}

// Workers claiming at the same moment each see room under the limit, and only one
// of them may act on it, in this gateway or another on the same database. A worker
// that loses a destination to another claim goes on to the next one rather than
// idling for a poll.
func TestConcurrentClaimsStayWithinTheLimit(t *testing.T) {
	db := openResetWorkerDB(t)
	now := time.Now()
	for i := range 20 {
		queueFor(t, db, "queued@sender.test", "busy.test", now.Add(-time.Hour+time.Duration(i)*time.Second))
	}
	other := queueFor(t, db, "queued@sender.test", "other.test", now.Add(-time.Minute))

	w := NewWorker(db, "sender.test", 1, time.Second)
	w.SetDestinationLimit(2)
	inFlight := func(domain string) int64 {
		t.Helper()
		var n int64
		if err := db.Model(&models.OutboundQueue{}).Where("status = 'delivering' AND domain = ?", domain).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	for round := range 10 {
		start := make(chan struct{})
		var claims sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			claims.Add(1)
			go func() {
				defer claims.Done()
				<-start
				if _, err := w.claim(time.Now()); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					errs <- err
				}
			}()
		}
		close(start)
		claims.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: claim failed: %v", round, err)
		}
		if got := inFlight("busy.test"); got > 2 {
			t.Fatalf("round %d: %d messages in flight to one destination, limit 2", round, got)
		}
	}
	if got := inFlight("busy.test"); got != 2 {
		t.Errorf("%d messages in flight to the busy destination, want its limit of 2", got)
	}
	if got := readRow(t, db, other.ID); got.Status != "delivering" {
		t.Errorf("the other destination's message is %q: the claims never reached it", got.Status)
	}
}

// What counts as in flight is worked out from the rows at each claim, so nothing
// can leak: a delivery that ended no longer counts, and neither does one whose
// worker crashed, once its heartbeat is older than the reclaim interval. The crashed
// worker's message is itself taken up again.
func TestDestinationLimitCountsOnlyLiveDeliveries(t *testing.T) {
	db := openResetWorkerDB(t)
	now := time.Now()
	w := NewWorker(db, "sender.test", 1, time.Second)
	w.SetDestinationLimit(2)
	w.reclaim = 10 * time.Minute

	first := queueDelivering(t, db, "remote.test", now)
	// The same destination, however the address spelled it.
	second := queueDelivering(t, db, "REMOTE.test", now)
	waiting := queueFor(t, db, "queued@sender.test", "Remote.Test", now.Add(-time.Minute))

	if item, err := w.claim(now); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("claimed %d (%v) with the destination at its limit", item.ID, err)
	}

	// One delivery ends: its place goes to the message waiting.
	if err := db.Model(&first).Update("status", "delivered").Error; err != nil {
		t.Fatal(err)
	}
	item, err := w.claim(now)
	if err != nil || item.ID != waiting.ID {
		t.Fatalf("claimed %d (%v), want the waiting message %d", item.ID, err, waiting.ID)
	}
	if got := readRow(t, db, waiting.ID); got.Attempts != 1 {
		t.Errorf("the waiting message has %d attempts on its first, want 1", got.Attempts)
	}

	// Both workers now holding the destination crash. A restarted gateway is held
	// back only until their heartbeats go stale, and then finishes their mail.
	if item, err := w.claim(now); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("claimed %d (%v) with live deliveries at the limit", item.ID, err)
	}
	later := now.Add(w.reclaim + time.Second)
	restarted := NewWorker(db, "sender.test", 1, time.Second)
	restarted.SetDestinationLimit(2)
	restarted.reclaim = w.reclaim
	taken := map[uint]bool{}
	for range 2 {
		item, err := restarted.claim(later)
		if err != nil {
			t.Fatalf("after the crash: %v", err)
		}
		taken[item.ID] = true
	}
	if !taken[second.ID] || !taken[waiting.ID] {
		t.Errorf("reclaimed %v, want %d and %d", taken, second.ID, waiting.ID)
	}
}

// A limit of zero is no limit, for an operator who wants none.
func TestDestinationLimitZeroIsNoLimit(t *testing.T) {
	db := openResetWorkerDB(t)
	now := time.Now()
	const rows = 5
	for i := range rows {
		queueFor(t, db, "queued@sender.test", "remote.test", now.Add(-time.Hour+time.Duration(i)*time.Second))
	}
	w := NewWorker(db, "sender.test", 1, time.Second)
	w.SetDestinationLimit(0)
	for i := range rows {
		if _, err := w.claim(now); err != nil {
			t.Fatal(fmt.Errorf("claim %d: %w", i+1, err))
		}
	}
}

// The limit a worker has before anything configures it is the one the gateway
// documents and config defaults to.
func TestWorkerStartsWithTheDefaultDestinationLimit(t *testing.T) {
	if w := NewWorker(nil, "sender.test", 1, time.Second); w.maxPerDestination != defaultMaxPerDestination {
		t.Fatalf("maxPerDestination = %d, want %d", w.maxPerDestination, defaultMaxPerDestination)
	}
}
