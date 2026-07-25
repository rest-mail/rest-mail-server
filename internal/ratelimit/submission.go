// Package ratelimit provides a shared, process-local per-account submission
// rate limiter. It caps how many messages a single authenticated account may
// submit per minute and per hour and is used by every outbound-submission entry
// point so a single compromised credential cannot flood outbound mail:
//
//   - the SMTP submission ports (587/465), see internal/gateway/smtp (#171); and
//   - the webmail/API send path, see internal/api/handlers (#184).
//
// Keeping one implementation here means both paths cap a sender's outbound
// volume identically instead of drifting apart.
package ratelimit

import (
	"sync"
	"time"
)

// Default per-account submission limits, shared by the SMTP submission ports
// and the webmail/API send path so both cap a sender's outbound volume the same
// way (#171, #184).
const (
	DefaultPerMinute = 20
	DefaultPerHour   = 100
)

// SubmissionLimiter caps how many messages a single account may submit per
// minute and per hour. It is process-local and safe for concurrent use. A nil
// *SubmissionLimiter allows every request, so a caller that constructs one
// without a limiter (e.g. a unit test) is never rate limited.
type SubmissionLimiter struct {
	perMinute int
	perHour   int
	now       func() time.Time

	mu      sync.Mutex
	buckets map[string]*window
}

// window tracks one account's counts within the current minute and hour.
type window struct {
	minuteCount int
	minuteReset time.Time
	hourCount   int
	hourReset   time.Time
}

// NewSubmissionLimiter builds a limiter with the given per-minute and per-hour
// caps. A non-positive cap disables that tier.
func NewSubmissionLimiter(perMinute, perHour int) *SubmissionLimiter {
	return &SubmissionLimiter{
		perMinute: perMinute,
		perHour:   perHour,
		now:       time.Now,
		buckets:   make(map[string]*window),
	}
}

// Allow records one submission for key and reports whether it stays within both
// the per-minute and per-hour caps. Once a window is exceeded, every further
// submission in that window is denied until the window rolls over.
func (l *SubmissionLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b := l.buckets[key]
	if b == nil {
		b = &window{}
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
