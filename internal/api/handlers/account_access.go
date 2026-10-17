package handlers

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// errNoAccess is the answer for an account a request may not act on, the
// same as for one that does not exist, so nobody learns which addresses are
// hosted by asking for them.
var errNoAccess = errors.New("account not found or access denied")

// mayReach finds the account (mailbox) id names, if the request may act on it
// with capability: a mail capability (models.CapMail*) or mailbox:manage. An
// account holds all of them on its own mailbox, and holds what its grants give
// on others: an owner grant on an account it created or linked, or an admin
// role at a scope covering it. A request made with an app password reaches
// only its own mailbox, with what the app password's scope holds (#344).
//
// This is the single source of truth for /accounts/{id}/… and every other
// route that acts on a mailbox. It answers errNoAccess, never another
// account's mailbox, when the request may not.
func mayReach(db *gorm.DB, claims *auth.Claims, id uint, capability string) (*models.Mailbox, error) {
	var mailbox models.Mailbox
	if err := db.First(&mailbox, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errNoAccess
		}
		return nil, err
	}
	if claims.MailboxOnly() {
		if mailbox.ID == claims.AccountID && appPasswordHolds(capability) {
			return &mailbox, nil
		}
		return nil, errNoAccess
	}
	held, err := grants.Load(db, claims.AccountID, time.Now())
	if err != nil {
		return nil, err
	}
	if held.Allows(capability, grants.Mailbox(mailbox.Address)) {
		return &mailbox, nil
	}
	return nil, errNoAccess
}

// appPasswordHolds reports whether an app password's scope holds capability:
// read, write and send on its own mailbox, never mailbox:manage.
func appPasswordHolds(capability string) bool {
	switch capability {
	case models.CapMailRead, models.CapMailWrite, models.CapMailSend:
		return true
	}
	return false
}

// resolveAccountMailbox is mayReach for a handler that needs only the
// mailbox's id.
func resolveAccountMailbox(db *gorm.DB, id uint, claims *auth.Claims, capability string) (uint, error) {
	mailbox, err := mayReach(db, claims, id, capability)
	if err != nil {
		return 0, err
	}
	return mailbox.ID, nil
}

// mailCapabilityFor is what a request on a mailbox's settings needs: mail:read
// to read them, mail:write to change them.
func mailCapabilityFor(r *http.Request) string {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return models.CapMailRead
	}
	return models.CapMailWrite
}

// mayActAs reports whether the request may act as address with capability:
// sending as it (mail:send), most often. An unknown address is refused.
func mayActAs(db *gorm.DB, claims *auth.Claims, address, capability string) (*models.Mailbox, error) {
	var mailbox models.Mailbox
	if err := db.Where("address = ?", address).First(&mailbox).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errNoAccess
		}
		return nil, err
	}
	return mayReach(db, claims, mailbox.ID, capability)
}

// passwordCheckTTL is how long BasicVerifier remembers a password that
// matched.
const passwordCheckTTL = 5 * time.Minute

// maxPasswordChecks bounds the memory passwordChecks may hold; past it, it
// starts afresh.
const maxPasswordChecks = 4096

// passwordChecks is a repositories.PasswordMemo: the passwords that matched in
// the last ttl, each by a SHA-256 of the credential's id, its hash and the
// password, so a new hash, another credential or another password never
// matches what was remembered.
type passwordChecks struct {
	ttl time.Duration
	mu  sync.Mutex
	m   map[[32]byte]time.Time
}

func newPasswordChecks(ttl time.Duration) *passwordChecks {
	return &passwordChecks{ttl: ttl, m: map[[32]byte]time.Time{}}
}

func passwordCheckKey(cred *models.Credential, secret string) [32]byte {
	h := sha256.New()
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], uint64(cred.ID))
	h.Write(id[:])
	h.Write([]byte(cred.SecretHash))
	h.Write([]byte{0})
	h.Write([]byte(secret))
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

func (p *passwordChecks) Remembered(cred *models.Credential, secret string) bool {
	key := passwordCheckKey(cred, secret)
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.m[key]
	if ok && time.Now().Before(until) {
		return true
	}
	delete(p.m, key)
	return false
}

func (p *passwordChecks) Remember(cred *models.Credential, secret string) {
	key := passwordCheckKey(cred, secret)
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.m) >= maxPasswordChecks {
		p.m = map[[32]byte]time.Time{}
	}
	p.m[key] = time.Now().Add(p.ttl)
}

// verify checks Basic credentials, remembering passwords that matched.
func (p *passwordChecks) verify(repo *repositories.CredentialRepository, address, secret string, now time.Time) (*models.Credential, *models.Mailbox, error) {
	return repo.VerifyBasic(address, secret, now, p)
}
