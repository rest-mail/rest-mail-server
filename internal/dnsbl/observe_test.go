package dnsbl

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/restmail/restmail/internal/metrics"
)

// logRecords is a slog.Handler that keeps every record.
type logRecords struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (l *logRecords) Enabled(context.Context, slog.Level) bool { return true }
func (l *logRecords) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *logRecords) WithGroup(string) slog.Handler            { return l }

func (l *logRecords) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	l.recs = append(l.recs, r.Clone())
	l.mu.Unlock()
	return nil
}

// at returns the messages logged at level, in order.
func (l *logRecords) at(level slog.Level) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, r := range l.recs {
		if r.Level == level {
			out = append(out, r.Message)
		}
	}
	return out
}

type observerMetrics struct {
	checks             *prometheus.CounterVec
	listed, lastAnswer *prometheus.GaugeVec
}

// newObserver returns a MetricsObserver over vectors registered in a registry
// of their own, so nothing touches the process-wide one.
func newObserver(t *testing.T) (*MetricsObserver, observerMetrics, *logRecords) {
	t.Helper()
	om := observerMetrics{checks: metrics.NewDNSBLChecks(), listed: metrics.NewDNSBLListed(), lastAnswer: metrics.NewDNSBLLastAnswer()}
	prometheus.NewPedanticRegistry().MustRegister(om.checks, om.listed, om.lastAnswer)
	logs := &logRecords{}
	return NewMetricsObserver(slog.New(logs), om.checks, om.listed, om.lastAnswer), om, logs
}

func (om observerMetrics) count(provider string, st Status) float64 {
	return testutil.ToFloat64(om.checks.WithLabelValues(provider, string(st)))
}

// TestMetricsObserverErrorsNeverClearAListing runs the real Monitor: a known
// listing, then every kind of non-answer, then a clean answer. There must be
// exactly one listing and one recovery, and the listing value must stay 1 (with
// its answer time unchanged) until the clean answer.
func TestMetricsObserverErrorsNeverClearAListing(t *testing.T) {
	obs, om, logs := newObserver(t)
	s := &scripted{}
	s.set(StatusListed)
	m, clock := newTestMonitor(t, s, testConfig(obs, sendingIP))
	start(t, m)

	tm := clock.nextTimer(t) // pass 1 (listed) is done
	listedAt := clock.Now().Unix()
	if got := testutil.ToFloat64(om.listed.WithLabelValues(sendingIP, "zen-like")); got != 1 {
		t.Fatalf("listed after a listing = %v, want 1", got)
	}
	if got := testutil.ToFloat64(om.lastAnswer.WithLabelValues(sendingIP, "zen-like")); got != float64(listedAt) {
		t.Fatalf("last answer = %v, want %d", got, listedAt)
	}

	for _, st := range []Status{StatusProviderError, StatusIndeterminate, StatusUnsupported} {
		s.set(st)
		clock.fire(tm)
		tm = clock.nextTimer(t)
		if got := testutil.ToFloat64(om.listed.WithLabelValues(sendingIP, "zen-like")); got != 1 {
			t.Fatalf("listed after %s = %v, want it still 1", st, got)
		}
		if got := testutil.ToFloat64(om.lastAnswer.WithLabelValues(sendingIP, "zen-like")); got != float64(listedAt) {
			t.Fatalf("last answer after %s = %v, want it unchanged at %d", st, got, listedAt)
		}
		if got := logs.at(slog.LevelInfo); len(got) != 0 {
			t.Fatalf("%s logged %v; a non-answer is not a recovery", st, got)
		}
	}

	s.set(StatusNotListed)
	clock.fire(tm)
	clock.nextTimer(t)
	if got := testutil.ToFloat64(om.listed.WithLabelValues(sendingIP, "zen-like")); got != 0 {
		t.Fatalf("listed after a clean answer = %v, want 0", got)
	}
	if got := testutil.ToFloat64(om.lastAnswer.WithLabelValues(sendingIP, "zen-like")); got != float64(clock.Now().Unix()) {
		t.Fatalf("last answer = %v, want the clean answer's time", got)
	}

	if got := logs.at(slog.LevelWarn); len(got) != 1 || !strings.Contains(got[0], "listed") {
		t.Fatalf("warnings = %v, want one listing", got)
	}
	if got := logs.at(slog.LevelInfo); len(got) != 1 || !strings.Contains(got[0], "no longer listed") {
		t.Fatalf("info lines = %v, want one recovery", got)
	}
	for _, st := range []Status{StatusListed, StatusProviderError, StatusIndeterminate, StatusUnsupported, StatusNotListed} {
		if got := om.count("zen-like", st); got != 1 {
			t.Errorf("checks{status=%s} = %v, want 1", st, got)
		}
	}
	if n := testutil.CollectAndCount(om.listed); n != 1 {
		t.Errorf("listed has %d series, want 1 (one IP, one provider)", n)
	}
}

// TestMetricsObserverUnknownStaysUnknown: a pair no list has answered for has
// no listing value at all, never 0, however many non-answers it has had.
func TestMetricsObserverUnknownStaysUnknown(t *testing.T) {
	obs, om, logs := newObserver(t)
	s := &scripted{}
	s.set(StatusProviderError)
	m, clock := newTestMonitor(t, s, testConfig(obs, sendingIP))
	start(t, m)

	tm := clock.nextTimer(t)
	s.set(StatusIndeterminate)
	clock.fire(tm)
	clock.nextTimer(t)

	if n := testutil.CollectAndCount(om.listed); n != 0 {
		t.Fatalf("listed has %d series after only non-answers, want none (unknown)", n)
	}
	if n := testutil.CollectAndCount(om.lastAnswer); n != 0 {
		t.Fatalf("last answer has %d series after only non-answers, want none", n)
	}
	if om.count("zen-like", StatusProviderError) != 1 || om.count("zen-like", StatusIndeterminate) != 1 {
		t.Fatal("the non-answers were not counted")
	}
	if got := append(logs.at(slog.LevelWarn), logs.at(slog.LevelInfo)...); len(got) != 0 {
		t.Fatalf("logged %v for non-answers", got)
	}
}

// TestMetricsObserverFirstCleanIsZero: a first clean answer is known clean (0)
// but is not a recovery.
func TestMetricsObserverFirstCleanIsZero(t *testing.T) {
	obs, om, logs := newObserver(t)
	s := &scripted{}
	s.set(StatusNotListed)
	m, clock := newTestMonitor(t, s, testConfig(obs, sendingIP))
	start(t, m)
	clock.nextTimer(t)

	if got := testutil.ToFloat64(om.listed.WithLabelValues(sendingIP, "zen-like")); got != 0 {
		t.Fatalf("listed = %v, want 0", got)
	}
	if got := logs.at(slog.LevelInfo); len(got) != 0 {
		t.Fatalf("logged %v; a first clean answer is not a recovery", got)
	}
}
