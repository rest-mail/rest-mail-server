package handlers

import (
	"errors"
	"sync"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

// An access token is stateless and lives for minutes, so what makes it stop
// working sooner is the account's credential epoch: every token carries the
// epoch it was minted at (cep), and is refused once the account's has moved
// on (RejectStaleSessions, the event stream, refresh). endSessions moves it
// on; SessionCache answers whether a token is still current without a query
// on every request.

// endSessions ends every session of the account, in tx: its refresh tokens
// are revoked and its credential epoch moves on, so every access token it was
// issued is refused at its next request, on the event stream and at refresh,
// wherever it is presented. Its passwords and app passwords are not touched.
// Once tx has committed, the session cache must forget the account
// (SessionCache.Forget).
func endSessions(tx *gorm.DB, accountID uint) error {
	if err := tx.Model(&models.Mailbox{}).Where("id = ?", accountID).
		Update("credential_epoch", gorm.Expr("credential_epoch + 1")).Error; err != nil {
		return err
	}
	return repositories.NewRefreshTokenRepository(tx).RevokeAllForAccount(accountID)
}

// sessionAccount reads what decides whether an account's tokens are current;
// nil when there is no such account.
func sessionAccount(db *gorm.DB, accountID uint) (*models.Mailbox, error) {
	var account models.Mailbox
	err := db.Select("id", "active", "credential_epoch", "password_change_required", "bootstrap_expires_at").
		First(&account, accountID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &account, nil
}

// sessionCurrent reports whether a token minted at epoch is still good for
// account at now: the account exists and is active, is not a bootstrap
// account whose deploy-time password has expired, and has not ended its
// sessions since.
func sessionCurrent(account *models.Mailbox, epoch int, now time.Time) bool {
	return account != nil && account.Active && !account.BootstrapExpired(now) && account.CredentialEpoch == epoch
}

// SessionCacheTTL is how long a SessionCache keeps an account's state. Every
// change made through this process that ends an account's sessions empties
// its entry at once; one made by another process (the operator commands)
// reaches this one within SessionCacheTTL.
const SessionCacheTTL = 5 * time.Second

// maxCachedSessions bounds a SessionCache: past it, the cache starts over.
const maxCachedSessions = 4096

// SessionCache is SessionCurrent with a memory: it reads each account's state
// at most once per SessionCacheTTL, so the check every request makes does not
// cost a query on every request.
type SessionCache struct {
	db  *gorm.DB
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	gen     uint64
	entries map[uint]cachedSession
}

type cachedSession struct {
	account *models.Mailbox // nil: no such account
	until   time.Time
}

// NewSessionCache is a SessionCache over db keeping entries for ttl.
func NewSessionCache(db *gorm.DB, ttl time.Duration) *SessionCache {
	return &SessionCache{db: db, ttl: ttl, now: time.Now, entries: map[uint]cachedSession{}}
}

// Current is middleware.SessionCurrent, from the cache or, when it has
// nothing current for the account, from the database.
func (c *SessionCache) Current(accountID uint, epoch int) (bool, error) {
	now := c.now()
	c.mu.Lock()
	cached, ok := c.entries[accountID]
	gen := c.gen
	c.mu.Unlock()
	if ok && now.Before(cached.until) {
		return sessionCurrent(cached.account, epoch, now), nil
	}

	account, err := sessionAccount(c.db, accountID)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	// Sessions ended since the read began may have made it stale: keep it
	// only if nothing has been forgotten in the meantime.
	if c.gen == gen {
		if len(c.entries) >= maxCachedSessions {
			c.entries = map[uint]cachedSession{}
		}
		c.entries[accountID] = cachedSession{account: account, until: now.Add(c.ttl)}
	}
	c.mu.Unlock()
	return sessionCurrent(account, epoch, now), nil
}

// Forget drops what the cache holds for the account, so its next check reads
// the database. Call it once a change that ends the account's sessions, or
// enables or deletes it, has committed. A nil cache forgets nothing.
func (c *SessionCache) Forget(accountID uint) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.gen++
	delete(c.entries, accountID)
	c.mu.Unlock()
}
