package middleware

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimitConfig configures the per-client token-bucket limiter.
type RateLimitConfig struct {
	// RPS is the sustained number of requests allowed per second per client IP
	// (the bucket refill rate).
	RPS float64
	// Burst is the bucket capacity: the most requests a single client IP may
	// make in a short spike before being throttled to RPS.
	Burst int
	// TTL is how long an idle client's bucket is retained before it is swept.
	// Zero selects a sensible default so the bucket map cannot grow unbounded.
	TTL time.Duration
}

// tokenBucket is a lazily-refilled token bucket for one client key.
type tokenBucket struct {
	tokens   float64
	last     time.Time
	lastSeen time.Time
}

// rateLimiter is a per-client-IP token-bucket limiter with no external
// dependencies. Buckets refill lazily on access and idle buckets are swept
// opportunistically (once per TTL window) under the same lock, so there is no
// background goroutine to leak.
type rateLimiter struct {
	rps       float64
	burst     float64
	ttl       time.Duration
	now       func() time.Time // injectable for tests
	mu        sync.Mutex
	buckets   map[string]*tokenBucket
	lastSweep time.Time
}

func newRateLimiter(cfg RateLimitConfig) *rateLimiter {
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &rateLimiter{
		rps:     cfg.RPS,
		burst:   float64(cfg.Burst),
		ttl:     ttl,
		now:     time.Now,
		buckets: make(map[string]*tokenBucket),
	}
}

// allow reports whether a request from key is permitted now. When it is not, it
// returns the estimated wait until a token becomes available (for Retry-After).
func (rl *rateLimiter) allow(key string) (bool, time.Duration) {
	now := rl.now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.sweepLocked(now)

	b, ok := rl.buckets[key]
	if !ok {
		// New clients start with a full bucket so the first burst is allowed.
		b = &tokenBucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	}

	// Lazily refill based on elapsed time since the last observation.
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(rl.burst, b.tokens+elapsed*rl.rps)
		b.last = now
	}
	b.lastSeen = now

	if b.tokens >= 1 {
		b.tokens -= 1
		return true, 0
	}

	// Not enough for one request; report how long until one token accrues.
	deficit := 1 - b.tokens
	wait := time.Duration(deficit / rl.rps * float64(time.Second))
	return false, wait
}

// available reports whether key has a token to spend now, without spending
// it: for a limiter that charges only for failures, checked before the work
// whose failure it charges for. When it does not, it returns the estimated
// wait until one accrues.
func (rl *rateLimiter) available(key string) (bool, time.Duration) {
	now := rl.now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[key]
	if !ok {
		return true, 0
	}
	tokens := math.Min(rl.burst, b.tokens+now.Sub(b.last).Seconds()*rl.rps)
	if tokens >= 1 {
		return true, 0
	}
	return false, time.Duration((1 - tokens) / rl.rps * float64(time.Second))
}

// sweepLocked evicts buckets untouched for longer than ttl. It runs at most once
// per ttl window to keep the amortized cost negligible. Caller holds rl.mu.
func (rl *rateLimiter) sweepLocked(now time.Time) {
	if now.Sub(rl.lastSweep) < rl.ttl {
		return
	}
	rl.lastSweep = now
	for k, b := range rl.buckets {
		if now.Sub(b.lastSeen) > rl.ttl {
			delete(rl.buckets, k)
		}
	}
}

// clientIP extracts the client IP from RemoteAddr. The TrustedRealIP middleware
// (installed early in the stack) resolves X-Forwarded-For into RemoteAddr ONLY for
// trusted-proxy peers, so this keys on the true client for proxied deployments and
// on the genuine socket peer otherwise — a spoofed header cannot shift the key.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// RateLimit builds middleware that throttles requests per client IP using a
// token bucket. It is intended for auth-sensitive routes (login/refresh) to blunt
// brute-force and credential-stuffing without disrupting legitimate interactive
// use. Over-limit requests get 429 with a Retry-After header.
func RateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	rl := newRateLimiter(cfg)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed, retry := rl.allow(clientIP(r))
			if !allowed {
				writeRateLimited(w, retry)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeRateLimited answers 429 with a Retry-After of retry, at least a second.
func writeRateLimited(w http.ResponseWriter, retry time.Duration) {
	secs := int(math.Ceil(retry.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests; please slow down")
}

// FailureBudget is a token bucket per key that charges only for failures: a
// handler asks Available before the work and calls Charge when it fails, so
// whatever succeeds costs nothing. It is for budgets a handler keys on
// something only it knows, such as the account a request names.
type FailureBudget struct {
	rl *rateLimiter
}

// NewFailureBudget builds a FailureBudget. cfg.TTL must outlast a full refill
// (Burst/RPS seconds), or an idle key is swept, and starts over full, early.
func NewFailureBudget(cfg RateLimitConfig) *FailureBudget {
	return &FailureBudget{rl: newRateLimiter(cfg)}
}

// Available reports whether key may fail once more now. When it may not, it
// returns the estimated wait until it can (for Retry-After).
func (b *FailureBudget) Available(key string) (bool, time.Duration) {
	return b.rl.available(key)
}

// Charge spends one of key's failures.
func (b *FailureBudget) Charge(key string) {
	b.rl.allow(key)
}

// Take atomically reserves a request, including successful ones. Recovery
// delivery uses it so concurrent requests cannot overrun an address's budget.
func (b *FailureBudget) Take(key string) (bool, time.Duration) {
	return b.rl.allow(key)
}
