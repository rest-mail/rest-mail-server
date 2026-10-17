package grants

import (
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// Audit actions of escalation (#346): an eligible grant switched on, switched
// off early, or noticed to have run out; and an active grant its holder made
// eligible.
const (
	ActionActivate     = "grants.activate"
	ActionDeactivate   = "grants.deactivate"
	ActionExpire       = "grants.expire"
	ActionMakeEligible = "grants.make_eligible"
)

// How long an activation lasts: DefaultActivation unless the holder asks for
// another length, and never more than MaxActivation.
const (
	DefaultActivation = 60 * time.Minute
	MaxActivation     = 240 * time.Minute
)

var (
	// ErrNotEligible: the grant is active, always in force, so there is
	// nothing to switch on.
	ErrNotEligible = errors.New("this grant is active: it is always in force, and there is nothing to switch on")
	// ErrAlreadySwitchedOn: the grant has an activation still running.
	ErrAlreadySwitchedOn = errors.New("this grant is already switched on")
	// ErrNotSwitchedOn: the grant has no activation running to switch off.
	ErrNotSwitchedOn = errors.New("this grant is not switched on")
	// ErrGrantExpired: the grant itself has run out.
	ErrGrantExpired = errors.New("this grant has expired")
	// ErrAlreadyEligible: the grant is eligible already.
	ErrAlreadyEligible = errors.New("this grant is eligible already")
)

// Activate switches the eligible grant g on for length, from now, for reason:
// in force until the activation ends, never past the grant's own expiry. It
// records the activation in the audit log, in tx; entry says who is acting and
// through which request. An activation of g that ran out unnoticed is recorded
// as expired first. The caller holds Lock, and has checked that whoever acts
// holds g and has just proven a second factor.
func Activate(tx *gorm.DB, g *models.Grant, length time.Duration, reason string, entry models.AuditEntry) (*models.GrantActivation, error) {
	now := time.Now()
	if g.Mode != models.GrantEligible {
		return nil, ErrNotEligible
	}
	if g.ExpiresAt != nil && !g.ExpiresAt.After(now) {
		return nil, ErrGrantExpired
	}
	if _, err := endExpired(tx, g.ID, now); err != nil {
		return nil, err
	}
	live, err := liveActivation(tx, g.ID, now)
	if err != nil {
		return nil, err
	}
	if live != nil {
		return nil, ErrAlreadySwitchedOn
	}
	ends := now.Add(length)
	if g.ExpiresAt != nil && g.ExpiresAt.Before(ends) {
		ends = *g.ExpiresAt
	}
	a := &models.GrantActivation{GrantID: g.ID, ActivatedAt: now, ExpiresAt: ends, Reason: reason}
	if err := tx.Create(a).Error; err != nil {
		return nil, err
	}
	entry.Action = ActionActivate
	if err := appendActivation(tx, g, a, entry); err != nil {
		return nil, err
	}
	return a, nil
}

// Deactivate switches g off before its activation runs out, recording it in
// the audit log, in tx. ErrNotSwitchedOn when no activation of g is running.
// The caller holds Lock, and has checked that whoever acts holds g.
func Deactivate(tx *gorm.DB, g *models.Grant, entry models.AuditEntry) (*models.GrantActivation, error) {
	now := time.Now()
	if _, err := endExpired(tx, g.ID, now); err != nil {
		return nil, err
	}
	live, err := liveActivation(tx, g.ID, now)
	if err != nil {
		return nil, err
	}
	if live == nil {
		return nil, ErrNotSwitchedOn
	}
	if err := tx.Model(&models.GrantActivation{}).Where("id = ?", live.ID).Update("ended_at", now).Error; err != nil {
		return nil, err
	}
	live.EndedAt = &now
	entry.Action = ActionDeactivate
	if err := appendActivation(tx, g, live, entry); err != nil {
		return nil, err
	}
	return live, nil
}

// MakeEligible turns the active grant g eligible, recording it in the audit
// log, in tx: from then on it gives nothing until switched on. It narrows
// what its holder holds day to day without taking the grant away, so the last
// superadmin may do it too. The caller holds Lock, and has checked that
// whoever acts holds g and has just proven a second factor, so can switch it
// on again.
func MakeEligible(tx *gorm.DB, g *models.Grant, entry models.AuditEntry) error {
	if g.Mode == models.GrantEligible {
		return ErrAlreadyEligible
	}
	if err := tx.Model(&models.Grant{}).Where("id = ?", g.ID).Update("mode", models.GrantEligible).Error; err != nil {
		return err
	}
	g.Mode = models.GrantEligible
	entry.Action = ActionMakeEligible
	return appendChange(tx, g, entry)
}

// LiveActivations are the activations running now of the grants with these
// ids, by grant id.
func LiveActivations(db *gorm.DB, grantIDs []uint, now time.Time) (map[uint]models.GrantActivation, error) {
	out := map[uint]models.GrantActivation{}
	if len(grantIDs) == 0 {
		return out, nil
	}
	var rows []models.GrantActivation
	err := db.Where("grant_id IN ? AND ended_at IS NULL AND expires_at > ?", grantIDs, now).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, a := range rows {
		out[a.GrantID] = a
	}
	return out, nil
}

// EndExpired records every activation that has run out unnoticed as expired:
// it sets its end, and writes its grants.expire entry, as the system, in one
// transaction under Lock. Replicas running it at once record each expiry once.
// It says how many it recorded.
//
// What a grant gives never waits for this: an activation stops counting the
// moment it runs out. This makes the record say so.
func EndExpired(db *gorm.DB, now time.Time) (int, error) {
	var pending int64
	err := db.Model(&models.GrantActivation{}).Where("ended_at IS NULL AND expires_at <= ?", now).Count(&pending).Error
	if err != nil || pending == 0 {
		return 0, err
	}
	ended := 0
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := Lock(tx); err != nil {
			return err
		}
		var err error
		ended, err = endExpired(tx, 0, now)
		return err
	})
	return ended, err
}

// endExpired is EndExpired, in tx, for the grant with id grantID, or for every
// grant when grantID is 0.
func endExpired(tx *gorm.DB, grantID uint, now time.Time) (int, error) {
	q := tx.Where("ended_at IS NULL AND expires_at <= ?", now)
	if grantID != 0 {
		q = q.Where("grant_id = ?", grantID)
	}
	var expired []models.GrantActivation
	if err := q.Order("id").Find(&expired).Error; err != nil {
		return 0, err
	}
	for i := range expired {
		a := &expired[i]
		var g []models.Grant
		if err := tx.Preload("Role").Where("id = ?", a.GrantID).Limit(1).Find(&g).Error; err != nil {
			return i, err
		}
		if len(g) == 0 {
			// Its grant went without it, behind Remove's back: record what is known.
			g = []models.Grant{{ID: a.GrantID}}
		}
		end := a.ExpiresAt
		if err := tx.Model(&models.GrantActivation{}).Where("id = ?", a.ID).Update("ended_at", end).Error; err != nil {
			return i, err
		}
		a.EndedAt = &end
		entry := models.AuditEntry{ActorKind: models.AuditActorSystem, ActorName: "system", Action: ActionExpire}
		if err := appendActivation(tx, &g[0], a, entry); err != nil {
			return i, err
		}
	}
	return len(expired), nil
}

// liveActivation is the activation of the grant with id grantID running now,
// or nil.
func liveActivation(tx *gorm.DB, grantID uint, now time.Time) (*models.GrantActivation, error) {
	var live []models.GrantActivation
	err := tx.Where("grant_id = ? AND ended_at IS NULL AND expires_at > ?", grantID, now).Limit(1).Find(&live).Error
	if err != nil || len(live) == 0 {
		return nil, err
	}
	return &live[0], nil
}

// appendActivation writes the audit entry for a step of a's life: the grant,
// and the activation's reason, start and end.
func appendActivation(tx *gorm.DB, g *models.Grant, a *models.GrantActivation, entry models.AuditEntry) error {
	target := models.AuditTarget{}
	for k, v := range entry.Target {
		target[k] = v
	}
	target["activation_id"] = strconv.FormatUint(uint64(a.ID), 10)
	target["reason"] = a.Reason
	target["started_at"] = a.ActivatedAt.UTC().Format(time.RFC3339)
	target["ends_at"] = a.ExpiresAt.UTC().Format(time.RFC3339)
	if a.EndedAt != nil {
		target["ended_at"] = a.EndedAt.UTC().Format(time.RFC3339)
	}
	entry.Target = target
	return appendChange(tx, g, entry)
}

// SweepInterval is how often a Sweeper looks for activations that have run
// out.
const SweepInterval = time.Minute

// Sweeper records activations that have run out (EndExpired), every interval.
type Sweeper struct {
	db       *gorm.DB
	interval time.Duration
	stop     chan struct{}
}

// NewSweeper is a Sweeper over db. An interval of zero means SweepInterval.
func NewSweeper(db *gorm.DB, interval time.Duration) *Sweeper {
	if interval <= 0 {
		interval = SweepInterval
	}
	return &Sweeper{db: db, interval: interval, stop: make(chan struct{})}
}

// Start runs the sweeper until Shutdown is called.
func (s *Sweeper) Start() {
	go s.run()
}

// Shutdown stops the sweeper.
func (s *Sweeper) Shutdown() {
	close(s.stop)
}

func (s *Sweeper) run() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := EndExpired(s.db, time.Now()); err != nil {
				slog.Error("grants: recording activations that ran out failed", "error", err)
			}
		case <-s.stop:
			return
		}
	}
}
