package config

import (
	"testing"
	"time"
)

// OSI-7 — the internal large-message path derivation. These guard that every
// internal hop's time/size budget scales with the configured max message size
// and stays BOUNDED (a finite function of the size and a positive floor rate,
// never infinite), and that the queue reclaim interval always strictly exceeds
// the worst-case single-send budget so a slow legitimate large send is never
// reclaimed mid-flight and duplicated.

// TestInternalDeliveryDeadline_ScalesAndBounded is the core requirement: a larger
// configured max size yields a proportionally larger internal deadline, the value
// is always finite, and small sizes are floored so normal mail keeps a sane
// budget.
func TestInternalDeliveryDeadline_ScalesAndBounded(t *testing.T) {
	// 1 MiB/s floor (the default) keeps the arithmetic legible: N MiB ≈ N s + grace.
	c := &Config{}

	small := c.InternalDeliveryDeadline(1 * 1024 * 1024)    // 1 MiB
	medium := c.InternalDeliveryDeadline(64 * 1024 * 1024)  // 64 MiB
	large := c.InternalDeliveryDeadline(128 * 1024 * 1024)  // 128 MiB
	huge := c.InternalDeliveryDeadline(512 * 1024 * 1024)   // 512 MiB

	// Proportional growth: a bigger message gets a strictly larger budget.
	if !(small < medium && medium < large && large < huge) {
		t.Fatalf("deadline must grow with size: 1MiB=%v 64MiB=%v 128MiB=%v 512MiB=%v",
			small, medium, large, huge)
	}

	// Bounded and non-zero at every size (never infinite, never disabled).
	for _, d := range []time.Duration{small, medium, large, huge} {
		if d <= 0 {
			t.Fatalf("deadline must be positive, got %v", d)
		}
		if d > time.Hour {
			t.Fatalf("deadline for a sane max size must stay bounded well under 1h, got %v", d)
		}
	}

	// 128 MiB at 1 MiB/s + 30s grace ≈ 158s — the concrete worst case the fixed
	// 30s could never satisfy.
	if large < 128*time.Second || large > 200*time.Second {
		t.Errorf("128 MiB deadline = %v, want ~158s (grace + 128s)", large)
	}
}

// TestInternalDeliveryDeadline_Floor verifies a tiny/zero size still yields the
// minimum sane budget (never regressing the historical 30s internal timeout).
func TestInternalDeliveryDeadline_Floor(t *testing.T) {
	c := &Config{}
	for _, size := range []int64{0, 1, 1024, 512 * 1024} {
		if got := c.InternalDeliveryDeadline(size); got < minInternalDeliveryDeadline {
			t.Errorf("deadline(%d) = %v, want >= floor %v", size, got, minInternalDeliveryDeadline)
		}
	}
	// A negative size is clamped to 0, not an underflow.
	if got := c.InternalDeliveryDeadline(-1); got != minInternalDeliveryDeadline {
		t.Errorf("deadline(-1) = %v, want floor %v", got, minInternalDeliveryDeadline)
	}
}

// TestInternalDeliveryDeadline_FloorRateWidensBudget verifies a lower configured
// floor rate widens the derived budget (an operator with a slow internal link),
// and a higher one tightens it — always finite.
func TestInternalDeliveryDeadline_FloorRate(t *testing.T) {
	clearEnv(t)
	size := int64(128 * 1024 * 1024)

	t.Setenv("INTERNAL_DELIVERY_FLOOR_RATE", "262144") // 256 KiB/s (slow link)
	slow := (&Config{}).InternalDeliveryDeadline(size)

	t.Setenv("INTERNAL_DELIVERY_FLOOR_RATE", "8388608") // 8 MiB/s (fast link)
	fast := (&Config{}).InternalDeliveryDeadline(size)

	if slow <= fast {
		t.Fatalf("a slower floor rate must widen the budget: slow=%v fast=%v", slow, fast)
	}
	if fast <= 0 || slow <= 0 {
		t.Fatalf("budgets must stay positive/finite: slow=%v fast=%v", slow, fast)
	}
}

// TestInternalDeliveryFloorRate_FallsBackSecurely verifies a malformed or
// non-positive floor rate falls back to the default rather than producing a
// zero/negative rate (which would make the derived deadline infinite).
func TestInternalDeliveryFloorRate_FallsBackSecurely(t *testing.T) {
	c := &Config{}
	for _, v := range []string{"", "not-a-number", "0", "-5"} {
		clearEnv(t)
		if v != "" {
			t.Setenv("INTERNAL_DELIVERY_FLOOR_RATE", v)
		}
		if got := c.InternalDeliveryFloorRate(); got != DefaultInternalDeliveryFloorRate {
			t.Errorf("floor rate for %q = %d, want default %d", v, got, DefaultInternalDeliveryFloorRate)
		}
	}

	// A valid positive override is honored.
	clearEnv(t)
	t.Setenv("INTERNAL_DELIVERY_FLOOR_RATE", "2097152")
	if got := c.InternalDeliveryFloorRate(); got != 2097152 {
		t.Errorf("floor rate override = %d, want 2097152", got)
	}
}

// TestInternalDeliveryBodyLimit_AcceptsMax verifies the delivery body ceiling is
// always ABOVE the configured max message size (plus real MIME/JSON overhead), so
// a legitimate max-size message is never rejected, while still being a bounded
// finite multiple (not unlimited buffering).
func TestInternalDeliveryBodyLimit_AcceptsMax(t *testing.T) {
	for _, max := range []int64{
		DefaultSMTPMaxMessageSize,
		25 * 1024 * 1024,
		128 * 1024 * 1024,
	} {
		c := &Config{SMTPMaxMessageSize: max}
		limit := c.InternalDeliveryBodyLimit()
		// Must clear the raw max plus generous duplication+escaping overhead: the
		// deliver JSON carries the raw message AND its extracted text/html body.
		if limit < max*2 {
			t.Errorf("body limit %d for max %d does not leave room for content duplication", limit, max)
		}
		// Must stay a bounded finite multiple (not unlimited).
		if limit > max*4+2*1024*1024 {
			t.Errorf("body limit %d for max %d is larger than the bounded 3x+headroom design", limit, max)
		}
	}
}

// TestStaleDeliveringReclaim_ExceedsSendBudget is the anti-duplicate invariant:
// the queue reclaim interval must strictly exceed the worst-case single max-size
// send budget, so an in-flight slow large send completes before it is reclaimed.
func TestStaleDeliveringReclaim_ExceedsSendBudget(t *testing.T) {
	cases := []int64{
		DefaultSMTPMaxMessageSize, // 10 MiB — reclaim should stay at the 15m baseline
		128 * 1024 * 1024,         // 128 MiB — still under baseline
		4 * 1024 * 1024 * 1024,    // 4 GiB — forces reclaim above the baseline
	}
	for _, max := range cases {
		c := &Config{SMTPMaxMessageSize: max}
		reclaim := c.StaleDeliveringReclaim()
		sendBudget := c.InternalDeliveryDeadline(max)
		if reclaim <= sendBudget {
			t.Errorf("max=%d: reclaim %v must strictly exceed max-size send budget %v", max, reclaim, sendBudget)
		}
		// Never shorter than the historical 15-minute baseline.
		if reclaim < defaultStaleDeliveringReclaim {
			t.Errorf("max=%d: reclaim %v must be >= baseline %v", max, reclaim, defaultStaleDeliveringReclaim)
		}
	}

	// For a normal deployment the derived reclaim stays exactly at the baseline
	// (no behavior change for small mail).
	c := &Config{SMTPMaxMessageSize: DefaultSMTPMaxMessageSize}
	if got := c.StaleDeliveringReclaim(); got != defaultStaleDeliveringReclaim {
		t.Errorf("normal-size reclaim = %v, want unchanged baseline %v", got, defaultStaleDeliveringReclaim)
	}

	// For a very large max, reclaim must actually have grown above the baseline.
	big := &Config{SMTPMaxMessageSize: 4 * 1024 * 1024 * 1024}
	if got := big.StaleDeliveringReclaim(); got <= defaultStaleDeliveringReclaim {
		t.Errorf("4 GiB reclaim = %v, want grown above baseline %v", got, defaultStaleDeliveringReclaim)
	}
}
