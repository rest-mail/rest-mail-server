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
	Scope      string `json:"scope"`
	Capability string `json:"capability"`
}

// Held is what an account holds right now: a capability at a scope for each
// capability of each of its unexpired grants that is active, or eligible and
// switched on (Activate), and implicit own-mailbox capabilities. Receiving is
// an explicit own-mailbox grant so it can be withheld or revoked.
// The zero Held holds nothing.
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

// Capabilities is the legacy global snapshot. Narrower access is reported
// separately by ScopedCapabilities, so old clients never mistake it for global.
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

// ScopedCapabilities is the deduplicated narrower snapshot for clients. It
// authorizes nothing: middleware still reads live grants for every request.
func (h Held) ScopedCapabilities() []Entry {
	seen := map[Entry]bool{}
	out := []Entry{}
	for _, e := range h.entries {
		if e.Scope != All && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Capability < out[j].Capability
	})
	return out
}

// Scopes is where this capability is held, including wildcard capabilities.
func (h Held) Scopes(capability string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range h.entries {
		if (e.Capability == capability || e.Capability == Wildcard) && !seen[e.Scope] {
			seen[e.Scope] = true
			out = append(out, e.Scope)
		}
	}
	sort.Strings(out)
	return out
}

// Within is what h holds that ceiling also holds: each capability both hold,
// at the narrower of two scopes where one covers the other. A wildcard on
// either side stands for the other side's capability. It bounds an owned
// account by its owner (#341, Decided 12).
func (h Held) Within(ceiling Held) Held {
	seen := map[Entry]bool{}
	var out Held
	for _, e := range h.entries {
		for _, c := range ceiling.entries {
			capability := ""
			switch {
			case e.Capability == c.Capability || c.Capability == Wildcard:
				capability = e.Capability
			case e.Capability == Wildcard:
				capability = c.Capability
			default:
				continue
			}
			scope := ""
			if Covers(c.Scope, e.Scope) {
				scope = e.Scope
			} else if Covers(e.Scope, c.Scope) {
				scope = c.Scope
			} else {
				continue
			}
			if entry := (Entry{Scope: scope, Capability: capability}); !seen[entry] {
				seen[entry] = true
				out.entries = append(out.entries, entry)
			}
		}
	}
	return out
}

// IntersectScopes keeps only the narrower scopes shared by both sets. Scopes
// form a hierarchy: two scopes overlap only when one covers the other.
func IntersectScopes(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, left := range a {
		for _, right := range b {
			scope := ""
			if Covers(left, right) {
				scope = right
			} else if Covers(right, left) {
				scope = left
			}
			if scope != "" && !seen[scope] {
				seen[scope] = true
				out = append(out, scope)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Load reads what an account holds at now: its active, unexpired grants, and
// the implicit own-mailbox capabilities (models.OwnerCapabilities except
// mail:receive), which every active account holds without a grant. An inactive
// or missing account holds nothing on its own mailbox, but its grants are still read:
// whoever acts for it is refused elsewhere.
//
// An owned account (models.Mailbox.OwnerID) holds only what its owner holds
// too, read live the same way: narrowing the owner narrows it at once, and an
// inactive owner leaves it holding nothing (#341, Decided 12). The server's
// live capability ceiling also bounds it, except for superadmin at "*".
func Load(db *gorm.DB, accountID uint, now time.Time) (Held, error) {
	held, _, err := load(db, accountID, now)
	if err != nil {
		return Held{}, err
	}
	return withinServer(db, held, now)
}

// load reads actor grants before the server ceiling is applied, and also the
// earliest expiry among grants or activations (zero when none expires). Only
// this actor snapshot is cached; the server ceiling must always be read live.
func load(db *gorm.DB, accountID uint, now time.Time) (Held, time.Time, error) {
	return loadBounded(db, accountID, now, map[uint]bool{})
}

// maxOwnerDepth bounds a chain of owners: an owned account's owner may itself
// be owned, but never this deep.
const maxOwnerDepth = 8

// loadBounded is load for an account reached through the owners in visited.
// An owned account holds only what its owner holds (Within), read the same
// way; an inactive or missing owner, a loop or too long a chain leaves it
// holding nothing.
func loadBounded(db *gorm.DB, accountID uint, now time.Time, visited map[uint]bool) (Held, time.Time, error) {
	if visited[accountID] || len(visited) >= maxOwnerDepth {
		return Held{}, time.Time{}, nil
	}
	visited[accountID] = true
	var rows []struct {
		Scope               string
		Capability          string
		ExpiresAt           *time.Time
		ActivationExpiresAt *time.Time
	}
	err := db.Table("grants AS g").
		Select("g.scope, c.name AS capability, g.expires_at, a.expires_at AS activation_expires_at").
		Joins("JOIN role_capabilities rc ON rc.role_id = g.role_id").
		Joins("JOIN capabilities c ON c.id = rc.capability_id").
		Joins("LEFT JOIN grant_activations a ON a.grant_id = g.id AND a.ended_at IS NULL AND a.expires_at > ?", now).
		Where("g.holder_kind = ? AND g.holder_id = ?", models.GrantHolderAccount, accountID).
		Where("g.mode = ? OR (g.mode = ? AND a.id IS NOT NULL)", models.GrantActive, models.GrantEligible).
		Where("g.expires_at IS NULL OR g.expires_at > ?", now).
		Order("g.scope, c.name").
		Scan(&rows).Error
	if err != nil {
		return Held{}, time.Time{}, err
	}
	var held Held
	var own []struct {
		Address string
		OwnerID *uint
	}
	if err := db.Model(&models.Mailbox{}).Select("address", "owner_id").Where("id = ? AND active", accountID).Scan(&own).Error; err != nil {
		return Held{}, time.Time{}, err
	}
	if len(own) == 1 {
		for _, c := range models.OwnerCapabilities {
			// Receiving is stored as a normal grant so removing it works.
			if c == models.CapMailReceive {
				continue
			}
			held.entries = append(held.entries, Entry{Scope: Mailbox(own[0].Address), Capability: c})
		}
	}
	var expires time.Time
	for _, r := range rows {
		held.entries = append(held.entries, Entry{Scope: r.Scope, Capability: r.Capability})
		for _, at := range []*time.Time{r.ExpiresAt, r.ActivationExpiresAt} {
			if at != nil && (expires.IsZero() || at.Before(expires)) {
				expires = *at
			}
		}
	}
	if len(own) == 1 && own[0].OwnerID != nil {
		// An inactive owner's grants are still read for itself (refused
		// elsewhere), but bound nothing: its owned accounts hold nothing.
		var active int64
		if err := db.Model(&models.Mailbox{}).Where("id = ? AND active", *own[0].OwnerID).Count(&active).Error; err != nil {
			return Held{}, time.Time{}, err
		}
		if active == 0 {
			return Held{}, time.Time{}, nil
		}
		ceiling, ceilingExpires, err := loadBounded(db, *own[0].OwnerID, now, visited)
		if err != nil {
			return Held{}, time.Time{}, err
		}
		held = held.Within(ceiling)
		if !ceilingExpires.IsZero() && (expires.IsZero() || ceilingExpires.Before(expires)) {
			expires = ceilingExpires
		}
	}
	return held, expires, nil
}

// RoleCapabilities lists the capabilities of the role with id roleID.
func RoleCapabilities(db *gorm.DB, roleID uint) ([]string, error) {
	var names []string
	err := db.Table("role_capabilities rc").
		Select("c.name").
		Joins("JOIN capabilities c ON c.id = rc.capability_id").
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
	entries map[uint]cachedHeld
}

type cachedHeld struct {
	held  Held
	until time.Time
}

// NewCache is a Cache over db keeping entries for ttl.
func NewCache(db *gorm.DB, ttl time.Duration) *Cache {
	return &Cache{db: db, ttl: ttl, now: time.Now, entries: map[uint]cachedHeld{}}
}

// Held is what the holder holds now, from the cache or, when it has nothing
// current, from the database. Only actor grants are cached: the server ceiling
// is read on every call, including cache hits and changes from other replicas.
func (c *Cache) Held(accountID uint) (Held, error) {
	key := accountID
	now := c.now()
	c.mu.Lock()
	cached, ok := c.entries[key]
	gen := c.gen
	c.mu.Unlock()
	if ok && now.Before(cached.until) {
		return withinServer(c.db, cached.held, now)
	}

	held, expires, err := load(c.db, accountID, now)
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
			c.entries = map[uint]cachedHeld{}
		}
		c.entries[key] = cachedHeld{held: held, until: until}
	}
	c.mu.Unlock()
	return withinServer(c.db, held, now)
}

// Invalidate forgets everything the cache holds. Call it after any grant
// change has committed.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.gen++
	c.entries = map[uint]cachedHeld{}
	c.mu.Unlock()
}
