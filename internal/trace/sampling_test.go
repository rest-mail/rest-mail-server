package trace

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/metrics"
)

// drain empties the recorder's buffer without a running loop, returning the
// enqueued traces so a test can inspect exactly which Records the sampling gate
// admitted.
func drain(r *Recorder) []models.MessageTrace {
	var out []models.MessageTrace
	for {
		select {
		case t := <-r.ch:
			out = append(out, t)
		default:
			return out
		}
	}
}

// TestSampling_AnomaliesAlwaysRecorded proves that with sampling fully off
// (rate 0.0) every non-continue outcome is still recorded 100%, while the happy
// path is entirely dropped. The loop is not started, so admitted traces sit in
// the buffer.
func TestSampling_AnomaliesAlwaysRecorded(t *testing.T) {
	anomalies := []string{outcomeRejected, outcomeQuarantined, outcomeDiscarded, outcomeDeferred}

	r := newRecorder(nil, 1024, 16, time.Hour)
	r.sampleRate = 0.0 // keep only anomalies
	r.randFloat = func() float64 { return 0.0 }

	for _, o := range anomalies {
		r.Record(models.MessageTrace{Outcome: o})
	}
	// Happy path at rate 0.0 → all dropped by sampling.
	for i := 0; i < 50; i++ {
		r.Record(models.MessageTrace{Outcome: outcomeDelivered})
		r.Record(models.MessageTrace{Outcome: outcomeQueued})
	}

	got := drain(r)
	if len(got) != len(anomalies) {
		t.Fatalf("recorded %d traces, want %d (only anomalies at rate 0.0)", len(got), len(anomalies))
	}
	for _, tr := range got {
		if tr.Outcome == outcomeDelivered || tr.Outcome == outcomeQueued {
			t.Errorf("happy-path outcome %q recorded at rate 0.0, want dropped", tr.Outcome)
		}
		if !tr.Sampled {
			t.Errorf("anomaly %q recorded with Sampled=false, want true", tr.Outcome)
		}
	}
}

// TestSampling_HappyPathRespectsRate drives the gate with a deterministic RNG so
// the kept count is exact: a repeating [below, above] sequence against rate 0.5
// keeps exactly half of the happy-path traces.
func TestSampling_HappyPathRespectsRate(t *testing.T) {
	r := newRecorder(nil, 4096, 16, time.Hour)
	r.sampleRate = 0.5
	// Alternate a value below the rate (kept) and above it (dropped).
	seq := []float64{0.1, 0.9}
	i := 0
	r.randFloat = func() float64 {
		v := seq[i%len(seq)]
		i++
		return v
	}

	const n = 1000
	for k := 0; k < n; k++ {
		r.Record(models.MessageTrace{Outcome: outcomeDelivered})
	}
	got := drain(r)
	if len(got) != n/2 {
		t.Errorf("kept %d of %d happy-path traces at rate 0.5 with alternating RNG, want %d", len(got), n, n/2)
	}
}

// TestSampling_RateOneKeepsAll confirms rate 1.0 admits every happy-path trace
// (randFloat is in [0,1) so it is always < 1.0) and records no drop.
func TestSampling_RateOneKeepsAll(t *testing.T) {
	r := newRecorder(nil, 4096, 16, time.Hour) // default sampleRate is 1.0
	before := testutil.ToFloat64(metrics.TraceDropped)

	const n = 500
	for k := 0; k < n; k++ {
		r.Record(models.MessageTrace{Outcome: outcomeDelivered})
	}
	if got := len(drain(r)); got != n {
		t.Errorf("kept %d of %d happy-path traces at rate 1.0, want all", got, n)
	}
	// Sampling-out must never be accounted as a backpressure drop.
	if d := testutil.ToFloat64(metrics.TraceDropped) - before; d != 0 {
		t.Errorf("trace_dropped increased by %v during rate-1.0 sampling, want 0", d)
	}
}

// TestSampling_NotADrop confirms a sampled-out happy-path trace is silent: it is
// neither buffered nor counted as a drop (the aggregate already counted it).
func TestSampling_NotADrop(t *testing.T) {
	r := newRecorder(nil, 16, 16, time.Hour)
	r.sampleRate = 0.0
	r.randFloat = func() float64 { return 0.0 }

	before := testutil.ToFloat64(metrics.TraceDropped)
	for i := 0; i < 100; i++ {
		r.Record(models.MessageTrace{Outcome: outcomeDelivered})
	}
	if d := testutil.ToFloat64(metrics.TraceDropped) - before; d != 0 {
		t.Errorf("trace_dropped increased by %v for sampled-out traces, want 0 (sampling is not a drop)", d)
	}
	if got := len(drain(r)); got != 0 {
		t.Errorf("buffered %d sampled-out traces, want 0", got)
	}
}

// TestSampling_StampsExpiresAt confirms a recorded trace gets its retention
// horizon stamped (ExpiresAt = CreatedAt + retention) and that a zero retention
// leaves ExpiresAt nil (the pre-PR4 default).
func TestSampling_StampsExpiresAt(t *testing.T) {
	fixed := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

	r := newRecorder(nil, 16, 16, time.Hour)
	r.retention = 7 * 24 * time.Hour
	r.now = func() time.Time { return fixed }

	r.Record(models.MessageTrace{Outcome: outcomeRejected})
	got := drain(r)
	if len(got) != 1 {
		t.Fatalf("recorded %d traces, want 1", len(got))
	}
	if got[0].ExpiresAt == nil {
		t.Fatal("ExpiresAt is nil, want a stamped horizon")
	}
	if want := fixed.Add(7 * 24 * time.Hour); !got[0].ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v (CreatedAt + retention)", got[0].ExpiresAt, want)
	}

	// Zero retention leaves the horizon unset.
	r2 := newRecorder(nil, 16, 16, time.Hour) // retention defaults to 0
	r2.Record(models.MessageTrace{Outcome: outcomeRejected})
	got2 := drain(r2)
	if len(got2) != 1 || got2[0].ExpiresAt != nil {
		t.Errorf("zero-retention ExpiresAt = %v, want nil", got2[0].ExpiresAt)
	}
}

// Bounded outcome vocabulary used by the sampling tests; mirrors the handler
// constants (kept here so the trace package's tests don't import handlers).
const (
	outcomeRejected    = "rejected"
	outcomeQuarantined = "quarantined"
	outcomeDiscarded   = "discarded"
	outcomeDeferred    = "deferred"
)
