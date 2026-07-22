package queue

import (
	"testing"
	"time"
)

func TestComputeBackoff(t *testing.T) {
	const maxBackoff = 4 * time.Hour

	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 1 * time.Minute},
		{1, 2 * time.Minute},
		{2, 4 * time.Minute},
		{3, 8 * time.Minute},
		{7, 128 * time.Minute},
		{8, maxBackoff},  // 2^8 = 256m > 4h → clamp
		{30, maxBackoff}, // used to overflow via math.Pow -> negative
		{40, maxBackoff}, // 2^40 minutes overflows int64 -> negative
		{63, maxBackoff},
		{-1, 1 * time.Minute}, // defensive
	}

	for _, c := range cases {
		got := computeBackoff(c.attempts)
		if got != c.want {
			t.Errorf("computeBackoff(%d) = %s, want %s", c.attempts, got, c.want)
		}
		// The core invariant the overflow violated: never negative, never > cap.
		if got < 0 {
			t.Errorf("computeBackoff(%d) is negative (%s)", c.attempts, got)
		}
		if got > maxBackoff {
			t.Errorf("computeBackoff(%d) = %s exceeds cap %s", c.attempts, got, maxBackoff)
		}
	}
}
