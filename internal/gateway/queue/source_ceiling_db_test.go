package queue

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// sourceCeiling is the read ceiling these tests give the worker.
const sourceCeiling = 1 << 20

// sourceOf is a message from sender of exactly n octets, in lines short enough for
// any peer, with an octet that is not UTF-8 in it when eightBit is set, so that the
// row keeps it in raw_octets rather than raw_message.
func sourceOf(sender string, n int, eightBit bool) string {
	head := "From: " + sender + "\r\nTo: someone@remote.test\r\nSubject: ceiling\r\n\r\n"
	if eightBit {
		head += "\xff\r\n"
	}
	line := strings.Repeat("a", 76) + "\r\n"
	rest := n - len(head)
	body := strings.Repeat(line, rest/len(line))
	switch tail := rest % len(line); tail {
	case 0:
	case 1: // a line is at least its CRLF: make the last full line one longer
		body = body[:len(body)-2] + "a\r\n"
	default:
		body += strings.Repeat("a", tail-2) + "\r\n"
	}
	source := head + body
	if len(source) != n {
		panic("sourceOf miscounted")
	}
	return source
}

func ceilingWorker(t *testing.T, db *gorm.DB, mx *timedMX) *Worker {
	return ceilingWorkerWith(t, db, mx, false)
}

// ceilingWorkerWith is ceilingWorker, delivering through the test reset barrier, as a
// test environment's gateway does, when testReset is set.
func ceilingWorkerWith(t *testing.T, db *gorm.DB, mx *timedMX, testReset bool) *Worker {
	t.Helper()
	w := claimingWorker(t, db, mx, testReset)
	w.SetMaxSourceOctets(sourceCeiling)
	return w
}

// A queued source of exactly the ceiling is loaded byte for byte, from either column,
// and delivered.
func TestQueuedSourceAtTheCeilingIsLoadedWhole(t *testing.T) {
	db := openResetWorkerDB(t)
	migrateDeliveryTables(t, db)
	_, sender := sendingDomain(t, db)
	mx := newTimedMX(t, timedMXOptions{})
	w := ceilingWorker(t, db, mx)

	sources := map[uint]string{}
	for _, eightBit := range []bool{false, true} {
		source := sourceOf(sender.Address, sourceCeiling, eightBit)
		row := queueTimedRow(t, db, sender.Address, func(item *models.OutboundQueue) { item.RawMessage = source })
		sources[row.ID] = source
	}
	for range sources {
		item, err := w.claim(time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if item.sourceOctets != sourceCeiling || item.RawMessage != sources[item.ID] {
			t.Fatalf("row %d: loaded %d octets (counted %d), want the %d queued", item.ID, len(item.RawMessage), item.sourceOctets, sourceCeiling)
		}
	}
	db.Model(&models.OutboundQueue{}).Where("sender = ?", sender.Address).Updates(map[string]any{"status": "pending", "attempts": 0})

	// Delivered through the test reset barrier, whose worker for the delivery must keep
	// the ceiling.
	ceilingWorkerWith(t, db, mx, true).drain(0)
	if got := mx.copiesReceived(); got != 2 {
		t.Fatalf("the peer received %d copies, want 2", got)
	}
	var delivered int64
	db.Model(&models.OutboundQueue{}).Where("sender = ? AND status = ?", sender.Address, "delivered").Count(&delivered)
	if delivered != 2 {
		t.Fatalf("%d rows delivered, want 2", delivered)
	}
}

// A queued source one octet past the ceiling, in either column, bounces as a permanent
// failure, message too big for system, without reaching the peer, and its sender gets a
// notice.
func TestQueuedSourceOverTheCeilingBounces(t *testing.T) {
	db := openResetWorkerDB(t)
	migrateDeliveryTables(t, db)
	_, sender := sendingDomain(t, db)
	mx := newTimedMX(t, timedMXOptions{})
	w := ceilingWorker(t, db, mx)

	var rows []models.OutboundQueue
	for _, eightBit := range []bool{false, true} {
		source := sourceOf(sender.Address, sourceCeiling+1, eightBit)
		rows = append(rows, queueTimedRow(t, db, sender.Address, func(item *models.OutboundQueue) { item.RawMessage = source }))
	}
	w.drain(0)

	if got := mx.copiesReceived(); got != 0 {
		t.Fatalf("the peer received %d copies of an oversize source", got)
	}
	for _, row := range rows {
		got := readRow(t, db, row.ID)
		if got.Status != "bounced" || got.LastErrorCode != 552 || got.FailureCategory != models.FailureOtherPermanent ||
			!strings.Contains(got.LastError, "message too big for system") || got.Attempts != 1 {
			t.Fatalf("row %d: status %q, code %d, category %q, attempts %d, error %q; want a permanent 552 bounce after one attempt",
				row.ID, got.Status, got.LastErrorCode, got.FailureCategory, got.Attempts, got.LastError)
		}
	}
	var notices int64
	db.Model(&models.Message{}).Where("mailbox_id = ?", sender.ID).Count(&notices)
	if notices != int64(len(rows)) {
		t.Fatalf("the sender got %d notices, want %d", notices, len(rows))
	}
}

// Claiming a row whose source is many times the ceiling does not load it: the claim
// allocates far less than the source.
func TestClaimingAnOversizeSourceDoesNotLoadIt(t *testing.T) {
	const oversize = 64 << 20
	db := openResetWorkerDB(t)
	migrateDeliveryTables(t, db)
	_, sender := sendingDomain(t, db)
	mx := newTimedMX(t, timedMXOptions{})
	w := ceilingWorker(t, db, mx)

	source := sourceOf(sender.Address, oversize, false)
	row := queueTimedRow(t, db, sender.Address, func(item *models.OutboundQueue) { item.RawMessage = source })
	source = ""

	var item claimedItem
	var err error
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	item, err = w.claim(time.Now())
	runtime.ReadMemStats(&after)
	if err != nil || item.ID != row.ID {
		t.Fatalf("claimed %d (%v), want %d", item.ID, err, row.ID)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("claiming a %d-octet source allocated %d octets", oversize, allocated)
	if item.sourceOctets != oversize || item.RawMessage != "" {
		t.Fatalf("counted %d octets and loaded %d, want %d counted and none loaded", item.sourceOctets, len(item.RawMessage), oversize)
	}
	if allocated > 4*sourceCeiling {
		t.Fatalf("claiming a %d-octet source allocated %d octets, want at most %d", oversize, allocated, 4*sourceCeiling)
	}
}

// A worker reads at most what the gateway's largest delivery body is until told
// otherwise, and a ceiling of zero or less leaves that in place.
func TestWorkerSourceCeilingDefaultsToTheDeliveryBodyLimit(t *testing.T) {
	w := NewWorker(nil, "sender.test", 1, time.Second)
	want := defaultMaxSourceOctets()
	if w.maxSourceOctets != want || want != 3*(10<<20)+1<<20 {
		t.Fatalf("default ceiling %d, want %d", w.maxSourceOctets, want)
	}
	w.SetMaxSourceOctets(0)
	if w.maxSourceOctets != want {
		t.Fatalf("a zero ceiling left %d", w.maxSourceOctets)
	}
}
