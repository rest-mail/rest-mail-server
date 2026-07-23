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

	closed  atomic.Bool
	started atomic.Bool
}

// NewRecorder builds a recorder with production defaults. It does not start the
// background goroutine — call Start.
func NewRecorder(db *gorm.DB) *Recorder {
	return newRecorder(db, defaultBufferSize, defaultBatchSize, defaultFlushInterval)
}

// newRecorder is the tunable constructor used by tests to exercise small
// buffers and fast flushes deterministically.
func newRecorder(db *gorm.DB, bufferSize, batchSize int, flushInterval time.Duration) *Recorder {
	return &Recorder{
		db:            db,
		ch:            make(chan models.MessageTrace, bufferSize),
		done:          make(chan struct{}),
		batchSize:     batchSize,
		flushInterval: flushInterval,
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
func (r *Recorder) Record(t models.MessageTrace) {
	if r == nil {
		return
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
