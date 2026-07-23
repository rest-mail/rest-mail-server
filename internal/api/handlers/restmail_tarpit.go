package handlers

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

// restmailTarpitDecay is how long a source's negative-lookup streak is retained.
// After this idle period the streak decays to zero, so an occasional miss from a
// legitimate sender is never treated as enumeration. It also bounds the tracker
// map: entries idle past this window are swept.
const restmailTarpitDecay = 5 * time.Minute

// RestmailTarpitConfig configures the negative-lookup enumeration tarpit on the
// RESTMAIL recipient-check path (GET /restmail/mailboxes). It is the exported
// surface the router wires from internal/config; the tracker itself is
// package-private. Enabled=false turns the tarpit off entirely.
type RestmailTarpitConfig struct {
	Enabled bool
	Base    time.Duration
	Max     time.Duration
}

// negLookupTarpit throttles recipient enumeration on the RESTMAIL negative
// mailbox-lookup path. Each source IP that asks about a mailbox that does NOT
// exist is delayed by an escalating, capped amount (base * consecutive misses,
// capped at max); the streak decays after an idle window. Positive lookups
// never call this, so real recipients stay fast.
//
// This is defense-in-depth — the path is already DKIM/SPF/DMARC-gated — that
// closes the OSI-1 enumeration oracle two ways: (a) bulk scanning of guessed
// addresses is throttled to a trickle as the per-source delay escalates, and
// (b) even the first miss pays a uniform base-delay floor, so a single probe's
// latency cannot trivially distinguish "exists" (fast) from "does not exist".
//
// The tracker mirrors the auth rate-limiter's design: a lazily-decayed map with
// an injectable clock and an opportunistic sweep under the same lock, so there
// is no background goroutine to leak and the map cannot grow unbounded.
type negLookupTarpit struct {
	enabled bool
	base    time.Duration
	max     time.Duration
	decay   time.Duration

	now   func() time.Time                     // injectable clock (tests)
	sleep func(context.Context, time.Duration) // injectable sleeper (tests)

	mu        sync.Mutex
	sources   map[string]*negSource
	lastSweep time.Time
}

// negSource is one source IP's decaying negative-lookup streak.
type negSource struct {
	count    int
	lastSeen time.Time
}

// newNegLookupTarpit builds a tracker from the exported config. A nil/disabled
// result is safe to call: delay is a no-op.
func newNegLookupTarpit(cfg RestmailTarpitConfig) *negLookupTarpit {
	return &negLookupTarpit{
		enabled: cfg.Enabled,
		base:    cfg.Base,
		max:     cfg.Max,
		decay:   restmailTarpitDecay,
		now:     time.Now,
		sleep:   ctxSleep,
		sources: make(map[string]*negSource),
	}
}

// delay records one negative lookup from key, computes the escalating capped
// delay, sleeps it (aborting if ctx is cancelled — e.g. the client hung up),
// and returns the delay that was applied. A nil, disabled, or misconfigured
// tarpit returns 0 without sleeping.
func (t *negLookupTarpit) delay(ctx context.Context, key string) time.Duration {
	if t == nil || !t.enabled || t.base <= 0 || t.max <= 0 {
		return 0
	}
	d := t.record(key)
	t.sleep(ctx, d)
	return d
}

// record advances key's streak (with decay) and returns this miss's delay. It
// is deterministic given the injected clock, so tests exercise the escalation,
// cap, decay, and per-source keying without sleeping.
func (t *negLookupTarpit) record(key string) time.Duration {
	now := t.now()

	t.mu.Lock()
	defer t.mu.Unlock()

	t.sweepLocked(now)

	s, ok := t.sources[key]
	if !ok || now.Sub(s.lastSeen) > t.decay {
		// New source, or one whose streak has decayed: start fresh.
		s = &negSource{}
		t.sources[key] = s
	}
	s.count++
	s.lastSeen = now

	return escalatingDelay(t.base, t.max, s.count)
}

// sweepLocked evicts sources untouched for longer than decay. It runs at most
// once per decay window to keep the amortized cost negligible. Caller holds mu.
func (t *negLookupTarpit) sweepLocked(now time.Time) {
	if now.Sub(t.lastSweep) < t.decay {
		return
	}
	t.lastSweep = now
	for k, s := range t.sources {
		if now.Sub(s.lastSeen) > t.decay {
			delete(t.sources, k)
		}
	}
}

// escalatingDelay is the pure delay model shared by the negative-lookup tarpit:
// base * n, capped at max, where n is the source's consecutive-miss count
// (n >= 1 on the first miss, so it doubles as the uniform base floor). The
// multiplication is overflow-safe: the cap is checked in whole base-steps before
// multiplying, so a huge n can never wrap time.Duration.
func escalatingDelay(base, max time.Duration, n int) time.Duration {
	if base <= 0 || max <= 0 || n <= 0 {
		return 0
	}
	steps := max / base
	if steps <= 0 || time.Duration(n) >= steps {
		return max
	}
	return base * time.Duration(n)
}

// ctxSleep sleeps for d, aborting early if ctx is cancelled. A non-positive d
// returns immediately.
func ctxSleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-tm.C:
	case <-ctx.Done():
	}
}

// restmailClientIP extracts the client IP from RemoteAddr. When the API sits
// behind the chi RealIP middleware (which resolves X-Forwarded-For/X-Real-IP
// into RemoteAddr), this keys on the true client for proxied deployments.
func restmailClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
