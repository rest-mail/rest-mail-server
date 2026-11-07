package handlers

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

// A refusal says only what the caller may already know (#1327). If the caller
// may not know the account exists, the answer is the same as for one that does
// not, so nobody learns which accounts are hosted by asking for them: errNoAccess,
// a 404. If the caller can see the account and this action is what it lacks, it
// is told so: errNotAllowed, a 403.
var (
	errNoAccess   = errors.New("account not found or access denied")
	errNotAllowed = endpoint.Refuse(http.StatusForbidden, "forbidden", "Access denied")
)

// mayReach finds the account (mailbox) id names, if the request may act on it
// with capability: a mail capability (models.CapMail*) or mailbox:manage. An
// account holds all of them on its own mailbox, and holds what its grants give
// on others: an owner grant on an account it created or linked, or an admin
// role at a scope covering it. A request made with an app password reaches
// only its own mailbox, with what the app password's scope holds (#344) and
// the account still holds there. Grants are read with grants.Load on every
// call, so the server's capability ceiling applies to sessions already open.
//
// This is the single source of truth for /accounts/{id}/… and every other
// route that acts on a mailbox. It answers errNoAccess or errNotAllowed, never
// another account's mailbox, when the request may not.
func mayReach(db *gorm.DB, claims *auth.Claims, id uint, capability string) (*models.Mailbox, error) {
	var mailbox models.Mailbox
	if err := db.First(&mailbox, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errNoAccess
		}
		return nil, err
	}
	if claims.MailboxOnly() && mailbox.ID != claims.AccountID {
		return nil, errNoAccess
	}
	if claims.MailboxOnly() && !appPasswordHolds(capability) {
		return nil, errNotAllowed
	}
	// An app password's scope is read live too: it holds only what its account
	// holds now, within the server's capability ceiling, so a protocol gateway
	// session signed in before a capability was removed is refused on its
	// next call.
	held, err := grants.Load(db, claims.AccountID, time.Now())
	if err != nil {
		return nil, err
	}
	if claims.MailboxOnly() {
		if held.Allows(capability, grants.Mailbox(mailbox.Address)) {
			return &mailbox, nil
		}
		return nil, errNotAllowed
	}
	if held.Allows(capability, grants.Mailbox(mailbox.Address)) &&
		middleware.CredentialAllows(claims, capability, grants.Mailbox(mailbox.Address)) {
		return &mailbox, nil
	}
	if mayKnowOf(held, claims, &mailbox) {
		return nil, errNotAllowed
	}
	return nil, errNoAccess
}

// mayKnowOf reports whether the caller already knows this account exists: it is
// its own, or this credential holds any capability on it, such as write without
// read. A key scoped to some other account does not know of this one, whatever
// the account holding the key may do.
func mayKnowOf(held grants.Held, claims *auth.Claims, mailbox *models.Mailbox) bool {
	if mailbox.ID == claims.AccountID {
		return true
	}
	scope := grants.Mailbox(mailbox.Address)
	for _, capability := range []string{models.CapMailRead, models.CapMailWrite, models.CapMailImport, models.CapMailSend,
		models.CapMailSendAs, models.CapMailSendOnBehalf, models.CapMailboxManage} {
		if held.Allows(capability, scope) && middleware.CredentialAllows(claims, capability, scope) {
			return true
		}
	}
	return false
}

// refusedAccount reports whether err is either refusal of an account.
func refusedAccount(err error) bool {
	return errors.Is(err, errNoAccess) || errors.Is(err, errNotAllowed)
}

// appPasswordHolds reports whether an app password's scope holds capability:
// read, write, import and send on its own mailbox, never mailbox:manage. A mail
// client's APPEND and COPY are imports, so an app password needs it.
func appPasswordHolds(capability string) bool {
	switch capability {
	case models.CapMailRead, models.CapMailWrite, models.CapMailImport, models.CapMailSend:
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

// passwordCheckTTL is how long PasswordVerifier remembers a password that
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

// verify checks a mail client's address and password, remembering passwords that matched.
func (p *passwordChecks) verify(repo *repositories.CredentialRepository, address, secret string, now time.Time) (*models.Credential, *models.Mailbox, error) {
	return repo.VerifyPassword(address, secret, now, p)
}
