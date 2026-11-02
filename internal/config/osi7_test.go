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

	small := c.InternalDeliveryDeadline(1 * 1024 * 1024)   // 1 MiB
	medium := c.InternalDeliveryDeadline(64 * 1024 * 1024) // 64 MiB
	large := c.InternalDeliveryDeadline(128 * 1024 * 1024) // 128 MiB
	huge := c.InternalDeliveryDeadline(512 * 1024 * 1024)  // 512 MiB

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

// TestStaleDeliveringReclaim_ExceedsLongestStep is the anti-duplicate invariant:
// the queue reclaim interval must strictly exceed the longest single SMTP step. It
// no longer scales with the message's size (#714): the worker holding a row
// refreshes it for as long as the attempt runs.
func TestStaleDeliveringReclaim_ExceedsLongestStep(t *testing.T) {
	cases := []int64{
		DefaultSMTPMaxMessageSize, // 10 MiB
		128 * 1024 * 1024,         // 128 MiB
		4 * 1024 * 1024 * 1024,    // 4 GiB
	}
	for _, max := range cases {
		c := &Config{SMTPMaxMessageSize: max}
		if got, want := c.StaleDeliveringReclaim(), 12*time.Minute; got != want {
			t.Errorf("max=%d: reclaim = %v, want %v", max, got, want)
		}
	}

	// A longer step raises it; shorter ones lower it, and it stays above each.
	raised := &Config{QueueSMTPTimeouts: OutboundSMTPTimeouts{FinalReply: 30 * time.Minute}}
	if got, want := raised.StaleDeliveringReclaim(), 32*time.Minute; got != want {
		t.Errorf("reclaim with a 30m final reply = %v, want %v", got, want)
	}
	short := time.Minute
	lowered := &Config{QueueSMTPTimeouts: OutboundSMTPTimeouts{Greeting: short, Mail: short, Rcpt: short, Data: short,
		DataBlock: short, FinalReply: short, Hello: short, StartTLS: short, Auth: short, Quit: short}}
	if got, want := lowered.StaleDeliveringReclaim(), 3*time.Minute; got != want {
		t.Errorf("reclaim with every step at 1m = %v, want %v", got, want)
	}
}

// RFC 5321 §4.5.3.2's minimums are the defaults, and each of its six timers has a
// setting ("Timeouts SHOULD be easily reconfigurable").
func TestQueueSMTPTimeouts_DefaultsAndSettings(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENVIRONMENT", "development")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.QueueSMTPTimeouts, DefaultOutboundSMTPTimeouts(); got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	want := OutboundSMTPTimeouts{
		Greeting: 5 * time.Minute, Mail: 5 * time.Minute, Rcpt: 5 * time.Minute, Data: 2 * time.Minute,
		DataBlock: 3 * time.Minute, FinalReply: 10 * time.Minute,
		Hello: 5 * time.Minute, StartTLS: 5 * time.Minute, Auth: 5 * time.Minute, Quit: 5 * time.Minute,
	}
	if cfg.QueueSMTPTimeouts != want {
		t.Fatalf("defaults = %+v, want the RFC's %+v", cfg.QueueSMTPTimeouts, want)
	}

	for key, read := range map[string]func(OutboundSMTPTimeouts) time.Duration{
		"QUEUE_SMTP_GREETING_TIMEOUT":    func(t OutboundSMTPTimeouts) time.Duration { return t.Greeting },
		"QUEUE_SMTP_MAIL_TIMEOUT":        func(t OutboundSMTPTimeouts) time.Duration { return t.Mail },
		"QUEUE_SMTP_RCPT_TIMEOUT":        func(t OutboundSMTPTimeouts) time.Duration { return t.Rcpt },
		"QUEUE_SMTP_DATA_TIMEOUT":        func(t OutboundSMTPTimeouts) time.Duration { return t.Data },
		"QUEUE_SMTP_DATA_BLOCK_TIMEOUT":  func(t OutboundSMTPTimeouts) time.Duration { return t.DataBlock },
		"QUEUE_SMTP_FINAL_REPLY_TIMEOUT": func(t OutboundSMTPTimeouts) time.Duration { return t.FinalReply },
	} {
		clearEnv(t)
		t.Setenv("ENVIRONMENT", "development")
		t.Setenv(key, "37m")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if got := read(cfg.QueueSMTPTimeouts); got != 37*time.Minute {
			t.Errorf("%s=37m read as %v", key, got)
		}
		if got := cfg.StaleDeliveringReclaim(); got != 39*time.Minute {
			t.Errorf("%s=37m: reclaim = %v, want 39m", key, got)
		}
		for _, bad := range []string{"soon", "300", "0s", "-1m"} {
			t.Setenv(key, bad)
			if _, err := Load(); err == nil {
				t.Errorf("Load() with %s=%q should fail", key, bad)
			}
		}
	}
}
