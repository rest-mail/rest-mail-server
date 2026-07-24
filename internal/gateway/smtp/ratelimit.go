package smtp

import (
	"sync"
	"time"
)

// Default per-account submission limits. They mirror the outbound pipeline's
// rate_limit defaults so submission (587/465) and the webmail/API send path cap
// a sender's outbound volume the same way. A single compromised credential
// therefore cannot flood outbound mail through the submission port (#171).
const (
	defaultSubmissionPerMinute = 20
	defaultSubmissionPerHour   = 100
)

// submissionRateLimiter caps how many messages a single authenticated account
// may submit per minute and per hour. It is process-local and shared across all
// submission sessions of a Server. A nil *submissionRateLimiter allows every
// request, so a session constructed without one (e.g. in a unit test) is never
// rate limited.
type submissionRateLimiter struct {
	perMinute int
	perHour   int
	now       func() time.Time

	mu      sync.Mutex
	buckets map[string]*rateWindow
}

// rateWindow tracks one account's counts within the current minute and hour.
type rateWindow struct {
	minuteCount int
	minuteReset time.Time
	hourCount   int
	hourReset   time.Time
}

// newSubmissionRateLimiter builds a limiter with the given per-minute and
// per-hour caps. A non-positive cap disables that tier.
func newSubmissionRateLimiter(perMinute, perHour int) *submissionRateLimiter {
	return &submissionRateLimiter{
		perMinute: perMinute,
		perHour:   perHour,
		now:       time.Now,
		buckets:   make(map[string]*rateWindow),
	}
}

// Allow records one submission for key and reports whether it stays within both
// the per-minute and per-hour caps. Once a window is exceeded, every further
// submission in that window is denied until the window rolls over.
func (l *submissionRateLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b := l.buckets[key]
	if b == nil {
		b = &rateWindow{}
		l.buckets[key] = b
	}
	if b.minuteReset.IsZero() || now.After(b.minuteReset) {
		b.minuteCount = 0
		b.minuteReset = now.Add(time.Minute)
	}
	if b.hourReset.IsZero() || now.After(b.hourReset) {
		b.hourCount = 0
		b.hourReset = now.Add(time.Hour)
	}
	b.minuteCount++
	b.hourCount++

	if l.perMinute > 0 && b.minuteCount > l.perMinute {
		return false
	}
	if l.perHour > 0 && b.hourCount > l.perHour {
		return false
	}
	return true
}
