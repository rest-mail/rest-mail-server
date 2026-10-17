package queue

import (
	"math/rand/v2"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// retrySchedule is how a transient failure of one category is retried: the
// first retry after first, each later one twice as long after the last but
// never more than max apart, until the message has been queued for maxAge. The
// message then bounces, as it does sooner if its row's own expiry or retry
// budget runs out first.
type retrySchedule struct {
	first, max, maxAge time.Duration
}

// retrySchedules holds a schedule for every transient category. Each stays
// within the default 30-attempt budget over its maximum age. A permanent
// category has none: it bounces at once.
var retrySchedules = map[models.FailureCategory]retrySchedule{
	// Greylisting turns away a first attempt and accepts the next one after a
	// delay, five minutes in most setups (Postgrey's default). A first retry at
	// seven minutes clears that even with the jitter taking a fifth off.
	models.FailureGreylisted: {first: 7 * time.Minute, max: 4 * time.Hour, maxAge: 72 * time.Hour},
	// A remote limiting our rate is not helped by retrying soon: back off harder.
	models.FailureRateLimited: {first: 15 * time.Minute, max: 4 * time.Hour, maxAge: 72 * time.Hour},
	// Someone has to delete mail first. A mailbox still full after two days is
	// seldom emptied in time to matter, and the sender is better told.
	models.FailureMailboxFull: {first: time.Hour, max: 8 * time.Hour, maxAge: 48 * time.Hour},
	// Certificates and TLS policies are fixed by people, not in a minute.
	models.FailureTLS: {first: 15 * time.Minute, max: 4 * time.Hour, maxAge: 72 * time.Hour},
	// A resolver or nameserver failing for now: a little longer than a blip.
	models.FailureDNS: {first: 5 * time.Minute, max: 4 * time.Hour, maxAge: 72 * time.Hour},
	// Outages, and anything else: soon at first, then less and less often.
	models.FailureConnection:     {first: time.Minute, max: 4 * time.Hour, maxAge: 72 * time.Hour},
	models.FailureOtherTransient: {first: time.Minute, max: 4 * time.Hour, maxAge: 72 * time.Hour},
}

// scheduleFor returns the retry schedule for a transient category, or the
// catch-all one for a category without its own.
func scheduleFor(c models.FailureCategory) retrySchedule {
	if s, ok := retrySchedules[c]; ok {
		return s
	}
	return retrySchedules[models.FailureOtherTransient]
}

// delay is the wait before the next attempt once attempts attempts have
// failed: first after the first, twice that after the second, and so on.
//
// Doubling stops at the cap, so the delay never overflows into a negative
// duration however large attempts is; that once scheduled retries in the past,
// firing them forever.
func (s retrySchedule) delay(attempts int) time.Duration {
	d := s.first
	for i := 1; i < attempts && d < s.max; i++ {
		d *= 2
	}
	return min(d, s.max)
}

// backoffJitterFraction is the most jitteredDelay moves a delay either way.
const backoffJitterFraction = 0.20

// jitteredDelay is delay moved randomly by up to a fifth either way, never past
// the cap and never to nothing. Without it, messages deferred by the same
// outage retry in lockstep and hit a recovering host all at once, every time.
func (s retrySchedule) jitteredDelay(attempts int) time.Duration {
	factor := 1 - backoffJitterFraction + rand.Float64()*(2*backoffJitterFraction) // [0.8, 1.2)
	d := time.Duration(float64(s.delay(attempts)) * factor)
	if d > s.max {
		d = s.max
	}
	if d <= 0 {
		d = time.Second
	}
	return d
}

// retryAt says when to retry a message after a transient failure, or false
// when it is not to be retried: its next attempt would come after its
// category's maximum age, after its own expiry, or beyond its retry budget.
// item.Attempts counts the attempt that just failed (the claim incremented it).
//
// Bouncing then, rather than scheduling an attempt past the deadline, also
// means no row is left deferred past its expiry, where the worker never picks
// it up again and its sender never hears.
func retryAt(item models.OutboundQueue, category models.FailureCategory, now time.Time) (time.Time, bool) {
	if item.Attempts >= item.MaxRetries {
		return time.Time{}, false
	}
	s := scheduleFor(category)
	next := now.Add(s.jitteredDelay(item.Attempts))
	deadline := item.CreatedAt.Add(s.maxAge)
	if item.ExpiresAt.Before(deadline) {
		deadline = item.ExpiresAt
	}
	if !next.Before(deadline) {
		return time.Time{}, false
	}
	return next, true
}
