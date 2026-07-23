// Package trace persists per-message observability traces asynchronously, off
// the message-processing hot path. A message's aggregate counters are recorded
// inline (exact, lock-free), but the durable MessageTrace row — with its JSON
// stage detail and raw PII — is comparatively expensive to write, so it is
// handed to a background goroutine via a bounded buffer.
//
// The cardinal invariant: recording a trace MUST NOT block or fail message
// processing. If the buffer is full or the recorder is shut down, the trace is
// dropped and restmail_trace_dropped_total is incremented — mail keeps flowing,
// aggregates stay exact, only the storage-costly per-message detail is lost.
package trace

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/metrics"
	"gorm.io/gorm"
)

// Default recorder tuning. The buffer absorbs bursts without blocking callers;
// the batch bounds a single INSERT; the flush interval bounds how long a
// partial batch waits before it is written (mirrors the queue worker's ticker).
const (
	defaultBufferSize    = 4096
	defaultBatchSize     = 512
	defaultFlushInterval = 500 * time.Millisecond
)

// Happy-path terminal outcomes eligible for sampling. These mirror the bounded
// outcome vocabulary the delivery handlers stamp (outcomeDelivered/outcomeQueued
// in internal/api/handlers) — the continue path, whose volume is the storage
// cost we dial down. Every other outcome (rejected/quarantined/discarded/
// deferred) is an anomaly and is recorded unconditionally.
const (
	outcomeDelivered = "delivered"
	outcomeQueued    = "queued"
)

// Config carries the PR4 volume knobs the recorder applies at Record time.
type Config struct {
	// SampleRate is the probability in [0,1] that a happy-path (delivered/queued)
	// trace is persisted. Non-continue outcomes ignore it and are always kept.
	SampleRate float64
	// Retention is the hot window: a recorded trace's ExpiresAt is stamped
	// CreatedAt + Retention for the pruner. Zero leaves ExpiresAt nil (no horizon).
	Retention time.Duration
}

// Recorder is an async, drop-on-full trace sink. One background goroutine drains
// a bounded channel and batch-inserts MessageTrace rows on a ticker. Construct
// with NewRecorder, call Start once, feed it via Record, and Shutdown to flush.
//
// Record is safe on a nil *Recorder (no-op) so handlers can hold an optional
// recorder without nil-guarding every call site.
type Recorder struct {
	db            *gorm.DB
	ch            chan models.MessageTrace
	done          chan struct{}
	wg            sync.WaitGroup
	batchSize     int
	flushInterval time.Duration

	// sampleRate/retention are the PR4 volume knobs applied at Record time. The
	// tunable constructor defaults sampleRate to 1.0 (keep every trace) and
	// retention to 0 (no horizon) so the pre-PR4 capture-all behaviour — and the
	// existing recorder tests — are preserved; NewRecorder overrides them from
	// config.
	sampleRate float64
	retention  time.Duration

	// randFloat returns a value in [0,1) for the sampling dice roll; injectable so
	// tests can drive the gate deterministically. Default is the concurrency-safe
	// math/rand/v2 top-level source. now is the injectable clock for ExpiresAt.
	randFloat func() float64
	now       func() time.Time

	closed  atomic.Bool
	started atomic.Bool
}

// NewRecorder builds a recorder with production defaults, applying the PR4
// sampling/retention config. It does not start the background goroutine — call
// Start.
func NewRecorder(db *gorm.DB, cfg Config) *Recorder {
	r := newRecorder(db, defaultBufferSize, defaultBatchSize, defaultFlushInterval)
	r.sampleRate = cfg.SampleRate
	r.retention = cfg.Retention
	return r
}

// newRecorder is the tunable constructor used by tests to exercise small
// buffers and fast flushes deterministically. It defaults to capture-all
// (sampleRate 1.0, no retention horizon); callers that want sampling set the
// fields or use NewRecorder.
func newRecorder(db *gorm.DB, bufferSize, batchSize int, flushInterval time.Duration) *Recorder {
	return &Recorder{
		db:            db,
		ch:            make(chan models.MessageTrace, bufferSize),
		done:          make(chan struct{}),
		batchSize:     batchSize,
		flushInterval: flushInterval,
		sampleRate:    1.0,
		randFloat:     rand.Float64,
		now:           time.Now,
	}
}

// Start launches the single drain/insert goroutine. Idempotent: a second call
// is a no-op. Safe on a nil recorder.
func (r *Recorder) Start() {
	if r == nil {
		return
	}
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	r.wg.Add(1)
	go r.loop()
}

// Record enqueues a trace without ever blocking the caller. If the buffer is
// full or the recorder has been shut down, the trace is dropped and
// restmail_trace_dropped_total is incremented. Safe on a nil recorder.
//
// Sampling gate (PR4): non-continue outcomes (rejected/quarantined/discarded/
// deferred) are always recorded 100%; happy-path (delivered/queued) traces are
// recorded only with probability sampleRate, and otherwise dropped SILENTLY — the
// aggregate counters already counted them inline, so this is intentional sampling,
// NOT a backpressure loss, and it must not touch trace_dropped_total. Recorded
// traces have their retention horizon stamped (ExpiresAt = CreatedAt + retention)
// for the pruner.
func (r *Recorder) Record(t models.MessageTrace) {
	if r == nil {
		return
	}
	if !r.shouldSample(t.Outcome) {
		// Happy-path trace not selected by sampling: skip entirely. Not a drop —
		// the aggregate rollups still count it via the always-on metrics.
		return
	}
	// Stamp the sampling flag and retention horizon on every recorded trace.
	t.Sampled = true
	if r.retention > 0 {
		now := r.now()
		if t.CreatedAt.IsZero() {
			t.CreatedAt = now
		}
		exp := t.CreatedAt.Add(r.retention)
		t.ExpiresAt = &exp
	}
	// Shut down: the drain goroutine is gone (or leaving); never send, never
	// block, just account the drop. Checked before the send so a post-Shutdown
	// Record can't race the channel.
	if r.closed.Load() {
		metrics.TraceDropped.Inc()
		return
	}
	select {
	case r.ch <- t:
	default:
		// Buffer saturated — drop rather than block message processing.
		metrics.TraceDropped.Inc()
	}
}

// shouldSample applies the sampling policy: anomalies (any non-continue outcome)
// are always kept; the happy path is kept with probability sampleRate. A rate of
// 1.0 keeps everything (randFloat < 1.0 always holds since it is in [0,1)); a
// rate of 0.0 keeps only anomalies.
func (r *Recorder) shouldSample(outcome string) bool {
	switch outcome {
	case outcomeDelivered, outcomeQueued:
		return r.randFloat() < r.sampleRate
	default:
		return true
	}
}

// Shutdown stops accepting new traces and flushes whatever is buffered, then
// returns once the drain goroutine has exited. Safe to call on a nil recorder,
// on one that was never started, and more than once.
func (r *Recorder) Shutdown() {
	if r == nil {
		return
	}
	// Mark closed first so concurrent Record calls drop instead of racing a
	// half-drained buffer. The channel itself is never closed — that would panic
	// an in-flight send; closed=true is our "no more work" signal.
	if !r.closed.CompareAndSwap(false, true) {
		return
	}
	if !r.started.Load() {
		// Never started: drain synchronously so a buffered-but-unstarted recorder
		// still persists what it can on shutdown, then return.
		r.drainAndFlush()
		return
	}
	close(r.done)
	r.wg.Wait()
}

// loop is the single consumer: it accumulates traces into a batch and flushes
// on a full batch, on the ticker, or on shutdown (final drain).
func (r *Recorder) loop() {
	defer r.wg.Done()

	ticker := time.NewTicker(r.flushInterval)
	defer ticker.Stop()

	batch := make([]models.MessageTrace, 0, r.batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		r.insert(batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-r.done:
			// Drain everything still buffered, then flush and exit. closed is
			// already set, so no new traces are arriving.
			for {
				select {
				case t := <-r.ch:
					batch = append(batch, t)
					if len(batch) >= r.batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case t := <-r.ch:
			batch = append(batch, t)
			if len(batch) >= r.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// drainAndFlush empties the buffer in the caller's goroutine (used only for the
// never-started shutdown path).
func (r *Recorder) drainAndFlush() {
	batch := make([]models.MessageTrace, 0, r.batchSize)
	for {
		select {
		case t := <-r.ch:
			batch = append(batch, t)
			if len(batch) >= r.batchSize {
				r.insert(batch)
				batch = batch[:0]
			}
		default:
			if len(batch) > 0 {
				r.insert(batch)
			}
			return
		}
	}
}

// insert persists a batch. A DB error is logged and swallowed: a failed trace
// write must never surface to (or retry into) the message path. A short,
// independent timeout keeps a stalled DB from wedging the drain goroutine.
func (r *Recorder) insert(batch []models.MessageTrace) {
	if r.db == nil || len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.db.WithContext(ctx).CreateInBatches(batch, r.batchSize).Error; err != nil {
		slog.Warn("trace recorder: batch insert failed, dropping traces",
			"count", len(batch), "error", err)
	}
}
