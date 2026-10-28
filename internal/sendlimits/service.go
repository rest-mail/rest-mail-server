package sendlimits

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const lockKey int64 = 0x73656e646c696d74 // sendlimt; always before audit's lock
var ErrOwnerCeiling = errors.New("owned account send limits must not exceed the owner's limits")

type Service struct {
	Defaults  models.SendLimitValues
	Domain    models.SendLimitValues
	Server    models.SendLimitValues
	PerMinute int64
	Now       func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func AccountScope(id uint) string { return "account:" + strconv.FormatUint(uint64(id), 10) }

type layer struct {
	scope   string
	limits  models.SendLimitValues
	ownerID uint
}
type Hit struct {
	Scope     string    `json:"scope"`
	Dimension string    `json:"dimension"`
	OwnerID   uint      `json:"owner_id"`
	RetryAt   time.Time `json:"retry_at"`
	NoticeID  uint      `json:"-"`
}

// Attempt counts the actual actor and the sending identity, deduplicating their
// ancestors. Local, To, Cc and Bcc recipients all count. Hold runs in the same
// transaction as the hit, owner notice and audit; SMTP leaves Hold nil because
// a 451 leaves the submitting client responsible for the retained message.
type Attempt struct {
	AccountID      uint
	SenderID       uint
	HeaderSenderID uint // SMTP may authorize a distinct From header identity
	Recipients     int64
	Entry          *models.AuditEntry
	BypassAccounts bool // only a human-2FA release, never a machine submission
	Hold           func(*gorm.DB, *Hit) error
	ForceHit       string // an existing outbound pipeline's temporary refusal
}

func lock(db *gorm.DB) error { return db.Exec("SELECT pg_advisory_xact_lock(?)", lockKey).Error }
func policy(db *gorm.DB, scope string, fallback models.SendLimitValues) (models.SendLimitValues, error) {
	var p models.SendLimitPolicy
	err := db.First(&p, "scope = ?", scope).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fallback, nil
	}
	if err != nil {
		return fallback, err
	}
	return p.SendLimitValues, nil
}
func (s *Service) accountLayers(db *gorm.DB, id uint) ([]layer, uint, error) {
	var chain []models.Mailbox
	seen := map[uint]bool{}
	for id != 0 {
		if seen[id] || len(chain) >= 64 {
			return nil, 0, errors.New("invalid account ownership chain")
		}
		seen[id] = true
		var m models.Mailbox
		if err := db.Select("id", "owner_id", "domain_id", "active").First(&m, id).Error; err != nil {
			return nil, 0, err
		}
		if !m.Active {
			return nil, 0, errors.New("inactive account in send ownership chain")
		}
		chain = append(chain, m)
		id = 0
		if m.OwnerID != nil {
			id = *m.OwnerID
		}
	}
	if len(chain) == 0 {
		return nil, 0, errors.New("missing submitting account")
	}
	inherited := s.Defaults
	var layers []layer
	for i := len(chain) - 1; i >= 0; i-- {
		scope := AccountScope(chain[i].ID)
		v, err := policy(db, scope, inherited)
		if err != nil {
			return nil, 0, err
		}
		if i != len(chain)-1 {
			v = Inherit(inherited, &v)
		}
		owner := chain[i].ID
		if chain[i].OwnerID != nil {
			owner = *chain[i].OwnerID
		}
		layers = append(layers, layer{scope: scope, limits: v, ownerID: owner})
		inherited = v
	}
	owner := chain[0].ID
	if chain[0].OwnerID != nil {
		owner = *chain[0].OwnerID
	}
	return layers, owner, nil
}
func (s *Service) EffectiveAccount(db *gorm.DB, id uint) (models.SendLimitValues, error) {
	layers, _, err := s.accountLayers(db, id)
	if err != nil {
		return models.SendLimitValues{}, err
	}
	return layers[len(layers)-1].limits, nil
}

// SetAccount serializes with reservations and policy changes. Callers supply a
// transaction containing authorization and the successful change's audit entry.
func (s *Service) SetAccount(db *gorm.DB, id uint, v models.SendLimitValues) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := lock(tx); err != nil {
			return err
		}
		if !Within(models.SendLimitValues{}, v) {
			return errors.New("send limits must be nonnegative")
		}
		var m models.Mailbox
		if err := tx.First(&m, id).Error; err != nil {
			return err
		}
		if m.OwnerID != nil {
			p, err := s.EffectiveAccount(tx, *m.OwnerID)
			if err != nil {
				return err
			}
			if !Within(p, v) {
				return ErrOwnerCeiling
			}
		}
		p := models.SendLimitPolicy{Scope: AccountScope(id), SendLimitValues: v}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&p).Error
	})
}

func (s *Service) Reserve(db *gorm.DB, a Attempt) (*Hit, error) {
	var hit *Hit
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := lock(tx); err != nil {
			return err
		}
		if a.Recipients < 1 {
			return errors.New("submission needs recipients")
		}
		layers, owner, err := s.accountLayers(tx, a.AccountID)
		if err != nil {
			return err
		}
		sender := a.SenderID
		if sender == 0 {
			sender = a.AccountID
		}
		extra, _, err := s.accountLayers(tx, sender)
		if err != nil {
			return err
		}
		layers = append(layers, extra...)
		if a.HeaderSenderID != 0 && a.HeaderSenderID != sender {
			header, _, err := s.accountLayers(tx, a.HeaderSenderID)
			if err != nil {
				return err
			}
			layers = append(layers, header...)
		}
		// Delegating to another domain does not escape the submitting account's
		// domain ceiling, nor either authenticated sending identity's ceiling.
		for _, id := range []uint{a.AccountID, sender, a.HeaderSenderID} {
			if id == 0 {
				continue
			}
			var mb models.Mailbox
			if err := tx.Select("domain_id").First(&mb, id).Error; err != nil {
				return err
			}
			ds := "domain:" + strconv.FormatUint(uint64(mb.DomainID), 10)
			dv, err := policy(tx, ds, s.Domain)
			if err != nil {
				return err
			}
			layers = append(layers, layer{scope: ds, limits: dv, ownerID: owner})
		}
		sv, err := policy(tx, "*", s.Server)
		if err != nil {
			return err
		}
		layers = append(layers, layer{scope: "*", limits: sv, ownerID: owner})
		now := s.now()
		seen := map[string]bool{}
		var counters []models.SendLimitCounter
		for _, l := range layers {
			if seen[l.scope] {
				continue
			}
			seen[l.scope] = true
			var hour, day models.SendLimitCounter
			for _, w := range []time.Duration{time.Hour, 24 * time.Hour} {
				c := models.SendLimitCounter{Scope: l.scope, WindowSeconds: int(w / time.Second), Start: Bucket(now, w)}
				err := tx.Where("scope = ? AND window_seconds = ? AND start = ?", c.Scope, c.WindowSeconds, c.Start).First(&c).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if w == time.Hour {
					hour = c
				} else {
					day = c
				}
				counters = append(counters, c)
			}
			dimension := Exceeded(l.limits, hour, day, a.Recipients)
			if a.BypassAccounts && strings.HasPrefix(l.scope, "account:") {
				dimension = ""
			}
			if dimension != "" {
				window := time.Hour
				if strings.HasSuffix(dimension, "_day") {
					window = 24 * time.Hour
				}
				hit = &Hit{Scope: l.scope, Dimension: dimension, OwnerID: l.ownerID, RetryAt: Bucket(now, window).Add(window)}
				break
			}
		}
		if hit == nil && a.ForceHit != "" {
			hit = &Hit{Scope: "pipeline", Dimension: a.ForceHit, OwnerID: owner, RetryAt: now.Add(time.Minute)}
		}
		if hit == nil && s.PerMinute > 0 && !a.BypassAccounts {
			c := models.SendLimitCounter{Scope: AccountScope(a.AccountID), WindowSeconds: 60, Start: Bucket(now, time.Minute)}
			err := tx.Where("scope = ? AND window_seconds = ? AND start = ?", c.Scope, 60, c.Start).First(&c).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if c.Messages >= s.PerMinute {
				hit = &Hit{Scope: c.Scope, Dimension: "messages_per_minute", OwnerID: owner, RetryAt: c.Start.Add(time.Minute)}
			} else {
				counters = append(counters, c)
			}
		}
		if hit != nil {
			if a.Entry == nil {
				return errors.New("limit hit requires actor audit")
			}
			e := *a.Entry
			if e.ActorName == "" {
				var actor models.Mailbox
				if err := tx.Select("address").First(&actor, a.AccountID).Error; err != nil {
					return err
				}
				e.ActorName = actor.Address
			}
			e.Action = "send_limits.hit"
			e.Outcome = models.AuditOutcomeDenied
			e.ErrorCode = "send_limit_exceeded"
			e.OccurredAt = now
			e.Target = models.AuditTarget{"scope": hit.Scope, "dimension": hit.Dimension, "owner_id": strconv.FormatUint(uint64(hit.OwnerID), 10), "account_id": strconv.FormatUint(uint64(a.AccountID), 10), "recipients": strconv.FormatInt(a.Recipients, 10), "retry_at": hit.RetryAt.Format(time.RFC3339)}
			if a.Hold != nil {
				if err := a.Hold(tx, hit); err != nil {
					return err
				}
			}
			if err := notice(tx, hit, now); err != nil {
				return err
			}
			return audit.Append(tx, &e)
		}
		for _, c := range counters {
			c.Messages++
			c.Recipients += a.Recipients
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&c).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return hit, err
}

func notice(tx *gorm.DB, hit *Hit, now time.Time) error {
	n := models.SendLimitNotice{OwnerID: hit.OwnerID, Start: Bucket(now, time.Hour)}
	inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&n)
	if inserted.Error != nil {
		return inserted.Error
	}
	if inserted.RowsAffected == 0 {
		return nil
	}
	var owner models.Mailbox
	if err := tx.First(&owner, hit.OwnerID).Error; err != nil {
		return err
	}
	to, _ := json.Marshal([]string{owner.Address})
	body := fmt.Sprintf("Sending has reached %s (%s). Further mail is held: REST Mail retains API submissions; SMTP clients receive a temporary failure and retain their submissions. Sign in with two-factor authentication to review held mail or change send limits. Next budget reset: %s.", hit.Dimension, hit.Scope, hit.RetryAt.Format(time.RFC3339))
	msg := models.Message{MailboxID: owner.ID, Folder: "INBOX", Sender: "postmaster@localhost", Subject: "REST Mail sending limit reached", BodyText: body, RecipientsTo: models.JSONB(to), RecipientsCc: models.JSONB("[]"), ReceivedAt: now, SizeBytes: len(body)}
	if err := tx.Create(&msg).Error; err != nil {
		return err
	}
	hit.NoticeID = msg.ID
	return tx.Model(&n).Update("message_id", msg.ID).Error
}
