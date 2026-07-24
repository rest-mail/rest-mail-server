// Package rollup snapshots the always-on, 100%-accurate Prometheus pipeline
// counters into time-bucketed database aggregates, so the in-app dashboard has
// durable windowed history without an external Prometheus/Grafana.
//
// Why snapshot the counters and NOT the traces: per-message traces are SAMPLED
// (happy-path ~10%), so re-aggregating message_traces would undercount delivered
// mail ~10×. The pipeline counters, by contrast, are incremented inline for
// EVERY message (never sampled), so the rollups they feed are exact. Sampling and
// the trace pruner therefore never affect aggregate accuracy — that is the whole
// point of computing rollups from the counters rather than the trace rows.
package rollup

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// trackedMetrics is the fixed set of always-on counter families the worker
// snapshots. These are the inbound pipeline funnel counters wired in PR1/PR2,
// counted inline on the hot path for every message (never sampled).
//
// messages_sent_total / pipeline_terminal_total{direction="outbound"} are wired
// but live in the smtp-gateway process, which has no /metrics endpoint in this
// checkout (the known gateway-scraping gap — see the design backlog), so they are
// not present in the API process registry and are not rolled up yet. The inbound
// funnel is fully covered here.
var trackedMetrics = map[string]bool{
	"restmail_messages_received_total":        true,
	"restmail_pipeline_stage_decisions_total": true,
	"restmail_pipeline_terminal_total":        true,
	"restmail_auth_verdict_total":             true,
	"restmail_pipeline_reject_reason_total":   true,
}

// Worker periodically snapshots the tracked counters and writes each series'
// per-bucket delta to pipeline_rollups. It mirrors the ticker/lifecycle shape of
// the queue worker and DMARC reporter: Start launches one background goroutine,
// Shutdown stops it (and takes a final snapshot to capture the partial interval).
type Worker struct {
	db       *gorm.DB
	gatherer prometheus.Gatherer
	interval time.Duration
	now      func() time.Time

	// Multi-resolution downsampling config (see downsample.go). Zero
	// detailedRetention or coarseResolution disables downsampling entirely, so a
	// worker built without WithDownsampling behaves exactly as before (fine
	// rollups only). Read once at construction; never mutated after Start.
	detailedRetention  time.Duration // keep fine rollups at fine resolution for this window
	coarseResolution   time.Duration // coarse (downsampled) bucket width, e.g. 24h
	coarseRetention    time.Duration // optional hard cap on coarse-row age; 0 = unbounded
	downsampleInterval time.Duration // how often the downsample pass runs

	// onBeforeFineDelete is a test-only seam: if non-nil it is invoked inside each
	// per-period downsample transaction AFTER the coarse row is written but BEFORE
	// the superseded fine rows are deleted. Returning an error rolls the whole
	// transaction back, exercising the crash-between-insert-and-delete path so a
	// test can prove atomicity (fine rows survive, no partial coarse row leaks).
	// nil in production.
	onBeforeFineDelete func() error

	// lastSeen is the watermark: the last cumulative value observed per series.
	// It is in-memory by design. The counters live in THIS process's registry and
	// reset to zero when the process restarts, so the watermark must reset with
	// them — persisting it across restarts would mask the post-restart climb from
	// zero and undercount. The reset guard in delta() additionally covers any
	// current < last observation defensively. Touched only by the single loop
	// goroutine (and the post-Wait final snapshot), so it needs no lock.
	lastSeen map[string]float64

	stop chan struct{}
	wg   sync.WaitGroup
}

// Option customises a Worker at construction (functional-options so the base
// NewWorker signature stays backward-compatible).
type Option func(*Worker)

// WithDownsampling enables multi-resolution downsampling: fine rollups are kept
// at fine resolution for detailedRetention, then coarse periods aged fully past
// that window are condensed to coarseResolution rows and their fine rows removed.
// coarseRetention optionally caps coarse-row age (0 = keep indefinitely).
// interval is how often the downsample pass runs. Passing zero detailedRetention
// or coarseResolution leaves downsampling disabled.
func WithDownsampling(detailedRetention, coarseResolution, coarseRetention, interval time.Duration) Option {
	return func(w *Worker) {
		w.detailedRetention = detailedRetention
		w.coarseResolution = coarseResolution
		w.coarseRetention = coarseRetention
		w.downsampleInterval = interval
	}
}

// NewWorker builds a rollup worker reading the process-default Prometheus
// registry on the given interval (also the rollup bucket width). Downsampling is
// off unless WithDownsampling is supplied.
func NewWorker(db *gorm.DB, interval time.Duration, opts ...Option) *Worker {
	w := &Worker{
		db:       db,
		gatherer: prometheus.DefaultGatherer,
		interval: interval,
		now:      time.Now,
		lastSeen: make(map[string]float64),
		stop:     make(chan struct{}),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Start launches the background snapshot loop and, when downsampling is enabled,
// the background downsample loop. Both share the stop channel and WaitGroup, so
// Shutdown drains them together.
func (w *Worker) Start() {
	w.wg.Add(1)
	go w.loop()
	if w.downsampleEnabled() {
		w.wg.Add(1)
		go w.downsampleLoop()
	}
	slog.Info("rollup worker started",
		"interval", w.interval,
		"downsampling", w.downsampleEnabled(),
		"detailed_retention", w.detailedRetention,
		"coarse_resolution", w.coarseResolution)
}

// Shutdown stops the loop and takes one final snapshot so the partial interval
// since the last tick is not lost from the durable aggregate history.
func (w *Worker) Shutdown() {
	close(w.stop)
	w.wg.Wait()
	// The loop goroutine has exited (wg.Wait returned), so this final snapshot
	// runs single-threaded — no concurrent access to lastSeen.
	if err := w.rollupOnce(w.now()); err != nil {
		slog.Warn("rollup worker: final snapshot failed", "error", err)
	}
	slog.Info("rollup worker stopped")
}

func (w *Worker) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			if err := w.rollupOnce(w.now()); err != nil {
				slog.Warn("rollup worker: snapshot failed", "error", err)
			}
		}
	}
}

// sample is one gathered counter series: its family name, canonical label JSON,
// and current cumulative value.
type sample struct {
	metric string
	labels json.RawMessage
	value  float64
}

// rollupOnce gathers the tracked counters, computes each series' delta since the
// watermark, and upserts the delta into the current time bucket. Idempotent:
// re-running with an unchanged snapshot computes zero deltas and writes nothing.
func (w *Worker) rollupOnce(now time.Time) error {
	samples, err := w.snapshot()
	if err != nil {
		return err
	}
	bucketStart := now.UTC().Truncate(w.interval)
	bucketSeconds := int(w.interval / time.Second)

	for key, s := range samples {
		d := delta(s.value, w.lastSeen[key])
		w.lastSeen[key] = s.value
		if d == 0 {
			continue // nothing new for this series this tick
		}
		if err := w.upsert(s, bucketStart, bucketSeconds, d); err != nil {
			slog.Warn("rollup worker: upsert failed", "metric", s.metric, "error", err)
		}
	}
	return nil
}

// delta returns the per-bucket increment for a series given its current
// cumulative counter value and the last-seen watermark. A current value below the
// watermark means the counter was reset (process restart re-created the registry,
// or the series was otherwise recreated), so the current value IS the delta since
// the reset.
func delta(current, last float64) float64 {
	if current >= last {
		return current - last
	}
	return current
}

// snapshot gathers the current values of the tracked counter series, keyed by a
// deterministic series identity (metric name + canonical label JSON).
func (w *Worker) snapshot() (map[string]sample, error) {
	families, err := w.gatherer.Gather()
	if err != nil {
		return nil, err
	}
	out := make(map[string]sample)
	for _, fam := range families {
		name := fam.GetName()
		if !trackedMetrics[name] {
			continue
		}
		for _, m := range fam.GetMetric() {
			c := m.GetCounter()
			if c == nil {
				continue
			}
			labels := make(map[string]string, len(m.GetLabel()))
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			lj, key := canonicalLabels(name, labels)
			out[key] = sample{metric: name, labels: lj, value: c.GetValue()}
		}
	}
	return out, nil
}

// canonicalLabels returns the label set as sorted-key JSON (encoding/json emits
// map string keys in sorted order) and a deterministic series-identity key
// combining the metric name with that JSON. Sorted keys make equal series map to
// the same row on the unique index and the same watermark entry.
func canonicalLabels(metric string, labels map[string]string) (json.RawMessage, string) {
	lj, _ := json.Marshal(labels)
	return json.RawMessage(lj), metric + "\x00" + string(lj)
}

// upsert adds delta to the (metric_name, labels, bucket_start) rollup row,
// inserting it at delta if absent. The ON CONFLICT accumulates rather than
// overwrites, so successive ticks within a bucket sum, and a replayed snapshot
// (delta 0, filtered out before this call) never double-counts.
func (w *Worker) upsert(s sample, bucketStart time.Time, bucketSeconds int, d float64) error {
	row := models.PipelineRollup{
		MetricName:    s.metric,
		Labels:        s.labels,
		BucketStart:   bucketStart,
		BucketSeconds: bucketSeconds,
		Value:         d,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return w.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "metric_name"}, {Name: "labels"}, {Name: "bucket_start"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"value": gorm.Expr("pipeline_rollups.value + ?", d),
		}),
	}).Create(&row).Error
}
