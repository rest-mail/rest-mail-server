package queue

import (
	"testing"
	"time"
)

// OSI-7 — the outbound queue's per-attempt send budget must scale with each
// message's size, and the reclaim interval must always strictly exceed the
// worst-case send budget so a slow legitimate large send is never reclaimed
// mid-flight and delivered twice.

// TestNewWorker_DeliveryDeadlineDefaults verifies a fresh worker preserves the
// historical fixed timing until configured: 30s per send, 15m reclaim.
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

// TestReclaimExceedsSendBudget is the anti-duplicate invariant at the worker
// level: with a realistic size-aware budget, the configured reclaim strictly
// exceeds the worst-case max-size send so an in-flight large send is safe.
func TestReclaimExceedsSendBudget(t *testing.T) {
	const maxSize = int64(128 << 20) // 128 MiB
	// Mirror the config derivation: grace + size/floorRate, reclaim = max(15m, budget+margin).
	budget := func(sizeBytes int64) time.Duration {
		return 30*time.Second + time.Duration(sizeBytes/(1<<20))*time.Second
	}
	reclaim := 15 * time.Minute
	if d := budget(maxSize) + 5*time.Minute; d > reclaim {
		reclaim = d
	}

	w := NewWorker(nil, "mx.local", 1, time.Second)
	w.SetDeliveryDeadline(budget, reclaim)

	if w.reclaim <= w.sendDeadline(maxSize) {
		t.Fatalf("reclaim %v must strictly exceed max-size send budget %v", w.reclaim, w.sendDeadline(maxSize))
	}
}
