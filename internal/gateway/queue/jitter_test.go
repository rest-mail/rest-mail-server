package queue

import (
	"testing"
	"time"
)

// TestComputeBackoffWithJitter is the red-green guard for the "retry jitter"
// backlog item. Without jitter every message deferred by the same outage retries
// in lockstep; the jittered backoff must (a) stay within ±20% of the base delay,
// (b) never be negative or exceed the 4h cap, and (c) actually vary between calls
// (a no-jitter implementation returns a constant and fails this).
func TestComputeBackoffWithJitter(t *testing.T) {
	const maxBackoff = 4 * time.Hour

	t.Run("within band and varies for a mid attempt", func(t *testing.T) {
		const attempt = 4 // base = 16m
		base := computeBackoff(attempt)
		lo := time.Duration(float64(base) * 0.80)
		hi := time.Duration(float64(base) * 1.20)

		seen := map[time.Duration]struct{}{}
		for i := 0; i < 200; i++ {
			got := computeBackoffWithJitter(attempt)
			if got < lo || got > hi {
				t.Fatalf("computeBackoffWithJitter(%d) = %s, outside ±20%% band [%s, %s]",
					attempt, got, lo, hi)
			}
			if got <= 0 {
				t.Fatalf("computeBackoffWithJitter(%d) = %s, must be positive", attempt, got)
			}
			seen[got] = struct{}{}
		}
		if len(seen) < 2 {
			t.Fatalf("computeBackoffWithJitter produced no variation across 200 calls (jitter absent)")
		}
	})

	t.Run("never exceeds cap even at max attempt", func(t *testing.T) {
		for i := 0; i < 500; i++ {
			got := computeBackoffWithJitter(30) // base clamps to 4h
			if got > maxBackoff {
				t.Fatalf("computeBackoffWithJitter(30) = %s exceeds cap %s", got, maxBackoff)
			}
			if got <= 0 {
				t.Fatalf("computeBackoffWithJitter(30) = %s, must be positive", got)
			}
		}
	})
}
