package middleware

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/metrics"
)

// How failed sign-ins are slowed (#1643). A failure waits for whichever is
// longer of two delays, and each counts recent failures:
//
//   - per address and client together: 3 failures free, then 0.5s, 1s, 2s, 4s,
//     and at most 5s. A guesser keeps paying for its own failures.
//   - per address, from anywhere: 5 failures free, then 0.25s, 0.5s, 1s, 2s, and
//     at most 3s. A guesser with many addresses gets a fresh allowance of the
//     first kind from each, so this is what it pays.
//
// One recent failure is forgotten each minute, so an address recovers on its
// own within an hour of an attack ending. A successful sign-in does not clear
// the counts: if it did, the account's owner signing in would restart an
// attacker's allowance.
const (
	addressFreeFailures = 5
	addressFirstDelay   = 250 * time.Millisecond
	addressMaxDelay     = 3 * time.Second
	addressMaxFailures  = 60

	clientFreeFailures = 3
	clientFirstDelay   = 500 * time.Millisecond
	clientMaxDelay     = 5 * time.Second
	clientMaxFailures  = 30

	failureForgottenEvery = time.Minute

	// underAttackFailures is the count of recent failures at which an address
	// is reported to the administrator. It is reported again only once the
	// count has fallen below underAttackRearm.
	underAttackFailures = 20
	underAttackRearm    = 10

	// maxWaitingFailures bounds how many failed sign-ins wait at once in this
	// process. Each one waiting holds a goroutine and its connection, and no
	// database connection.
	maxWaitingFailures = 256

	// maxTrackedKeys bounds each table of counts: an address need not exist to
	// be counted, so anyone can add keys. When a table is full a key is dropped
	// and starts over, which costs some slowing and never refuses anybody.
	maxTrackedKeys = 100_000
)

// SignInTarpit slows failed sign-ins and never refuses one. A handler checks
// the password first and calls Failed only when it is wrong, so a correct
// password never waits, whatever is happening to the account, and a stranger
// failing on purpose can at worst make someone's own mistyped password take a
// few seconds to be answered.
//
// Past maxWaitingFailures, a failure is answered at once with the answer it
// would have had. A flood therefore costs itself its own slowing, but cannot
// turn into a refusal for anybody, and holds no more than the bound.
//
// The counts live in this process, like the rest of the API's limiters, so
// with several replicas each counts what it sees, and a restart forgets them.
//
// A nil *SignInTarpit does nothing: the auth rate limits are switched off.
type SignInTarpit struct {
	now     func() time.Time
	wait    func(ctx context.Context, d time.Duration)
	waiting chan struct{}

	mu        sync.Mutex
	addresses map[[sha256.Size]byte]*failureCount
	clients   map[[sha256.Size]byte]*failureCount
	lastSweep time.Time
}

// failureCount is a count of recent failures, leaking one every
// failureForgottenEvery.
type failureCount struct {
	count    float64
	last     time.Time
	reported bool
}

// NewSignInTarpit builds an empty SignInTarpit.
func NewSignInTarpit() *SignInTarpit {
	return &SignInTarpit{
		now:       time.Now,
		wait:      waitOrHangUp,
		waiting:   make(chan struct{}, maxWaitingFailures),
		addresses: make(map[[sha256.Size]byte]*failureCount),
		clients:   make(map[[sha256.Size]byte]*failureCount),
	}
}

// waitOrHangUp waits for d, or until the client has gone: nobody is left to be
// slowed. The failure was charged before the wait, so hanging up does not avoid
// it.
func waitOrHangUp(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// SetWait replaces how a delay is waited out, so a test can see the delays
// without sitting through them. Call it before serving traffic.
func (t *SignInTarpit) SetWait(wait func(ctx context.Context, d time.Duration)) {
	t.wait = wait
}

// SetClock makes the tarpit read the time from now, so a test can move past a
// failure being forgotten. Call it before serving traffic.
func (t *SignInTarpit) SetClock(now func() time.Time) {
	t.now = now
}

// Reset forgets every count.
func (t *SignInTarpit) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.addresses)
	clear(t.clients)
	t.lastSweep = time.Time{}
}

// Failed counts a failed sign-in for address, known to this server or not, so
// the delay says nothing about which addresses exist, then waits out the delay
// it has earned. Call it holding no transaction.
func (t *SignInTarpit) Failed(r *http.Request, address string) {
	if t == nil {
		return
	}
	metrics.AuthFailures.WithLabelValues("api").Inc()
	delay := t.charge(address, clientIP(r))
	if delay <= 0 {
		return
	}
	select {
	case t.waiting <- struct{}{}:
	default:
		metrics.SignInFailuresUnslowed.Inc()
		return
	}
	metrics.SignInFailuresWaiting.Inc()
	defer func() {
		metrics.SignInFailuresWaiting.Dec()
		<-t.waiting
	}()
	t.wait(r.Context(), delay)
}

// charge counts one failure and returns the delay it has earned.
func (t *SignInTarpit) charge(address, ip string) time.Duration {
	canonical := canonicalSignInAddress(address)
	addressKey := sha256.Sum256([]byte(canonical))
	clientKey := sha256.Sum256([]byte(canonical + "\x00" + clientPrefix(ip)))
	now := t.now()

	t.mu.Lock()
	t.sweepLocked(now)
	byAddress := addFailure(t.addresses, addressKey, now, addressMaxFailures)
	byClient := addFailure(t.clients, clientKey, now, clientMaxFailures)
	failures := int(math.Ceil(byAddress.count))
	report := false
	switch {
	case failures >= underAttackFailures && !byAddress.reported:
		byAddress.reported, report = true, true
	case failures < underAttackRearm:
		byAddress.reported = false
	}
	delay := max(
		rampedDelay(failures, addressFreeFailures, addressFirstDelay, addressMaxDelay),
		rampedDelay(int(math.Ceil(byClient.count)), clientFreeFailures, clientFirstDelay, clientMaxDelay),
	)
	t.mu.Unlock()

	if report {
		metrics.SignInAccountsUnderAttack.Inc()
		// The address is what an attacker chose, so it is masked, as the
		// gateways mask it; the audit log has the attempts in full.
		slog.Warn("many failed sign-ins for one address", "event", "api_signin_account_under_attack",
			"address", maskSignInAddress(canonical), "failures", failures)
	}
	return delay
}

// addFailure counts a failure against key, after forgetting what has leaked
// since the last one. The caller holds the lock.
func addFailure(table map[[sha256.Size]byte]*failureCount, key [sha256.Size]byte, now time.Time, ceiling float64) *failureCount {
	entry, ok := table[key]
	if !ok {
		if len(table) >= maxTrackedKeys {
			for victim := range table {
				delete(table, victim)
				break
			}
		}
		entry = &failureCount{last: now}
		table[key] = entry
	}
	entry.count = min(entry.remaining(now)+1, ceiling)
	entry.last = now
	return entry
}

// remaining is the count as it stands at now.
func (c *failureCount) remaining(now time.Time) float64 {
	return max(0, c.count-now.Sub(c.last).Seconds()/failureForgottenEvery.Seconds())
}

// sweepLocked drops counts that have leaked away, at most once a minute. The
// caller holds the lock.
func (t *SignInTarpit) sweepLocked(now time.Time) {
	if now.Sub(t.lastSweep) < failureForgottenEvery {
		return
	}
	t.lastSweep = now
	for _, table := range []map[[sha256.Size]byte]*failureCount{t.addresses, t.clients} {
		for key, entry := range table {
			if entry.remaining(now) <= 0 {
				delete(table, key)
			}
		}
	}
}

// rampedDelay is nothing for the first free failures, then first, doubling with
// each further failure, up to ceiling.
func rampedDelay(failures, free int, first, ceiling time.Duration) time.Duration {
	if failures <= free {
		return 0
	}
	delay := first
	for i := free + 1; i < failures && delay < ceiling; i++ {
		delay *= 2
	}
	return min(delay, ceiling)
}

// canonicalSignInAddress is the form sign-in looks an address up by, so spelling
// variants of one address are counted together. What is not a mailbox address
// at all is still counted, as itself.
func canonicalSignInAddress(address string) string {
	if canonical, err := rmail.CanonicalMailboxAddress(address); err == nil {
		return canonical
	}
	return strings.ToLower(strings.TrimSpace(address))
}

// clientPrefix is what counts as one client: an IPv4 address, or an IPv6 /64,
// the block one customer is usually given.
func clientPrefix(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if !addr.Is6() {
		return addr.String()
	}
	prefix, err := addr.WithZone("").Prefix(64)
	if err != nil {
		return ip
	}
	return prefix.String()
}

// maskSignInAddress keeps the first character of the local part and the domain.
func maskSignInAddress(address string) string {
	local, domain, ok := strings.Cut(address, "@")
	first := ""
	for _, r := range local {
		first = string(r)
		break
	}
	if !ok {
		return first + "***"
	}
	return first + "***@" + domain
}
