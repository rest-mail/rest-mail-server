package connlimiter

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/restmail/restmail/internal/metrics"
)

// Config holds connection limiter settings.
type Config struct {
	MaxPerIP        int           // max simultaneous connections per IP (default 20)
	MaxGlobal       int           // max total connections (default 1000)
	AuthMaxFails    int           // auth failures before temp ban (default 5)
	AuthBanWindow   time.Duration // window for counting failures (default 10m)
	AuthBanDuration time.Duration // how long to ban (default 30m)
}

func (c Config) withDefaults() Config {
	if c.MaxPerIP <= 0 {
		c.MaxPerIP = 20
	}
	if c.MaxGlobal <= 0 {
		c.MaxGlobal = 1000
	}
	if c.AuthMaxFails <= 0 {
		c.AuthMaxFails = 5
	}
	if c.AuthBanWindow <= 0 {
		c.AuthBanWindow = 10 * time.Minute
	}
	if c.AuthBanDuration <= 0 {
		c.AuthBanDuration = 30 * time.Minute
	}
	return c
}

type failRecord struct {
	mu       sync.Mutex
	failures []time.Time
	bannedAt time.Time
}

// BanChecker is an optional function that checks persistent (DB-backed) bans.
// It receives the IP and protocol (e.g. "smtp") and returns true if banned.
type BanChecker func(ip, protocol string) bool

// Limiter tracks per-IP connection counts and auth failures.
type Limiter struct {
	cfg    Config
	global atomic.Int32
	// acceptMu serializes the cap-check-and-increment in Accept so it is a
	// single atomic step. The counters stay atomic.Int32 because Release
	// decrements them lock-free (concurrently with an Accept holding acceptMu).
	acceptMu   sync.Mutex
	perIP      sync.Map // string → *atomic.Int32
	authFails  sync.Map // source IP → *failRecord
	userFails  sync.Map // normalized account key → *failRecord
	banChecker BanChecker
	protocol   string
}

// New creates a Limiter with the given config (defaults applied for zero values).
func New(cfg Config) *Limiter {
	return &Limiter{cfg: cfg.withDefaults()}
}

// SetBanChecker sets an optional function to check persistent bans (e.g. from DB).
func (l *Limiter) SetBanChecker(checker BanChecker, protocol string) {
	l.banChecker = checker
	l.protocol = protocol
}

// SetProtocol tags this limiter with the protocol it fronts ("smtp"/"imap"/
// "pop3"). It is the label used for the active_connections and auth_failures
// metrics, and is applied independently of SetBanChecker so metrics carry the
// protocol even when no DB-backed ban checker is wired. When unset, those
// metrics are not emitted (avoids an empty-label series in tests/tooling).
func (l *Limiter) SetProtocol(protocol string) {
	l.protocol = protocol
}

// Accept checks whether a new connection from ip is allowed.
// Returns true and increments counters if allowed.
func (l *Limiter) Accept(ip string) bool {
	// Check persistent bans first. The ban checker may hit the DB, so keep it
	// outside acceptMu.
	if l.banChecker != nil && l.banChecker(ip, l.protocol) {
		return false
	}

	// The cap checks and the increments must form one atomic step. Doing the
	// Load and the Add as separate atomic ops let concurrent callers each
	// observe a sub-limit count and all increment past the cap, admitting more
	// connections than MaxPerIP/MaxGlobal (M-2, CWE-362).
	l.acceptMu.Lock()
	if int(l.global.Load()) >= l.cfg.MaxGlobal {
		l.acceptMu.Unlock()
		return false
	}
	val, _ := l.perIP.LoadOrStore(ip, &atomic.Int32{})
	counter := val.(*atomic.Int32)
	if int(counter.Load()) >= l.cfg.MaxPerIP {
		l.acceptMu.Unlock()
		return false
	}
	counter.Add(1)
	l.global.Add(1)
	l.acceptMu.Unlock()

	if l.protocol != "" {
		metrics.ActiveConnections.WithLabelValues(l.protocol).Inc()
	}
	return true
}

// Release decrements connection counters for an IP. It mirrors a prior
// successful Accept, so the active_connections gauge is decremented in lockstep
// with the global counter.
func (l *Limiter) Release(ip string) {
	l.global.Add(-1)
	if l.protocol != "" {
		metrics.ActiveConnections.WithLabelValues(l.protocol).Dec()
	}
	if val, ok := l.perIP.Load(ip); ok {
		counter := val.(*atomic.Int32)
		counter.Add(-1)
	}
}

// recordFail appends a failure to the record for key in fails and (re)arms the
// ban once the count within the sliding window crosses AuthMaxFails. It is the
// shared core of the per-IP and per-account failure trackers.
func (l *Limiter) recordFail(fails *sync.Map, key string) {
	val, _ := fails.LoadOrStore(key, &failRecord{})
	rec := val.(*failRecord)
	rec.mu.Lock()
	defer rec.mu.Unlock()

	now := time.Now()
	rec.failures = append(rec.failures, now)

	// Prune old failures outside the window.
	cutoff := now.Add(-l.cfg.AuthBanWindow)
	fresh := rec.failures[:0]
	for _, t := range rec.failures {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	rec.failures = fresh

	if len(rec.failures) >= l.cfg.AuthMaxFails {
		rec.bannedAt = now
	}
}

// banned reports whether key currently holds an in-memory ban in fails,
// lazily clearing it once AuthBanDuration has elapsed.
func (l *Limiter) banned(fails *sync.Map, key string) bool {
	val, ok := fails.Load(key)
	if !ok {
		return false
	}
	rec := val.(*failRecord)
	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.bannedAt.IsZero() {
		return false
	}
	if time.Since(rec.bannedAt) > l.cfg.AuthBanDuration {
		rec.bannedAt = time.Time{}
		rec.failures = nil
		return false
	}
	return true
}

// clearFail drops the failure history for key in fails.
func (l *Limiter) clearFail(fails *sync.Map, key string) {
	val, ok := fails.Load(key)
	if !ok {
		return
	}
	rec := val.(*failRecord)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.failures = nil
	rec.bannedAt = time.Time{}
}

// RecordAuthFail records an authentication failure for a source IP.
func (l *Limiter) RecordAuthFail(ip string) {
	if l.protocol != "" {
		metrics.AuthFailures.WithLabelValues(l.protocol).Inc()
	}
	l.recordFail(&l.authFails, ip)
}

// IsBanned returns true if the IP is currently banned (in-memory or persistent).
func (l *Limiter) IsBanned(ip string) bool {
	// Check persistent bans
	if l.banChecker != nil && l.banChecker(ip, l.protocol) {
		return true
	}
	return l.banned(&l.authFails, ip)
}

// ResetAuth clears auth failure history for an IP (call on successful auth).
func (l *Limiter) ResetAuth(ip string) {
	l.clearFail(&l.authFails, ip)
}

// accountKey normalizes a username/email into the stable key used for
// per-account throttling, so that case- and whitespace-only variants of the
// same login are counted as one principal. An empty result means "no account
// to key on" and callers skip per-account tracking.
func accountKey(user string) string {
	return strings.ToLower(strings.TrimSpace(user))
}

// RecordAuthFailUser records an authentication failure against an ACCOUNT
// (username/email), independent of the source IP. This throttles a distributed
// brute-force that rotates source IPs against a single account, which the per-IP
// tracker alone cannot see. It deliberately does NOT touch the auth_failures
// metric: the per-IP RecordAuthFail already counts the event, so pairing the two
// on one failure must not double-count.
func (l *Limiter) RecordAuthFailUser(user string) {
	key := accountKey(user)
	if key == "" {
		return
	}
	l.recordFail(&l.userFails, key)
}

// IsUserBanned reports whether the ACCOUNT is currently rate-limited by too many
// recent auth failures across all source IPs.
func (l *Limiter) IsUserBanned(user string) bool {
	key := accountKey(user)
	if key == "" {
		return false
	}
	return l.banned(&l.userFails, key)
}

// ResetAuthUser clears per-account auth failure history (call on a successful
// auth for that account).
func (l *Limiter) ResetAuthUser(user string) {
	key := accountKey(user)
	if key == "" {
		return
	}
	l.clearFail(&l.userFails, key)
}
