package push

import (
	"errors"
	"strconv"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MaxDevicesPerSession caps the devices one session may register. A session
// is one app on one phone, which replaces its registration when its token
// changes; the cap only stops a session filling the table.
const MaxDevicesPerSession = 10

// Why the server removed a device, in its devices.remove audit entry.
const (
	// RemovedSessionEnded: the session that registered it ended (logout,
	// revocation, or its refresh token expired).
	RemovedSessionEnded = "session_ended"
	// RemovedByAPNs: APNs said the token is gone (410 Unregistered,
	// BadDeviceToken or DeviceTokenNotForTopic); the reason is APNs'.
	RemovedByAPNs = "apns"
)

// ActionDeviceRegister and ActionDeviceRemove are the audit actions of a
// device registered and removed.
const (
	ActionDeviceRegister = "devices.register"
	ActionDeviceRemove   = "devices.remove"
)

// ErrTooManyDevices refuses a registration past MaxDevicesPerSession.
var ErrTooManyDevices = errors.New("too many devices registered by this session")

// sweepGrace is how long a session whose refresh token was just rotated is
// left alone by Sweep. A refresh rotates the old row before it saves the new
// one, so for a moment a live session has no active row.
const sweepGrace = 2 * time.Minute

// Store keeps the devices sessions register, and knows which ones new mail in
// a mailbox goes to.
type Store struct {
	db *gorm.DB
}

// NewStore is the device store over db.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// DeviceID is a device's id as the API and the push payload give it: an
// opaque string.
func DeviceID(d *models.PushDevice) string { return strconv.FormatUint(uint64(d.ID), 10) }

// liveSession is the condition that the session of the device aliased d is
// alive: one of its refresh tokens is active and unexpired at @now.
const liveSession = `EXISTS (SELECT 1 FROM refresh_tokens rt
	WHERE rt.session_id = d.session_id AND rt.status = 'active' AND rt.expires_at > @now)`

// SessionAlive reports whether session sid is alive at now: one of its
// refresh tokens is active and unexpired. An access token outlives the
// session it was minted for by up to its own lifetime; a device registered
// with one after a logout would belong to nothing.
func (s *Store) SessionAlive(sid string, now time.Time) (bool, error) {
	var n int64
	err := s.db.Model(&models.RefreshToken{}).
		Where("session_id = ? AND status = ? AND expires_at > ?", sid, models.RefreshTokenActive, now).
		Limit(1).Count(&n).Error
	return n > 0, err
}

// List is the devices session sid registered, oldest first.
func (s *Store) List(sid string) ([]models.PushDevice, error) {
	var devices []models.PushDevice
	err := s.db.Where("session_id = ?", sid).Order("id").Find(&devices).Error
	return devices, err
}

// Register records d for its session, keyed on (session, token): when the
// session already registered the token, it answers with that device and
// created false.
func (s *Store) Register(d *models.PushDevice) (device *models.PushDevice, created bool, err error) {
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var existing []models.PushDevice
		if err := tx.Where("session_id = ? AND token = ?", d.SessionID, d.Token).Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if len(existing) > 0 {
			device = &existing[0]
			return nil
		}
		var n int64
		if err := tx.Model(&models.PushDevice{}).Where("session_id = ?", d.SessionID).Count(&n).Error; err != nil {
			return err
		}
		if n >= MaxDevicesPerSession {
			return ErrTooManyDevices
		}
		// A registration racing this one for the same token wins, and is
		// the answer.
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(d)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			var winner models.PushDevice
			if err := tx.Where("session_id = ? AND token = ?", d.SessionID, d.Token).Take(&winner).Error; err != nil {
				return err
			}
			device = &winner
			return nil
		}
		device, created = d, true
		return nil
	})
	return device, created, err
}

// Remove deletes device id if session sid registered it, and reports whether
// it did.
func (s *Store) Remove(sid string, id uint) (bool, error) {
	res := s.db.Where("id = ? AND session_id = ?", id, sid).Delete(&models.PushDevice{})
	return res.RowsAffected > 0, res.Error
}

// target is one device that new mail in a mailbox goes to, and the account it
// is, among those its session lists.
type target struct {
	DeviceID    uint
	Token       string
	Environment string
	// Account is the mailbox's id in GET /api/v1/accounts: the mailbox's own,
	// since an account is a mailbox.
	Account uint
}

// targets is every APNs device of a live session that lists mailboxID, as
// GET /api/v1/accounts does: its own account, or one it holds an active mail
// grant on at that account's own scope (it owns or has linked it).
func (s *Store) targets(mailboxID uint, now time.Time) ([]target, error) {
	var out []target
	err := s.db.Raw(`
SELECT d.id AS device_id, d.token, d.environment, d.mailbox_id AS account
FROM push_devices d
WHERE d.mailbox_id = @mailbox AND d.platform = @platform AND `+liveSession+`
UNION
SELECT d.id AS device_id, d.token, d.environment, m.id AS account
FROM push_devices d
JOIN grants g ON g.holder_kind = @holder AND g.holder_id = d.mailbox_id AND g.mode = @active
	AND (g.expires_at IS NULL OR g.expires_at > @now)
JOIN role_capabilities rc ON rc.role_id = g.role_id
JOIN capabilities c ON c.id = rc.capability_id AND c.name IN @mail
JOIN mailboxes m ON m.id = @mailbox AND g.scope = 'mailbox:' || lower(m.address)
WHERE d.mailbox_id <> @mailbox AND d.platform = @platform AND `+liveSession,
		map[string]any{
			"mailbox": mailboxID, "platform": models.PushPlatformAPNs, "now": now,
			"holder": models.GrantHolderAccount, "active": models.GrantActive,
			"mail": []string{models.CapMailRead, models.CapMailWrite, models.CapMailSend, "*"},
		}).
		Scan(&out).Error
	return out, err
}

// unread is the mailbox's unread mail in its inbox, counted as the
// folder_update event counts it.
func (s *Store) unread(mailboxID uint) (int64, error) {
	var n int64
	err := s.db.Model(&models.Message{}).
		Where("mailbox_id = ? AND folder = ? AND is_read = ? AND is_deleted = ?", mailboxID, "INBOX", false, false).
		Count(&n).Error
	return n, err
}

// removed is a device deleted by the server, for its audit entry.
type removed struct {
	ID        uint
	MailboxID uint
	Platform  string
}

// removeAudited runs a DELETE … RETURNING id, mailbox_id, platform in a
// transaction, and records each device it deleted as a devices.remove entry
// by the system with reason, in the same transaction.
func (s *Store) removeAudited(reason, apnsReason string, now time.Time, sql string, args map[string]any) (int, error) {
	var gone []removed
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(sql, args).Scan(&gone).Error; err != nil {
			return err
		}
		for _, d := range gone {
			e := &models.AuditEntry{
				OccurredAt: now,
				ActorKind:  models.AuditActorSystem,
				ActorName:  "system",
				Action:     ActionDeviceRemove,
				Outcome:    models.AuditOutcomeSuccess,
				Target: models.AuditTarget{
					"device_id":  strconv.FormatUint(uint64(d.ID), 10),
					"mailbox_id": strconv.FormatUint(uint64(d.MailboxID), 10),
					"platform":   d.Platform,
					"reason":     reason,
				},
			}
			if apnsReason != "" {
				e.Target["apns_reason"] = apnsReason
			}
			if err := audit.Append(tx, e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(gone), nil
}

// RemoveGone deletes a device APNs said is gone, with APNs' reason.
func (s *Store) RemoveGone(id uint, apnsReason string, now time.Time) error {
	if apnsReason == "" {
		apnsReason = "Unregistered"
	}
	_, err := s.removeAudited(RemovedByAPNs, apnsReason, now,
		`DELETE FROM push_devices WHERE id = @id RETURNING id, mailbox_id, platform`,
		map[string]any{"id": id})
	return err
}

// SessionEnded deletes the devices of session sid if it is no longer alive:
// what a logout calls once it has revoked the session's refresh token. A
// logout with an old, already rotated refresh token leaves the session, and
// its devices, alive.
func (s *Store) SessionEnded(sid string, now time.Time) (int, error) {
	return s.removeAudited(RemovedSessionEnded, "", now,
		`DELETE FROM push_devices d WHERE d.session_id = @sid AND NOT `+liveSession+`
RETURNING d.id, d.mailbox_id, d.platform`,
		map[string]any{"sid": sid, "now": now})
}

// Sweep deletes the devices of every session that has ended: revoked (a
// password change, a disabled mailbox, RevokeAllForAccount), or expired. New
// mail goes only to live sessions' devices whatever the sweep has done, so it
// tidies rather than protects. A session refreshed in the last sweepGrace is
// left for the next sweep.
func (s *Store) Sweep(now time.Time) (int, error) {
	return s.removeAudited(RemovedSessionEnded, "", now, `
DELETE FROM push_devices d WHERE NOT EXISTS (SELECT 1 FROM refresh_tokens rt
	WHERE rt.session_id = d.session_id AND (
		(rt.status = 'active' AND rt.expires_at > @now) OR
		(rt.status = 'rotated' AND rt.updated_at > @grace)))
RETURNING d.id, d.mailbox_id, d.platform`,
		map[string]any{"now": now, "grace": now.Add(-sweepGrace)})
}
