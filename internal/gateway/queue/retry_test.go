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
			4 * time.Hour, models.QueueLifetime},
		{models.FailureRateLimited,
			[]time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour},
			4 * time.Hour, models.QueueLifetime},
		{models.FailureMailboxFull,
			[]time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour},
			8 * time.Hour, models.QueueLifetime},
		{models.FailureTLS,
			[]time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour},
			4 * time.Hour, models.QueueLifetime},
		{models.FailureDNS,
			[]time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute, 160 * time.Minute, 4 * time.Hour},
			4 * time.Hour, models.QueueLifetime},
		{models.FailureConnection,
			[]time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute, 160 * time.Minute, 4 * time.Hour},
			4 * time.Hour, models.QueueLifetime},
		{models.FailureOtherTransient,
			[]time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute, 160 * time.Minute, 4 * time.Hour},
			4 * time.Hour, models.QueueLifetime},
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
// reach its maximum age, even with every delay jittered as short as it goes,
// or the budget, not the schedule, would decide when a message bounces (#711).
func TestRetrySchedules_FitTheDefaultBudget(t *testing.T) {
	for _, c := range transientCategories {
		s := retrySchedules[c]
		attempts, at := 1, time.Duration(0)
		shortest := func(attempts int) time.Duration {
			return time.Duration(float64(s.delay(attempts)) * (1 - backoffJitterFraction))
		}
		for at+shortest(attempts) < s.maxAge {
			at += shortest(attempts)
			attempts++
		}
		if attempts >= models.DefaultMaxRetries {
			t.Errorf("%s: %d attempts within %s, the budget of %d runs out first", c, attempts, s.maxAge, models.DefaultMaxRetries)
		}
	}
}

// A message is retried for at least four to five days before it bounces
// (RFC 5321 §4.5.4.1), and by default for five (#711).
func TestRetrySchedules_RetryForDays(t *testing.T) {
	if models.QueueLifetime != 5*24*time.Hour {
		t.Errorf("the queue's lifetime is %s, want five days", models.QueueLifetime)
	}
	for _, c := range transientCategories {
		if age := retrySchedules[c].maxAge; age < 4*24*time.Hour {
			t.Errorf("%s bounces after %s, under four days", c, age)
		}
	}
	var q models.OutboundQueue
	if err := q.BeforeCreate(nil); err != nil {
		t.Fatal(err)
	}
	if until := time.Until(q.ExpiresAt); until < models.QueueLifetime-time.Minute || q.MaxRetries != models.DefaultMaxRetries {
		t.Errorf("a new message expires in %s with a budget of %d; want %s and %d", until, q.MaxRetries, models.QueueLifetime, models.DefaultMaxRetries)
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
			Attempts: attempts, MaxRetries: models.DefaultMaxRetries, CreatedAt: created, ExpiresAt: created.Add(models.QueueLifetime),
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
		it, now := item(5, 116*time.Hour) // a full mailbox, four hours short of five days: 8h to the next try
		if _, ok := retryAt(it, models.FailureMailboxFull, now); ok {
			t.Error("a mailbox full until the queue's lifetime is up is still retried")
		}
		// The same message, failing for another reason, retries within the time left.
		if _, ok := retryAt(it, models.FailureConnection, now); !ok {
			t.Error("a connection failure at 116h is not retried")
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
		it, now := item(models.DefaultMaxRetries, time.Hour)
		if _, ok := retryAt(it, models.FailureConnection, now); ok {
			t.Errorf("retried after its %dth attempt", models.DefaultMaxRetries)
		}
		it.Attempts = models.DefaultMaxRetries - 1
		if _, ok := retryAt(it, models.FailureConnection, now); !ok {
			t.Errorf("not retried after its %dth attempt", models.DefaultMaxRetries-1)
		}
	})
}
