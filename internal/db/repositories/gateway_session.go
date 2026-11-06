package repositories

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// GatewaySessionTokenPrefix starts every gateway session token, so one is told
// from a JWT at a glance.
const GatewaySessionTokenPrefix = "rmgs_"

// ErrGatewaySessionEnded is a token that names no open session: unknown,
// closed, idle too long, or its credential no longer usable.
var ErrGatewaySessionEnded = errors.New("gateway session ended")

// gatewaySessionTouchEvery bounds how often a session's last use is written.
const gatewaySessionTouchEvery = time.Minute

// GatewaySessionRepository keeps the protocol gateways' connection sessions (#1586).
type GatewaySessionRepository struct {
	db *gorm.DB
}

func NewGatewaySessionRepository(db *gorm.DB) *GatewaySessionRepository {
	return &GatewaySessionRepository{db: db}
}

// IsGatewaySessionToken reports whether token is shaped like a gateway session token.
func IsGatewaySessionToken(token string) bool {
	return strings.HasPrefix(token, GatewaySessionTokenPrefix)
}

func hashGatewayToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Open records a new session and returns it with its token, which is never stored.
func (r *GatewaySessionRepository) Open(accountID, credentialID uint, credentialKind, service, protocol string, now time.Time) (*models.GatewaySession, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := GatewaySessionTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	s := &models.GatewaySession{
		TokenHash: hashGatewayToken(token), AccountID: accountID,
		CredentialID: credentialID, CredentialKind: credentialKind,
		Service: service, Protocol: protocol, CreatedAt: now, LastUsedAt: now,
	}
	if err := r.db.Create(s).Error; err != nil {
		return nil, "", err
	}
	return s, token, nil
}

// Use returns the open session token names for service, checked as of now:
// it must be open, opened by service, used within GatewaySessionIdle, and its
// credential still usable (not revoked or expired, the account active). A
// session idle too long, or whose credential is gone, is closed. Anything else
// is ErrGatewaySessionEnded.
func (r *GatewaySessionRepository) Use(token, service string, now time.Time) (*models.GatewaySession, error) {
	var s models.GatewaySession
	err := r.db.Where("token_hash = ? AND closed_at IS NULL", hashGatewayToken(token)).Take(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrGatewaySessionEnded
	}
	if err != nil {
		return nil, err
	}
	// Another service's certificate cannot use it; the session stays open
	// for the service that holds it.
	if s.Service != service {
		return nil, ErrGatewaySessionEnded
	}
	if now.Sub(s.LastUsedAt) > models.GatewaySessionIdle {
		return nil, r.end(s.ID, now)
	}
	usable, err := NewCredentialRepository(r.db).Usable(s.CredentialID, now)
	if err != nil {
		return nil, err
	}
	if !usable {
		return nil, r.end(s.ID, now)
	}
	if now.Sub(s.LastUsedAt) >= gatewaySessionTouchEvery {
		if err := r.db.Model(&models.GatewaySession{}).Where("id = ?", s.ID).Update("last_used_at", now).Error; err != nil {
			return nil, err
		}
		s.LastUsedAt = now
	}
	return &s, nil
}

func (r *GatewaySessionRepository) end(id uint, now time.Time) error {
	if err := r.db.Model(&models.GatewaySession{}).Where("id = ? AND closed_at IS NULL", id).Update("closed_at", now).Error; err != nil {
		return err
	}
	return ErrGatewaySessionEnded
}

// Close ends the session token names, if service opened it. Closing one already
// closed, or unknown, is not an error, so a double logout is safe.
func (r *GatewaySessionRepository) Close(token, service string, now time.Time) error {
	return r.db.Model(&models.GatewaySession{}).
		Where("token_hash = ? AND service = ? AND closed_at IS NULL", hashGatewayToken(token), service).
		Update("closed_at", now).Error
}

// CloseAllForAccount ends every open session of one account.
func (r *GatewaySessionRepository) CloseAllForAccount(accountID uint, now time.Time) error {
	return r.db.Model(&models.GatewaySession{}).
		Where("account_id = ? AND closed_at IS NULL", accountID).
		Update("closed_at", now).Error
}

// CloseIdle ends every session idle for longer than GatewaySessionIdle, and
// returns how many it closed.
func (r *GatewaySessionRepository) CloseIdle(now time.Time) (int64, error) {
	res := r.db.Model(&models.GatewaySession{}).
		Where("closed_at IS NULL AND last_used_at < ?", now.Add(-models.GatewaySessionIdle)).
		Update("closed_at", now)
	return res.RowsAffected, res.Error
}
