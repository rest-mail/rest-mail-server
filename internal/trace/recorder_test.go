package trace

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/metrics"
	"gorm.io/gorm"
)

// TestRecorder_DropOnFull proves the cardinal invariant: when the buffer is
// saturated, Record drops the trace and increments restmail_trace_dropped_total
// instead of blocking. The loop is intentionally NOT started so nothing drains.
func TestRecorder_DropOnFull(t *testing.T) {
	r := newRecorder(nil, 2, 2, time.Hour) // buffer of 2, no consumer

	// Fill the buffer to capacity — these must NOT drop.
	before := testutil.ToFloat64(metrics.TraceDropped)
	r.Record(models.MessageTrace{Outcome: "rejected"})
	r.Record(models.MessageTrace{Outcome: "rejected"})
	if got := testutil.ToFloat64(metrics.TraceDropped) - before; got != 0 {
		t.Fatalf("dropped %v traces while filling to capacity, want 0", got)
	}

	// Buffer full — the next Record drops.
	r.Record(models.MessageTrace{Outcome: "rejected"})
	if got := testutil.ToFloat64(metrics.TraceDropped) - before; got != 1 {
		t.Errorf("trace_dropped delta = %v after overflow, want 1", got)
	}
}

// TestRecorder_NonBlockingUnderPressure fires many Records at a full buffer and
// requires them all to return (dropping) rather than blocking the caller.
func TestRecorder_NonBlockingUnderPressure(t *testing.T) {
	r := newRecorder(nil, 1, 1, time.Hour)
	r.Record(models.MessageTrace{}) // fill the single slot

	before := testutil.ToFloat64(metrics.TraceDropped)
	const n = 1000
	done := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ {
			r.Record(models.MessageTrace{})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked under buffer pressure — must never block the caller")
	}
	if got := testutil.ToFloat64(metrics.TraceDropped) - before; got != n {
		t.Errorf("trace_dropped delta = %v, want %d (all overflow calls dropped)", got, n)
	}
}

// TestRecorder_DropAfterShutdown verifies a Record after Shutdown returns
// immediately and drops (the closed flag short-circuits the send, so a
// post-shutdown caller can never panic on or block a drained channel).
func TestRecorder_DropAfterShutdown(t *testing.T) {
	r := newRecorder(nil, 4, 4, time.Hour)
	r.Start()
	r.Shutdown()

	before := testutil.ToFloat64(metrics.TraceDropped)
	r.Record(models.MessageTrace{Outcome: "delivered"})
	if got := testutil.ToFloat64(metrics.TraceDropped) - before; got != 1 {
		t.Errorf("trace_dropped delta = %v after shutdown, want 1", got)
	}
}

// TestRecorder_NilSafe confirms every method is a no-op (never panics) on a nil
// recorder, so a handler holding an optional recorder needs no nil guards.
func TestRecorder_NilSafe(t *testing.T) {
	var r *Recorder
	r.Start()
	r.Record(models.MessageTrace{})
	r.Shutdown()
}

// TestRecorder_ShutdownIdempotent confirms a second Shutdown is a harmless no-op.
func TestRecorder_ShutdownIdempotent(t *testing.T) {
	r := newRecorder(nil, 4, 4, time.Hour)
	r.Start()
	r.Shutdown()
	r.Shutdown() // must not panic / double-close
}

// ── DB-backed (skip when no database is reachable) ───────────────────

func openTraceTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("trace recorder DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.MessageTrace{}); err != nil {
		t.Skipf("trace recorder DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// TestRecorder_BatchInsertAndFlush proves the recorder batches buffered traces
// into the database and flushes the remainder on shutdown — no row is lost.
func TestRecorder_BatchInsertAndFlush(t *testing.T) {
	gdb := openTraceTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec("DELETE FROM message_traces").Error; err != nil {
		t.Fatalf("clear message_traces: %v", err)
	}

	// Buffer comfortably larger than the input (so nothing drops on full), a
	// small batch so full-batch flushes happen mid-run, and a long ticker so the
	// remaining tail is only persisted by the drain-on-Shutdown path.
	r := newRecorder(tx, 64, 3, time.Hour)
	r.Start()

	const n = 7
	for i := 0; i < n; i++ {
		r.Record(models.MessageTrace{
			Direction:    "inbound",
			Outcome:      "delivered",
			RFCMessageID: "<batch-" + strconv.Itoa(i) + "@test>",
		})
	}
	r.Shutdown() // flushes buffered traces before returning

	var count int64
	if err := tx.Model(&models.MessageTrace{}).Count(&count).Error; err != nil {
		t.Fatalf("count message_traces: %v", err)
	}
	if count != n {
		t.Errorf("persisted %d traces, want %d (batches + shutdown flush)", count, n)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
