package smtp

import (
	"context"
	"time"
)

// Anti-abuse tarpit defaults, mirrored by internal/config (SMTP_TARPIT_ENABLED,
// SMTP_TARPIT_BASE, SMTP_TARPIT_SOFT_LIMIT, SMTP_TARPIT_MAX). They apply when
// the deployment does not configure a policy.
const (
	defaultTarpitBase      = 1 * time.Second
	defaultTarpitSoftLimit = 2
	defaultTarpitMax       = 15 * time.Second
)

// tarpitPolicy is the anti-abuse escalating-delay policy applied to a single
// SMTP connection's rejection points (an invalid inbound RCPT rejected 550, or
// an AUTH failure). It slows dictionary/enumeration/spam sessions without ever
// touching a legitimate sender.
//
// A legitimate sender makes 0-1 mistakes and is never delayed. A script that
// racks up rejections is progressively slowed: once the connection's cumulative
// error count crosses softLimit, each SUBSEQUENT rejection is preceded by a
// sleep that grows linearly with the overage, capped at max.
//
// The cap is mandatory, not cosmetic: the sleep holds a connection slot and a
// goroutine, so an unbounded delay would be a self-inflicted DoS. The bound is
// three-layered — this per-rejection cap (max), the connlimiter's per-IP and
// global connection caps (concurrent tarpitted sessions are capped), and the
// per-command ReadTimeout — so total tarpit resource use is always finite.
type tarpitPolicy struct {
	enabled   bool
	base      time.Duration
	softLimit int
	max       time.Duration
}

// defaultTarpitPolicy returns the compiled-in policy used when the deployment
// does not configure one.
func defaultTarpitPolicy() tarpitPolicy {
	return tarpitPolicy{
		enabled:   true,
		base:      defaultTarpitBase,
		softLimit: defaultTarpitSoftLimit,
		max:       defaultTarpitMax,
	}
}

// tarpitDelay is the pure delay model: how long to sleep before returning the
// rejection that brought a connection's cumulative error count to errCount.
//
//   - At or below softLimit: 0. Legit senders (0-1 errors) and the grace band
//     up to the soft limit pay nothing.
//   - Above softLimit: base * (errCount - softLimit) — the delay escalates
//     linearly with each further error.
//   - Never more than max: the cap bounds how long a single rejection can hold
//     the connection's goroutine + limiter slot, which is what keeps the tarpit
//     from becoming a self-inflicted DoS.
//
// A non-positive base or max yields 0 (tarpitting off). The multiplication is
// overflow-safe: the cap is checked in step units before multiplying, so a huge
// errCount can never wrap time.Duration.
func tarpitDelay(errCount, softLimit int, base, max time.Duration) time.Duration {
	if base <= 0 || max <= 0 {
		return 0
	}
	if errCount <= softLimit {
		return 0
	}
	over := errCount - softLimit
	// Compare in whole base-steps to avoid overflowing time.Duration for large
	// errCount: if the overage reaches the number of base-steps that fit in max,
	// the delay is capped regardless of the exact product.
	steps := max / base
	if steps <= 0 || time.Duration(over) >= steps {
		return max
	}
	return base * time.Duration(over)
}

// delayFor returns this policy's delay for the given cumulative connection error
// count, or 0 when the policy is disabled.
func (p tarpitPolicy) delayFor(errCount int) time.Duration {
	if !p.enabled {
		return 0
	}
	return tarpitDelay(errCount, p.softLimit, p.base, p.max)
}

// tarpitSleep sleeps for d, aborting early if ctx is cancelled — a torn-down
// connection or a server shutdown cancels the session context, so the goroutine
// never hangs past the connection's life or blocks a clean shutdown. A
// non-positive d returns immediately. It is a thin, injectable wrapper so the
// escalation logic (tarpitDelay) stays a pure, fast-to-test function.
func tarpitSleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
