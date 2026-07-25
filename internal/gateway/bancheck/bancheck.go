package bancheck

import (
	"sync"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/gateway/connlimiter"
	"gorm.io/gorm"
)

// banCacheTTL bounds how long a ban-lookup result is reused before the DB is
// consulted again. It is short so a newly created or lifted ban takes effect
// promptly, while still collapsing the DB hit that a burst of connections (or
// auth failures) from one IP would otherwise cause on every check.
const banCacheTTL = 10 * time.Second

// banLookup reports whether ip is banned for proto, returning any DB error so
// the caller can apply the fail-closed policy instead of silently reading an
// outage as "not banned".
type banLookup func(ip, proto string) (bool, error)

// Wire sets up DB-backed ban checking on the limiter for the given protocol.
func Wire(limiter *connlimiter.Limiter, database *gorm.DB, protocol string) {
	if database == nil {
		return
	}
	lookup := func(ip, proto string) (bool, error) {
		var count int64
		res := database.Model(&models.Ban{}).
			Where("ip = ? AND (protocol = ? OR protocol = 'all') AND (expires_at IS NULL OR expires_at > ?)",
				ip, proto, time.Now()).
			Count(&count)
		if res.Error != nil {
			return false, res.Error
		}
		return count > 0, nil
	}
	limiter.SetBanChecker(newCachedChecker(lookup, banCacheTTL), protocol)
}

type banCacheEntry struct {
	banned    bool
	fetchedAt time.Time
}

// newCachedChecker wraps a banLookup with a short-TTL result cache and a
// fail-closed policy, for the gateway auth paths (SMTP/IMAP/POP3 admission).
//
// It replaces a naive per-connection DB query that had two problems:
//   - it hit the DB on every connection/auth-fail (an uncached load), and
//   - it failed OPEN on a DB error: a swallowed Count error read as count==0, so
//     during a DB outage a banned IP was silently admitted.
//
// The cache collapses repeated checks for the same ip/proto within ttl. On a DB
// error the last known verdict (if any) is served, so a transient blip neither
// locks out a known-good IP nor un-bans a known-bad one; only an IP never seen
// before, during an outage, is failed CLOSED (treated as banned) — the secure
// default for an auth path, at the cost of refusing unknown clients until the DB
// recovers.
func newCachedChecker(lookup banLookup, ttl time.Duration) connlimiter.BanChecker {
	var mu sync.Mutex
	cache := map[string]banCacheEntry{}
	return func(ip, proto string) bool {
		key := proto + "\x00" + ip
		now := time.Now()

		mu.Lock()
		entry, ok := cache[key]
		fresh := ok && now.Sub(entry.fetchedAt) < ttl
		mu.Unlock()
		if fresh {
			return entry.banned
		}

		banned, err := lookup(ip, proto)
		if err != nil {
			// DB unavailable: prefer the last known verdict; fail closed if none.
			if ok {
				return entry.banned
			}
			return true
		}

		mu.Lock()
		cache[key] = banCacheEntry{banned: banned, fetchedAt: now}
		mu.Unlock()
		return banned
	}
}
