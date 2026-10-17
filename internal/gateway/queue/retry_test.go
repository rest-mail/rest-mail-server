package queue

import (
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

var transientCategories = []models.FailureCategory{
	models.FailureMailboxFull, models.FailureGreylisted, models.FailureRateLimited,
	models.FailureConnection, models.FailureTLS, models.FailureDNS, models.FailureOtherTransient,
}

// Each schedule, as delays after the first, second, … failed attempt: the
// first retry, doubling, then the cap however many attempts there have been
// (a large count once overflowed into a negative delay).
func TestRetrySchedules(t *testing.T) {
	cases := []struct {
		category models.FailureCategory
		delays   []time.Duration // after attempts 1, 2, 3, …
		max      time.Duration
		maxAge   time.Duration
	}{
		{models.FailureGreylisted,
			[]time.Duration{7 * time.Minute, 14 * time.Minute, 28 * time.Minute, 56 * time.Minute, 112 * time.Minute, 224 * time.Minute, 4 * time.Hour},
			4 * time.Hour, 72 * time.Hour},
		{models.FailureRateLimited,
			[]time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour},
			4 * time.Hour, 72 * time.Hour},
		{models.FailureMailboxFull,
			[]time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour},
			8 * time.Hour, 48 * time.Hour},
		{models.FailureTLS,
			[]time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour},
			4 * time.Hour, 72 * time.Hour},
		{models.FailureDNS,
			[]time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute, 160 * time.Minute, 4 * time.Hour},
			4 * time.Hour, 72 * time.Hour},
		{models.FailureConnection,
			[]time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, 64 * time.Minute, 128 * time.Minute, 4 * time.Hour},
			4 * time.Hour, 72 * time.Hour},
		{models.FailureOtherTransient,
			[]time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, 64 * time.Minute, 128 * time.Minute, 4 * time.Hour},
			4 * time.Hour, 72 * time.Hour},
	}
	if len(cases) != len(transientCategories) {
		t.Fatalf("%d schedules tested, %d transient categories", len(cases), len(transientCategories))
	}
	for _, tc := range cases {
		t.Run(string(tc.category), func(t *testing.T) {
			s, ok := retrySchedules[tc.category]
			if !ok {
				t.Fatal("no schedule of its own")
			}
			for i, want := range tc.delays {
				if got := s.delay(i + 1); got != want {
					t.Errorf("delay after attempt %d = %s, want %s", i+1, got, want)
				}
			}
			for _, attempts := range []int{30, 64, 1 << 20} {
				if got := s.delay(attempts); got != tc.max {
					t.Errorf("delay after attempt %d = %s, want the cap %s", attempts, got, tc.max)
				}
			}
			if s.maxAge != tc.maxAge {
				t.Errorf("max age %s, want %s", s.maxAge, tc.maxAge)
			}
		})
	}
}

// A schedule never needs more attempts than a message's default budget to
// reach its maximum age, or the budget, not the schedule, would decide when a
// message bounces.
func TestRetrySchedules_FitTheDefaultBudget(t *testing.T) {
	const defaultBudget = 30
	for _, c := range transientCategories {
		s := retrySchedules[c]
		attempts, at := 1, time.Duration(0)
		for at+s.delay(attempts) < s.maxAge {
			at += s.delay(attempts)
			attempts++
		}
		if attempts > defaultBudget {
			t.Errorf("%s: %d attempts within %s, more than the budget of %d", c, attempts, s.maxAge, defaultBudget)
		}
	}
}

// Jitter moves a retry by up to a fifth either way, never past the cap, and
// does move it.
func TestRetrySchedule_Jitter(t *testing.T) {
	for _, c := range transientCategories {
		s := retrySchedules[c]
		for _, attempts := range []int{1, 3, 64} {
			base := s.delay(attempts)
			lo, hi := time.Duration(float64(base)*0.8), min(time.Duration(float64(base)*1.2), s.max)
			seen := map[time.Duration]bool{}
			for range 200 {
				got := s.jitteredDelay(attempts)
				if got < lo || got > hi || got <= 0 {
					t.Fatalf("%s after attempt %d: %s, outside [%s, %s]", c, attempts, got, lo, hi)
				}
				seen[got] = true
			}
			if len(seen) < 2 {
				t.Fatalf("%s after attempt %d: no jitter in 200 delays", c, attempts)
			}
		}
	}
}

// Greylisting accepts the retry that comes after its delay, five minutes in
// Postgrey's default, so even the earliest jittered first retry must wait that.
func TestRetrySchedule_GreylistingWaitsItOut(t *testing.T) {
	s := retrySchedules[models.FailureGreylisted]
	if earliest := time.Duration(float64(s.delay(1)) * (1 - backoffJitterFraction)); earliest <= 5*time.Minute {
		t.Errorf("earliest first retry %s, not after five minutes", earliest)
	}
}

// A permanent category has no schedule: it bounces at once.
func TestRetrySchedules_NoneForPermanent(t *testing.T) {
	for c := range retrySchedules {
		if c.Permanent() {
			t.Errorf("permanent %q has a retry schedule", c)
		}
	}
}

func TestRetryAt(t *testing.T) {
	created := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	item := func(attempts int, age time.Duration) (models.OutboundQueue, time.Time) {
		return models.OutboundQueue{
			Attempts: attempts, MaxRetries: 30, CreatedAt: created, ExpiresAt: created.Add(72 * time.Hour),
		}, created.Add(age)
	}

	t.Run("retried on its category's schedule", func(t *testing.T) {
		it, now := item(1, time.Minute)
		next, ok := retryAt(it, models.FailureRateLimited, now)
		if !ok {
			t.Fatal("not retried")
		}
		if d := next.Sub(now); d < 12*time.Minute || d > 18*time.Minute {
			t.Errorf("rate-limited first retry in %s, want 15m ± 20%%", d)
		}
	})
	t.Run("bounces once the next try would pass the category's maximum age", func(t *testing.T) {
		it, now := item(5, 47*time.Hour) // a full mailbox, two days on: 8h to the next try
		if _, ok := retryAt(it, models.FailureMailboxFull, now); ok {
			t.Error("a mailbox full for two days is still retried")
		}
		// The same message, failing for another reason, has a day left.
		if _, ok := retryAt(it, models.FailureConnection, now); !ok {
			t.Error("a connection failure at 47h is not retried")
		}
	})
	t.Run("bounces before its own expiry", func(t *testing.T) {
		it, now := item(3, time.Hour)
		it.ExpiresAt = now.Add(10 * time.Minute) // sooner than any rate-limited retry
		if _, ok := retryAt(it, models.FailureRateLimited, now); ok {
			t.Error("retried after the row's own expiry, where it would never be picked up again")
		}
	})
	t.Run("bounces when its retry budget is spent", func(t *testing.T) {
		it, now := item(30, time.Hour)
		if _, ok := retryAt(it, models.FailureConnection, now); ok {
			t.Error("retried after its 30th attempt")
		}
		it.Attempts = 29
		if _, ok := retryAt(it, models.FailureConnection, now); !ok {
			t.Error("not retried after its 29th attempt")
		}
	})
}
