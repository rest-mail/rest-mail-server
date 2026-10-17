package grants

import (
	"sort"
	"sync"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// Wildcard is the capability that stands for every capability (superadmin's).
const Wildcard = "*"

// Entry is one capability held at one scope.
type Entry struct {
	Scope      string
	Capability string
}

// Held is what a holder holds right now: a capability at a scope for each
// capability of each of its active, unexpired grants. The zero Held holds
// nothing.
type Held struct {
	entries []Entry
}

// HeldOf is a Held of exactly these entries.
func HeldOf(entries ...Entry) Held { return Held{entries: entries} }

// Allows reports whether the holder holds capability at a scope covering
// target. A target of All needs the capability at "*" itself.
func (h Held) Allows(capability, target string) bool {
	for _, e := range h.entries {
		if (e.Capability == Wildcard || e.Capability == capability) && Covers(e.Scope, target) {
			return true
		}
	}
	return false
}

// AllowsSomewhere reports whether the holder holds capability at any scope.
func (h Held) AllowsSomewhere(capability string) bool {
	for _, e := range h.entries {
		if e.Capability == Wildcard || e.Capability == capability {
			return true
		}
	}
	return false
}

// CanGrant reports whether the holder may grant (or take away) a role made of
// capabilities at scope: only if it holds every one of them at a scope
// covering it. Superadmin at "*" may grant anything.
func (h Held) CanGrant(capabilities []string, scope string) bool {
	for _, c := range capabilities {
		if !h.Allows(c, scope) {
			return false
		}
	}
	return true
}

// Capabilities is what the holder holds at "*", sorted, for a client to show
// what it can do: ["*"] alone when that includes everything. What it holds
// only at narrower scopes is not listed, as no client yet shows scoped
// access, and listing a capability held on one domain would show functions
// that answer 403.
func (h Held) Capabilities() []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range h.entries {
		if e.Scope != All || seen[e.Capability] {
			continue
		}
		if e.Capability == Wildcard {
			return []string{Wildcard}
		}
		seen[e.Capability] = true
		out = append(out, e.Capability)
	}
	sort.Strings(out)
	return out
}

// Load reads what a holder holds now, straight from its grants: in a
// transaction, as of that transaction.
func Load(db *gorm.DB, holderKind string, holderID uint, now time.Time) (Held, error) {
	held, _, err := load(db, holderKind, holderID, now)
	return held, err
}

// load is Load, and also the earliest time one of the grants it read expires
// (zero when none does), after which what it read is out of date.
func load(db *gorm.DB, holderKind string, holderID uint, now time.Time) (Held, time.Time, error) {
	var rows []struct {
		Scope      string
		Capability string
		ExpiresAt  *time.Time
	}
	err := db.Table("grants AS g").
		Select("g.scope, c.name AS capability, g.expires_at").
		Joins("JOIN admin_roles_capabilities rc ON rc.role_id = g.role_id").
		Joins("JOIN admin_capabilities c ON c.id = rc.capability_id").
		Where("g.holder_kind = ? AND g.holder_id = ? AND g.mode = ?", holderKind, holderID, models.GrantActive).
		Where("g.expires_at IS NULL OR g.expires_at > ?", now).
		Order("g.scope, c.name").
		Scan(&rows).Error
	if err != nil {
		return Held{}, time.Time{}, err
	}
	var held Held
	var expires time.Time
	for _, r := range rows {
		held.entries = append(held.entries, Entry{Scope: r.Scope, Capability: r.Capability})
		if r.ExpiresAt != nil && (expires.IsZero() || r.ExpiresAt.Before(expires)) {
			expires = *r.ExpiresAt
		}
	}
	return held, expires, nil
}

// RoleCapabilities lists the capabilities of the role with id roleID.
func RoleCapabilities(db *gorm.DB, roleID uint) ([]string, error) {
	var names []string
	err := db.Table("admin_roles_capabilities rc").
		Select("c.name").
		Joins("JOIN admin_capabilities c ON c.id = rc.capability_id").
		Where("rc.role_id = ?", roleID).
		Order("c.name").
		Pluck("c.name", &names).Error
	return names, err
}

// CacheTTL is how long a Cache keeps what a holder holds. Every grant change
// made through this process empties the cache at once; one made through
// another replica reaches this one within CacheTTL.
const CacheTTL = 5 * time.Second

// maxCached bounds a Cache: past it, the cache starts over.
const maxCached = 4096

// Cache keeps what each holder holds for up to CacheTTL, so a check on every
// request does not cost a query on every request.
type Cache struct {
	db  *gorm.DB
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	gen     uint64
	entries map[holderKey]cachedHeld
}

type holderKey struct {
	kind string
	id   uint
}

type cachedHeld struct {
	held  Held
	until time.Time
}

// NewCache is a Cache over db keeping entries for ttl.
func NewCache(db *gorm.DB, ttl time.Duration) *Cache {
	return &Cache{db: db, ttl: ttl, now: time.Now, entries: map[holderKey]cachedHeld{}}
}

// Held is what the holder holds now, from the cache or, when it has nothing
// current, from the database.
func (c *Cache) Held(holderKind string, holderID uint) (Held, error) {
	key := holderKey{holderKind, holderID}
	now := c.now()
	c.mu.Lock()
	cached, ok := c.entries[key]
	gen := c.gen
	c.mu.Unlock()
	if ok && now.Before(cached.until) {
		return cached.held, nil
	}

	held, expires, err := load(c.db, holderKind, holderID, now)
	if err != nil {
		return Held{}, err
	}
	until := now.Add(c.ttl)
	if !expires.IsZero() && expires.Before(until) {
		until = expires
	}
	c.mu.Lock()
	// A change since the read began may have made it stale: keep it only if
	// nothing has been invalidated in the meantime.
	if c.gen == gen {
		if len(c.entries) >= maxCached {
			c.entries = map[holderKey]cachedHeld{}
		}
		c.entries[key] = cachedHeld{held: held, until: until}
	}
	c.mu.Unlock()
	return held, nil
}

// Invalidate forgets everything the cache holds. Call it after any grant
// change has committed.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.gen++
	c.entries = map[holderKey]cachedHeld{}
	c.mu.Unlock()
}
