package handlers

import (
	"net/http"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/audit"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
	"gorm.io/gorm"
)

// A domain can be locked (#2012, decided on #1870; made absolute on #2098).
// Soft deletion is reversible, so automation may do it; the lock is for a
// domain whose owner wants deleting it to be a deliberate second step. While
// the lock is set nobody may delete the domain, a person's session with a fresh
// second factor included: the lock must be lifted first, and only a person's
// session with a fresh second factor may lift it, never a key. Anyone who may
// update the domain may set it: it only takes power away.

const automationLockDescription = "automation_locked true locks the domain, so that nobody can delete it until the lock is lifted, and records who locked it and when in automation_lock; locking a locked domain keeps the first lock. false lifts the lock, and needs a person's session with a fresh second factor: anything else, an API key included, is refused with 403 two_factor_required or recent_two_factor_required, and nothing in the request is changed. Locking and lifting are written to the audit log; asking for the state the domain is already in writes nothing."

var errDomainAutomationLocked = endpoint.Refuse(http.StatusForbidden, "automation_locked",
	"This domain is locked and cannot be deleted: lift the lock first, which needs a person signed in with a fresh second factor.")

func automationLockOf(d models.Domain) *apitypes.DomainAutomationLock {
	if d.AutomationLockedAt == nil {
		return nil
	}
	return &apitypes.DomainAutomationLock{LockedAt: *d.AutomationLockedAt, LockedBy: d.AutomationLockedBy}
}

// changeAutomationLock sets or lifts the domain's lock in
// tx, holding the domain's row so a lift is judged against the lock it lifts.
func changeAutomationLock(tx *gorm.DB, r *http.Request, id uint32, locked bool) error {
	var domain models.Domain
	if err := lockDomain(tx, id, &domain); err != nil {
		return err
	}
	if locked == (domain.AutomationLockedAt != nil) {
		return nil
	}
	if !locked {
		if err := RequireFreshTwoFactor(r); err != nil {
			return err
		}
		lockedAt, lockedBy := *domain.AutomationLockedAt, domain.AutomationLockedBy
		if err := tx.Model(&domain).Updates(map[string]any{"automation_locked_at": nil, "automation_locked_by": ""}).Error; err != nil {
			return err
		}
		e := outboundAuditEntry(r, "domain.automation_unlock", domain)
		e.Target["locked_at"] = lockedAt.UTC().Format(time.RFC3339)
		e.Target["locked_by"] = lockedBy
		return audit.Append(tx, &e)
	}
	by := ""
	if c := middleware.GetClaims(r); c != nil {
		by = c.Address
	}
	if err := tx.Model(&domain).Updates(map[string]any{"automation_locked_at": tx.NowFunc().UTC(), "automation_locked_by": by}).Error; err != nil {
		return err
	}
	e := outboundAuditEntry(r, "domain.automation_lock", domain)
	return audit.Append(tx, &e)
}
