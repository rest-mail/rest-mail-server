package queue

import (
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
)

// OSI-7 — the budget of the RESTMAIL hop's HTTPS requests must scale with each
// message's size. #714 — the reclaim interval must exceed the longest single SMTP
// step, so a slow legitimate step is never reclaimed mid-flight and delivered
// twice.

// TestNewWorker_DeliveryDeadlineDefaults verifies a fresh worker's timing until
// configured: 30s per RESTMAIL request, reclaim at the longest step plus a margin.
func TestNewWorker_DeliveryDeadlineDefaults(t *testing.T) {
	w := NewWorker(nil, "mx.local", 1, time.Second)
	if w.reclaim != staleDeliveringTimeout {
		t.Errorf("default reclaim = %v, want %v", w.reclaim, staleDeliveringTimeout)
	}
	if w.sendDeadline == nil {
		t.Fatal("sendDeadline must be defaulted, got nil")
	}
	// The default budget ignores size and returns the historical 30s.
	for _, size := range []int64{0, 1 << 10, 1 << 30} {
		if got := w.sendDeadline(size); got != 30*time.Second {
			t.Errorf("default sendDeadline(%d) = %v, want 30s", size, got)
		}
	}
}

// TestSetDeliveryDeadline_WiresAndGuards verifies the setter installs a
// size-aware budget and reclaim, and rejects a nil function or non-positive
// reclaim so a misconfiguration can never shorten reclaim below a send budget.
func TestSetDeliveryDeadline_WiresAndGuards(t *testing.T) {
	w := NewWorker(nil, "mx.local", 1, time.Second)

	sizeAware := func(sizeBytes int64) time.Duration {
		return 30*time.Second + time.Duration(sizeBytes/(1<<20))*time.Second
	}
	w.SetDeliveryDeadline(sizeAware, 20*time.Minute)

	if got := w.sendDeadline(128 << 20); got != 30*time.Second+128*time.Second {
		t.Errorf("size-aware sendDeadline(128MiB) = %v, want 158s", got)
	}
	if w.reclaim != 20*time.Minute {
		t.Errorf("reclaim = %v, want 20m", w.reclaim)
	}

	// A nil function keeps the previously-installed one (not a panic on the next
	// send); a non-positive reclaim keeps the previous value.
	w.SetDeliveryDeadline(nil, 0)
	if w.sendDeadline == nil {
		t.Fatal("nil deadlineFn must be ignored, sendDeadline became nil")
	}
	if w.reclaim != 20*time.Minute {
		t.Errorf("non-positive reclaim must be ignored, reclaim = %v want 20m", w.reclaim)
	}
}

// TestReclaimExceedsLongestStep is the anti-duplicate invariant at the worker
// level: the reclaim the config derives exceeds the longest SMTP step, whatever
// the step is set to, and does not depend on the message's size.
func TestReclaimExceedsLongestStep(t *testing.T) {
	for _, timers := range []config.OutboundSMTPTimeouts{
		{},
		{FinalReply: 45 * time.Minute},
		{Greeting: 30 * time.Minute, FinalReply: time.Minute},
	} {
		c := &config.Config{QueueSMTPTimeouts: timers}
		w := NewWorker(nil, "mx.local", 1, time.Second)
		w.SetSMTPTimeouts(c.QueueSMTPTimeouts)
		w.SetDeliveryDeadline(c.InternalDeliveryDeadline, c.StaleDeliveringReclaim())

		if w.reclaim <= w.timeouts().Longest() {
			t.Errorf("reclaim %v must strictly exceed the longest step %v", w.reclaim, w.timeouts().Longest())
		}
	}
}
