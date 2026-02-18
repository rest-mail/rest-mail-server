package connlimiter

import (
	"sync"
	"sync/atomic"
	"time"
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
	cfg          Config
	global       atomic.Int32
	perIP        sync.Map // string → *atomic.Int32
	authFails    sync.Map // string → *failRecord
	banChecker   BanChecker
	protocol     string
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

// Accept checks whether a new connection from ip is allowed.
// Returns true and increments counters if allowed.
func (l *Limiter) Accept(ip string) bool {
	// Check persistent bans first
	if l.banChecker != nil && l.banChecker(ip, l.protocol) {
		return false
	}
	if int(l.global.Load()) >= l.cfg.MaxGlobal {
		return false
	}
	val, _ := l.perIP.LoadOrStore(ip, &atomic.Int32{})
	counter := val.(*atomic.Int32)
	if int(counter.Load()) >= l.cfg.MaxPerIP {
		return false
	}
	counter.Add(1)
	l.global.Add(1)
	return true
}

// Release decrements connection counters for an IP.
func (l *Limiter) Release(ip string) {
	l.global.Add(-1)
	if val, ok := l.perIP.Load(ip); ok {
		counter := val.(*atomic.Int32)
		counter.Add(-1)
	}
}

// RecordAuthFail records an authentication failure for an IP.
func (l *Limiter) RecordAuthFail(ip string) {
	val, _ := l.authFails.LoadOrStore(ip, &failRecord{})
	rec := val.(*failRecord)
	rec.mu.Lock()
	defer rec.mu.Unlock()

	now := time.Now()
	rec.failures = append(rec.failures, now)

	// Prune old failures outside the window
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

// IsBanned returns true if the IP is currently banned (in-memory or persistent).
func (l *Limiter) IsBanned(ip string) bool {
	// Check persistent bans
	if l.banChecker != nil && l.banChecker(ip, l.protocol) {
		return true
	}

	val, ok := l.authFails.Load(ip)
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

// ResetAuth clears auth failure history for an IP (call on successful auth).
func (l *Limiter) ResetAuth(ip string) {
	val, ok := l.authFails.Load(ip)
	if !ok {
		return
	}
	rec := val.(*failRecord)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.failures = nil
	rec.bannedAt = time.Time{}
}
