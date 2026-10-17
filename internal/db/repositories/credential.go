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

// ErrCredentialNotFound is returned when a mailbox has no such credential, or
// none still unrevoked.
var ErrCredentialNotFound = errors.New("credential not found")

// LastUsedEvery is how often a credential's last_used_at is written: at most
// once a minute, however often it is used.
const LastUsedEvery = time.Minute

// CredentialRepository is the gorm-backed store of credentials (#344): app
// passwords for now. It never sees a secret except to hash it, and never
// deletes a row: a revoked credential stays, for the audit trail.
type CredentialRepository struct {
	db *gorm.DB
}

func NewCredentialRepository(db *gorm.DB) *CredentialRepository {
	return &CredentialRepository{db: db}
}

// appPasswordScope is an app password's scope: for now always its own mailbox,
// with read, write and send.
func appPasswordScope(mailboxID uint) models.CredentialScope {
	return models.CredentialScope{
		MailboxID:    mailboxID,
		Capabilities: []string{models.CredentialCapRead, models.CredentialCapWrite, models.CredentialCapSend},
	}
}

// CreateAppPassword makes an app password for a mailbox, created by the
// mailbox itself, that expires at expiresAt (nil: never). It returns the row
// and the password, which exists nowhere else: the caller shows it once.
func (r *CredentialRepository) CreateAppPassword(mailboxID uint, name string, expiresAt *time.Time) (*models.Credential, string, error) {
	// A public id is 12 random characters out of 62; a clash is all but
	// impossible, but the unique index decides, and a clash just draws again.
	for attempt := 0; ; attempt++ {
		password, publicID, secretHash, err := auth.NewAppPassword()
		if err != nil {
			return nil, "", err
		}
		cred := &models.Credential{
			OwnerKind:     models.CredentialOwnerMailbox,
			OwnerID:       mailboxID,
			Kind:          models.CredentialKindAppPassword,
			Name:          name,
			PublicID:      publicID,
			SecretHash:    secretHash,
			Scope:         appPasswordScope(mailboxID),
			CreatedByKind: models.CredentialOwnerMailbox,
			CreatedByID:   mailboxID,
			ExpiresAt:     expiresAt,
		}
		err = r.db.Create(cred).Error
		if err == nil {
			return cred, password, nil
		}
		if attempt < 2 && isUniqueViolation(err) {
			continue
		}
		return nil, "", fmt.Errorf("failed to store the app password: %w", err)
	}
}

// isUniqueViolation reports whether err is Postgres refusing a duplicate key.
func isUniqueViolation(err error) bool {
	return errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(err.Error(), "SQLSTATE 23505")
}

// ListForMailbox returns a mailbox's credentials that are not revoked, newest
// first. Expired ones are included, so their owner sees them lapse.
func (r *CredentialRepository) ListForMailbox(mailboxID uint) ([]models.Credential, error) {
	var creds []models.Credential
	err := r.db.Where("owner_kind = ? AND owner_id = ? AND revoked_at IS NULL", models.CredentialOwnerMailbox, mailboxID).
		Order("created_at DESC, id DESC").Find(&creds).Error
	return creds, err
}

// Revoke revokes one of a mailbox's credentials at now. It returns
// ErrCredentialNotFound when the mailbox has no such credential still
// unrevoked, so revoking twice, or someone else's, finds nothing.
func (r *CredentialRepository) Revoke(mailboxID, id uint, now time.Time) error {
	res := r.db.Model(&models.Credential{}).
		Where("id = ? AND owner_kind = ? AND owner_id = ? AND revoked_at IS NULL", id, models.CredentialOwnerMailbox, mailboxID).
		Update("revoked_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

// RevokeAppPasswords revokes, at now, every app password of a mailbox not yet
// revoked, expired ones included, and returns them, oldest first. Changing the
// mailbox's password calls it inside the transaction that changes it, so no
// app password made under the old password outlives it. The rows stay,
// revoked, for the audit trail.
func (r *CredentialRepository) RevokeAppPasswords(mailboxID uint, now time.Time) ([]models.Credential, error) {
	var revoked []models.Credential
	err := r.db.Model(&revoked).Clauses(clause.Returning{}).
		Where("owner_kind = ? AND owner_id = ? AND kind = ? AND revoked_at IS NULL",
			models.CredentialOwnerMailbox, mailboxID, models.CredentialKindAppPassword).
		Update("revoked_at", now).Error
	if err != nil {
		return nil, err
	}
	sort.Slice(revoked, func(i, j int) bool { return revoked[i].ID < revoked[j].ID })
	return revoked, nil
}

// VerifyAppPassword checks an app password presented together with the
// address of the mailbox it belongs to, at now. It finds the credential by the
// public id, compares the secret's hash in constant time, and checks that the
// credential belongs to that address, is neither revoked nor expired, and that
// its mailbox is active. It returns the credential and its mailbox, or an
// *auth.AppPasswordRefusal saying why not; any other error means the check
// could not be made.
func (r *CredentialRepository) VerifyAppPassword(address, password string, now time.Time) (*models.Credential, *models.Mailbox, error) {
	publicID, secret, ok := auth.ParseAppPassword(password)
	if !ok {
		return nil, nil, &auth.AppPasswordRefusal{Reason: auth.AppPasswordMalformed}
	}
	var cred models.Credential
	err := r.db.Where("public_id = ? AND kind = ?", publicID, models.CredentialKindAppPassword).Take(&cred).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Compare anyway, against a dummy, so an unknown id costs what a
		// wrong secret does.
		auth.AppPasswordSecretMatches(secret, "")
		return nil, nil, &auth.AppPasswordRefusal{Reason: auth.AppPasswordUnknown, PublicID: publicID}
	}
	if err != nil {
		return nil, nil, err
	}
	if !auth.AppPasswordSecretMatches(secret, cred.SecretHash) {
		return nil, nil, &auth.AppPasswordRefusal{Reason: auth.AppPasswordWrongSecret, PublicID: publicID}
	}
	refused := func(reason string) error {
		id := cred.ID
		return &auth.AppPasswordRefusal{Reason: reason, PublicID: publicID, CredentialID: &id}
	}

	var mailbox models.Mailbox
	if cred.OwnerKind != models.CredentialOwnerMailbox {
		return nil, nil, refused(auth.AppPasswordWrongAddress)
	}
	err = r.db.Take(&mailbox, cred.OwnerID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, refused(auth.AppPasswordWrongAddress)
	}
	if err != nil {
		return nil, nil, err
	}
	switch {
	case mailbox.Address != address:
		return nil, nil, refused(auth.AppPasswordWrongAddress)
	case cred.RevokedAt != nil:
		return nil, nil, refused(auth.AppPasswordRevoked)
	case !cred.Usable(now):
		return nil, nil, refused(auth.AppPasswordExpired)
	case !mailbox.Active:
		return nil, nil, refused(auth.AppPasswordMailboxInactive)
	}
	return &cred, &mailbox, nil
}

// TouchLastUsed records that a credential was used at now, writing at most
// once every LastUsedEvery: the condition is in the UPDATE, so replicas using
// the same credential at once do not all write.
func (r *CredentialRepository) TouchLastUsed(id uint, now time.Time) error {
	return r.db.Model(&models.Credential{}).
		Where("id = ? AND (last_used_at IS NULL OR last_used_at <= ?)", id, now.Add(-LastUsedEvery)).
		Update("last_used_at", now).Error
}

// Usable reports whether a credential can still be used at now: it exists, is
// neither revoked nor expired, and its mailbox is active. The SMTP gateway
// checks it before each message, so a revoked app password stops sending at
// once, not at the next sign-in.
func (r *CredentialRepository) Usable(id uint, now time.Time) (bool, error) {
	var count int64
	err := r.db.Model(&models.Credential{}).
		Joins("JOIN mailboxes ON mailboxes.id = credentials.owner_id AND credentials.owner_kind = ?", models.CredentialOwnerMailbox).
		Where("credentials.id = ? AND credentials.revoked_at IS NULL AND (credentials.expires_at IS NULL OR credentials.expires_at > ?) AND mailboxes.active", id, now).
		Count(&count).Error
	return count > 0, err
}
