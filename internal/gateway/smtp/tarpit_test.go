package smtp

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

// TestTarpitDelay exercises the pure escalation model: nothing at or below the
// soft limit, linear escalation past it, and a hard cap at max.
func TestTarpitDelay(t *testing.T) {
	const (
		base = time.Second
		max  = 15 * time.Second
		soft = 2
	)
	cases := []struct {
		name                string
		errCount, softLimit int
		base, max, want     time.Duration
	}{
		{"zero errors", 0, soft, base, max, 0},
		{"below soft limit", 1, soft, base, max, 0},
		{"at soft limit", 2, soft, base, max, 0},
		{"one past soft limit", 3, soft, base, max, 1 * time.Second},
		{"two past soft limit", 4, soft, base, max, 2 * time.Second},
		{"escalating", 10, soft, base, max, 8 * time.Second},
		{"exactly at cap", 17, soft, base, max, 15 * time.Second},
		{"past cap is capped", 20, soft, base, max, 15 * time.Second},
		{"far past cap is capped", 1000, soft, base, max, 15 * time.Second},
		{"zero soft limit delays from first error", 1, 0, base, max, 1 * time.Second},
		{"base zero disables", 5, soft, 0, max, 0},
		{"max zero disables", 5, soft, base, 0, 0},
		{"huge count no overflow", 1_000_000_000, soft, base, max, 15 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tarpitDelay(tc.errCount, tc.softLimit, tc.base, tc.max); got != tc.want {
				t.Errorf("tarpitDelay(%d, %d, %v, %v) = %v, want %v",
					tc.errCount, tc.softLimit, tc.base, tc.max, got, tc.want)
			}
		})
	}
}

// TestTarpitPolicyDelayFor confirms a disabled policy never delays, and an
// enabled one delegates to the pure model.
func TestTarpitPolicyDelayFor(t *testing.T) {
	enabled := tarpitPolicy{enabled: true, base: time.Second, softLimit: 2, max: 15 * time.Second}
	if got := enabled.delayFor(1); got != 0 {
		t.Errorf("enabled.delayFor(1) = %v, want 0", got)
	}
	if got := enabled.delayFor(4); got != 2*time.Second {
		t.Errorf("enabled.delayFor(4) = %v, want 2s", got)
	}

	disabled := tarpitPolicy{enabled: false, base: time.Second, softLimit: 2, max: 15 * time.Second}
	if got := disabled.delayFor(100); got != 0 {
		t.Errorf("disabled.delayFor(100) = %v, want 0 (disabled must never delay)", got)
	}
}

// TestTarpitSleepAbortsOnCancelledContext proves a sleep started with an
// already-cancelled context returns at once — a torn-down connection or server
// shutdown never hangs the goroutine.
func TestTarpitSleepAbortsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		tarpitSleep(ctx, 10*time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tarpitSleep did not abort on an already-cancelled context")
	}
}

// TestTarpitSleepAbortsWhenCancelledMidSleep proves cancellation during the
// sleep unblocks it promptly (the shutdown path).
func TestTarpitSleepAbortsWhenCancelledMidSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	tarpitSleep(ctx, 30*time.Second)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("tarpitSleep did not abort on mid-sleep cancel, took %v", elapsed)
	}
}

// sleepRecorder is a fake tarpit sleeper: it records the requested delays and
// returns immediately, so session-level escalation is asserted deterministically
// without real multi-second sleeps.
type sleepRecorder struct {
	mu    sync.Mutex
	calls []time.Duration
}

func (r *sleepRecorder) sleep(_ context.Context, d time.Duration) {
	r.mu.Lock()
	r.calls = append(r.calls, d)
	r.mu.Unlock()
}

func (r *sleepRecorder) snapshot() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.calls...)
}

// TestSessionRcptTarpitEscalates drives a session through five invalid inbound
// RCPTs and asserts the tarpit stays silent until the soft limit is crossed,
// then escalates linearly on each subsequent rejection.
func TestSessionRcptTarpitEscalates(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()
	rec := &sleepRecorder{}
	h := newSMTPHarness(t, back, store, false, func(s *Server) {
		s.SetTarpitPolicy(true, time.Second, 2, 15*time.Second)
		s.tarpitSleep = rec.sleep
	})

	h.ehlo()
	if r := h.cmd("MAIL FROM:<spammer@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL = %q, want 250", r)
	}
	for i := 0; i < 5; i++ {
		if r := h.cmd("RCPT TO:<nobody%d@example.com>", i); replyCode(r) != "550" {
			t.Fatalf("RCPT %d = %q, want 550", i, r)
		}
	}

	got := rec.snapshot()
	want := []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RCPT tarpit delays = %v, want %v (first two rejections are within the soft limit)", got, want)
	}
}

// TestSessionRcptTarpitCleanSession proves a legitimate session (valid
// recipients only) is never tarpitted.
func TestSessionRcptTarpitCleanSession(t *testing.T) {
	back := newMockBackend()
	back.local["alice@example.com"] = true
	back.local["bob@example.com"] = true
	store := newMockStore()
	rec := &sleepRecorder{}
	h := newSMTPHarness(t, back, store, false, func(s *Server) {
		s.SetTarpitPolicy(true, time.Second, 2, 15*time.Second)
		s.tarpitSleep = rec.sleep
	})

	h.ehlo()
	if r := h.cmd("MAIL FROM:<friend@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL = %q", r)
	}
	for _, to := range []string{"alice@example.com", "bob@example.com"} {
		if r := h.cmd("RCPT TO:<%s>", to); replyCode(r) != "250" {
			t.Fatalf("RCPT %s = %q, want 250", to, r)
		}
	}

	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("clean session must not be tarpitted, got delays %v", got)
	}
}

// TestSessionRcptTarpitDisabled proves that with the policy disabled, even a
// flood of invalid RCPTs incurs no delay.
func TestSessionRcptTarpitDisabled(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()
	rec := &sleepRecorder{}
	h := newSMTPHarness(t, back, store, false, func(s *Server) {
		s.SetTarpitPolicy(false, time.Second, 2, 15*time.Second)
		s.tarpitSleep = rec.sleep
	})

	h.ehlo()
	h.cmd("MAIL FROM:<spammer@remote.test>")
	for i := 0; i < 5; i++ {
		if r := h.cmd("RCPT TO:<nobody%d@example.com>", i); replyCode(r) != "550" {
			t.Fatalf("RCPT %d = %q, want 550", i, r)
		}
	}

	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("disabled tarpit must not delay, got %v", got)
	}
}

// TestSessionAuthTarpitEscalates proves repeated AUTH failures on one
// connection feed the same escalating tarpit (the submission/brute-force path).
func TestSessionAuthTarpitEscalates(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()
	rec := &sleepRecorder{}
	h := newSMTPHarness(t, back, store, true, func(s *Server) {
		s.SetTarpitPolicy(true, time.Second, 2, 15*time.Second)
		s.tarpitSleep = rec.sleep
	})

	h.ehlo()
	// Four bad-password attempts stay under the connlimiter's default ban
	// threshold (5), so each is a 535 that increments the tarpit counter.
	for i := 0; i < 4; i++ {
		if r := h.authPlain(back.user, "wrong-password"); replyCode(r) != "535" {
			t.Fatalf("AUTH attempt %d = %q, want 535", i, r)
		}
	}

	got := rec.snapshot()
	want := []time.Duration{1 * time.Second, 2 * time.Second}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AUTH tarpit delays = %v, want %v", got, want)
	}
}

// TestServerSetTarpitPolicy verifies the setter's guardrails: disabling wins,
// invalid values are ignored (policy kept), and valid values take effect.
func TestServerSetTarpitPolicy(t *testing.T) {
	s := NewServer("smtp.test", newMockBackend(), nil, newMockStore(), nil)

	// Default policy is enabled and escalates.
	if got := s.tarpit.delayFor(3); got != 1*time.Second {
		t.Fatalf("default policy delayFor(3) = %v, want 1s", got)
	}

	// Disable: no delay regardless of count.
	s.SetTarpitPolicy(false, time.Second, 2, 15*time.Second)
	if got := s.tarpit.delayFor(100); got != 0 {
		t.Fatalf("disabled delayFor(100) = %v, want 0", got)
	}

	// Valid re-enable takes effect.
	s.SetTarpitPolicy(true, 2*time.Second, 1, 10*time.Second)
	if got := s.tarpit.delayFor(2); got != 2*time.Second {
		t.Fatalf("re-enabled delayFor(2) = %v, want 2s", got)
	}

	// Invalid (max < base) is ignored: the previous valid policy is kept.
	s.SetTarpitPolicy(true, 5*time.Second, 1, time.Second)
	if got := s.tarpit.delayFor(2); got != 2*time.Second {
		t.Fatalf("after invalid update delayFor(2) = %v, want 2s (kept)", got)
	}
}
