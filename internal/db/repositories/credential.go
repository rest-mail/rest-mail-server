package repositories

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrCredentialNotFound is returned when there is no such credential of the
// account's, or it is already revoked.
var ErrCredentialNotFound = errors.New("credential not found")

// LastUsedEvery is how often, at most, a credential's last_used_at is
// written: a client polling every few seconds must not write on every
// request.
const LastUsedEvery = time.Minute

// CredentialRepository keeps credentials: everything an account signs in with.
type CredentialRepository struct {
	db *gorm.DB
}

func NewCredentialRepository(db *gorm.DB) *CredentialRepository {
	return &CredentialRepository{db: db}
}

// appPasswordScope is an app password's scope: its account's own mailbox, with
// read, write and send.
func appPasswordScope(accountID uint) models.CredentialScope {
	return models.CredentialScope{
		MailboxID:    accountID,
		Capabilities: []string{models.CredentialCapRead, models.CredentialCapWrite, models.CredentialCapSend},
	}
}

// accountScope is the scope of an interactive credential (a password, a TOTP
// secret, a recovery code): the account itself.
func accountScope(accountID uint) models.CredentialScope {
	return models.CredentialScope{MailboxID: accountID, Capabilities: []string{}}
}

// create stores cred under a fresh public id, trying again on the rare
// collision.
func (r *CredentialRepository) create(cred *models.Credential) error {
	for attempt := 0; ; attempt++ {
		if cred.PublicID == "" {
			id, err := auth.NewPublicID()
			if err != nil {
				return err
			}
			cred.PublicID = id
		}
		err := r.db.Create(cred).Error
		if err == nil {
			return nil
		}
		if attempt < 2 && IsUniqueViolation(err) {
			cred.ID, cred.PublicID = 0, ""
			continue
		}
		return fmt.Errorf("failed to store the credential: %w", err)
	}
}

// CreateAppPassword makes a new app password for an account and returns it,
// with the only copy of the password there will ever be. createdBy is the
// account making it.
func (r *CredentialRepository) CreateAppPassword(accountID, createdBy uint, name string, expiresAt *time.Time) (*models.Credential, string, error) {
	for attempt := 0; ; attempt++ {
		password, publicID, secretHash, err := auth.NewAppPassword()
		if err != nil {
			return nil, "", err
		}
		by := createdBy
		cred := &models.Credential{
			AccountID:   accountID,
			Kind:        models.CredentialKindAppPassword,
			Name:        name,
			PublicID:    publicID,
			SecretHash:  secretHash,
			Scope:       appPasswordScope(accountID),
			CreatedByID: &by,
			ExpiresAt:   expiresAt,
		}
		err = r.db.Create(cred).Error
		if err == nil {
			return cred, password, nil
		}
		if attempt < 2 && IsUniqueViolation(err) {
			continue
		}
		return nil, "", fmt.Errorf("failed to store the app password: %w", err)
	}
}

// IsUniqueViolation reports whether err is Postgres refusing a duplicate key.
func IsUniqueViolation(err error) bool {
	return errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(err.Error(), "SQLSTATE 23505")
}

// ListForAccount lists an account's credentials of the given kinds that are
// not revoked, newest first. Expired ones are listed.
func (r *CredentialRepository) ListForAccount(accountID uint, kinds ...string) ([]models.Credential, error) {
	var creds []models.Credential
	q := r.db.Where("account_id = ? AND revoked_at IS NULL", accountID)
	if len(kinds) > 0 {
		q = q.Where("kind IN ?", kinds)
	}
	err := q.Order("created_at DESC, id DESC").Find(&creds).Error
	return creds, err
}

// Revoke revokes one of an account's app passwords. The row stays, for the
// audit trail.
func (r *CredentialRepository) Revoke(accountID, id uint, now time.Time) error {
	res := r.db.Model(&models.Credential{}).
		Where("id = ? AND account_id = ? AND kind = ? AND revoked_at IS NULL", id, accountID, models.CredentialKindAppPassword).
		Update("revoked_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

// RevokeKinds revokes every unrevoked credential of the account's of the
// given kinds, expired ones too, and returns them, in id order. Rows are kept,
// marked revoked.
func (r *CredentialRepository) RevokeKinds(accountID uint, now time.Time, kinds ...string) ([]models.Credential, error) {
	var revoked []models.Credential
	err := r.db.Model(&revoked).Clauses(clause.Returning{}).
		Where("account_id = ? AND kind IN ? AND revoked_at IS NULL", accountID, kinds).
		Update("revoked_at", now).Error
	if err != nil {
		return nil, err
	}
	sort.Slice(revoked, func(i, j int) bool { return revoked[i].ID < revoked[j].ID })
	return revoked, nil
}

// RevokeAppPasswords revokes every unrevoked app password, API key and client secret of the
// account's, expired ones too, and returns them: what a new password does,
// since whoever knew the old one could have made them.
func (r *CredentialRepository) RevokeAppPasswords(accountID uint, now time.Time) ([]models.Credential, error) {
	return r.RevokeKinds(accountID, now, models.CredentialKindAppPassword, models.CredentialKindAPIKey, models.CredentialKindClientSecret)
}

// SetPassword makes hash the account's one password: every password it had is
// revoked, and a new password credential holds hash. createdBy is the account
// setting it, or nil for the system. Run it in the transaction that makes the
// change, with whatever else the change revokes.
func (r *CredentialRepository) SetPassword(accountID uint, hash string, createdBy *uint, now time.Time) (*models.Credential, error) {
	if _, err := r.RevokeKinds(accountID, now, models.CredentialKindPassword); err != nil {
		return nil, err
	}
	cred := &models.Credential{
		AccountID:   accountID,
		Kind:        models.CredentialKindPassword,
		Name:        "Password",
		SecretHash:  hash,
		Scope:       accountScope(accountID),
		CreatedByID: createdBy,
	}
	if err := r.create(cred); err != nil {
		return nil, err
	}
	return cred, nil
}

// Passwords lists the account's usable password credentials.
func (r *CredentialRepository) Passwords(accountID uint, now time.Time) ([]models.Credential, error) {
	var creds []models.Credential
	err := r.db.Where("account_id = ? AND kind = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)",
		accountID, models.CredentialKindPassword, now).
		Order("id").Find(&creds).Error
	return creds, err
}

// HasPassword reports whether the account has a usable password: whether it
// can be signed in to at all.
func (r *CredentialRepository) HasPassword(accountID uint, now time.Time) (bool, error) {
	var count int64
	err := r.db.Model(&models.Credential{}).
		Where("account_id = ? AND kind = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)",
			accountID, models.CredentialKindPassword, now).
		Count(&count).Error
	return count > 0, err
}

// PasswordMemo remembers passwords that have just matched, so a client sending
// the same Basic credentials on every request is not charged a bcrypt
// comparison each time. Remembered must answer true only for exactly the
// credential (its id and hash) and secret it was told about.
type PasswordMemo interface {
	Remembered(cred *models.Credential, secret string) bool
	Remember(cred *models.Credential, secret string)
}

// MatchPassword finds which of the account's passwords password is, or nil.
// An account with no password (or no account: pass 0) still costs one bcrypt
// comparison, against a dummy hash, so a wrong address, an account nobody can
// sign in to and a wrong password take the same time.
func (r *CredentialRepository) MatchPassword(accountID uint, password string, now time.Time) (*models.Credential, error) {
	return r.matchPassword(accountID, password, now, nil)
}

func (r *CredentialRepository) matchPassword(accountID uint, password string, now time.Time, memo PasswordMemo) (*models.Credential, error) {
	var creds []models.Credential
	if accountID != 0 {
		var err error
		if creds, err = r.Passwords(accountID, now); err != nil {
			return nil, err
		}
	}
	if len(creds) == 0 {
		_ = auth.CheckPassword(password, auth.DummyPasswordHash)
		return nil, nil
	}
	if memo != nil {
		for i := range creds {
			if memo.Remembered(&creds[i], password) {
				return &creds[i], nil
			}
		}
	}
	for i := range creds {
		if auth.CheckPassword(password, creds[i].SecretHash) == nil {
			if auth.PasswordNeedsRehash(password, creds[i].SecretHash) {
				hash, err := auth.HashPassword(password)
				if err != nil {
					return nil, fmt.Errorf("upgrade imported password: %w", err)
				}
				// Re-encoding the same password keeps its credential id, grants,
				// sessions and audit history. Never overwrite a concurrent reset,
				// replacement or revocation with this proof of the old password.
				res := r.db.Model(&models.Credential{}).
					Where("id = ? AND secret_hash = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", creds[i].ID, creds[i].SecretHash, now).
					Update("secret_hash", hash)
				if res.Error != nil {
					return nil, fmt.Errorf("store upgraded password: %w", res.Error)
				}
				if res.RowsAffected == 0 {
					return nil, nil
				}
				creds[i].SecretHash = hash
			}
			if memo != nil {
				memo.Remember(&creds[i], password)
			}
			return &creds[i], nil
		}
	}
	return nil, nil
}

// HasTwoFactor reports whether the account has a confirmed, unrevoked TOTP
// credential: whether signing in needs a code.
func (r *CredentialRepository) HasTwoFactor(accountID uint) (bool, error) {
	var count int64
	err := r.db.Model(&models.Credential{}).
		Where("account_id = ? AND kind = ? AND revoked_at IS NULL AND confirmed_at IS NOT NULL", accountID, models.CredentialKindTOTP).
		Count(&count).Error
	return count > 0, err
}

// VerifyBasic checks what a client sent with HTTP Basic: an app password,
// recognised by its format, or else one of the account's passwords. It is the
// one way the protocol gateways sign in. A password is refused for an account
// that has two-factor authentication (a mail client cannot send a code: it
// needs an app password) or is still in setup.
//
// A refusal is a *auth.CredentialRefusal; any other error means the
// credential could not be checked.
//
// memo, when not nil, remembers passwords that matched (see PasswordMemo).
func (r *CredentialRepository) VerifyBasic(address, secret string, now time.Time, memo PasswordMemo) (*models.Credential, *models.Mailbox, error) {
	if auth.IsAppPassword(secret) {
		return r.VerifyAppPassword(address, secret, now)
	}
	return r.verifyPassword(address, secret, now, memo)
}

func (r *CredentialRepository) verifyPassword(address, password string, now time.Time, memo PasswordMemo) (*models.Credential, *models.Mailbox, error) {
	var mailbox models.Mailbox
	err := FindMailboxByAddress(r.db, address, &mailbox)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, err
	}
	cred, err := r.matchPassword(mailbox.ID, password, now, memo)
	if err != nil {
		return nil, nil, err
	}
	if cred == nil {
		return nil, nil, &auth.CredentialRefusal{Reason: auth.CredentialWrongPassword}
	}
	refused := func(reason string) error {
		id := cred.ID
		return &auth.CredentialRefusal{Reason: reason, PublicID: cred.PublicID, CredentialID: &id}
	}
	switch {
	case !mailbox.Active:
		return nil, nil, refused(auth.CredentialAccountInactive)
	case mailbox.PasswordChangeRequired || mailbox.BootstrapExpired(now):
		return nil, nil, refused(auth.CredentialAccountInSetup)
	}
	twoFactor, err := r.HasTwoFactor(mailbox.ID)
	if err != nil {
		return nil, nil, err
	}
	if twoFactor {
		return nil, nil, refused(auth.CredentialNeedsAppPassword)
	}
	return cred, &mailbox, nil
}

// VerifyAppPassword checks an app password presented for address. It finds
// the credential by its public id and compares the secret's hash in constant
// time; an unknown public id is compared against a dummy hash, so it costs the
// same. Only once the secret matches does it say why a real credential is
// refused (revoked, expired, another account's, an inactive account).
func (r *CredentialRepository) VerifyAppPassword(address, password string, now time.Time) (*models.Credential, *models.Mailbox, error) {
	publicID, secret, ok := auth.ParseAppPassword(password)
	if !ok {
		return nil, nil, &auth.CredentialRefusal{Reason: auth.CredentialWrongSecret}
	}
	var cred models.Credential
	err := r.db.Where("public_id = ? AND kind = ?", publicID, models.CredentialKindAppPassword).Take(&cred).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		auth.AppPasswordSecretMatches(secret, "")
		return nil, nil, &auth.CredentialRefusal{Reason: auth.CredentialUnknown, PublicID: publicID}
	}
	if err != nil {
		return nil, nil, err
	}
	if !auth.AppPasswordSecretMatches(secret, cred.SecretHash) {
		return nil, nil, &auth.CredentialRefusal{Reason: auth.CredentialWrongSecret, PublicID: publicID}
	}
	refused := func(reason string) error {
		id := cred.ID
		return &auth.CredentialRefusal{Reason: reason, PublicID: publicID, CredentialID: &id}
	}

	var mailbox models.Mailbox
	err = r.db.Take(&mailbox, cred.AccountID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, refused(auth.CredentialWrongAddress)
	}
	if err != nil {
		return nil, nil, err
	}
	var addressed models.Mailbox
	addressErr := FindMailboxByAddress(r.db, address, &addressed)
	if addressErr != nil && !errors.Is(addressErr, gorm.ErrRecordNotFound) {
		return nil, nil, addressErr
	}
	switch {
	case addressErr != nil || addressed.ID != mailbox.ID:
		return nil, nil, refused(auth.CredentialWrongAddress)
	case cred.RevokedAt != nil:
		return nil, nil, refused(auth.CredentialRevoked)
	case !cred.Usable(now):
		return nil, nil, refused(auth.CredentialExpired)
	case !mailbox.Active:
		return nil, nil, refused(auth.CredentialAccountInactive)
	}
	return &cred, &mailbox, nil
}

// TouchLastUsed records that a credential was used at now, unless it was
// already recorded less than LastUsedEvery before.
func (r *CredentialRepository) TouchLastUsed(id uint, now time.Time) error {
	return r.db.Model(&models.Credential{}).
		Where("id = ? AND (last_used_at IS NULL OR last_used_at <= ?)", id, now.Add(-LastUsedEvery)).
		Update("last_used_at", now).Error
}

// Usable reports whether a credential may still be used at now: not revoked,
// not expired, and its account active.
func (r *CredentialRepository) Usable(id uint, now time.Time) (bool, error) {
	var count int64
	err := r.db.Model(&models.Credential{}).
		Joins("JOIN mailboxes ON mailboxes.id = credentials.account_id").
		Where("credentials.id = ? AND credentials.revoked_at IS NULL AND (credentials.expires_at IS NULL OR credentials.expires_at > ?) AND mailboxes.active", id, now).
		Count(&count).Error
	return count > 0, err
}
