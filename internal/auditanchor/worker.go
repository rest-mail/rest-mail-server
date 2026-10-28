package auditanchor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// A separate lock serialises publication across API replicas without holding
// up ordinary audit appends during the queries that prepare an anchor.
const publicationLock int64 = 0x61756474616e6368 // "audtanch"

// Worker checks once a minute, including at startup. Its schedule and previous
// anchor come from the audit log, so replicas and restarts share the same limit.
// Each publication and every recipient's queue row commit in one transaction.
type Worker struct {
	db         *gorm.DB
	server     string
	recipients []string
	cancel     context.CancelFunc
	done       sync.WaitGroup
}

// NewWorker defaults an empty recipient list to the installation's owners:
// active accounts with a non-expired superadmin grant at *, active or eligible.
// Ownership keys have no email address registry; ordinary mailbox owner grants
// do not confer ownership of the installation.
func NewWorker(db *gorm.DB, server string, recipients []string) *Worker {
	return &Worker{db: db, server: server, recipients: append([]string(nil), recipients...)}
}

// Start begins the background loop. Call once, then Shutdown before closing DB.
func (w *Worker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			checkCtx, stop := context.WithTimeout(ctx, 15*time.Second)
			if _, err := w.Check(checkCtx, time.Now()); err != nil && ctx.Err() == nil {
				slog.Error("audit anchor email: publication failed", "error", err)
			}
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// Shutdown cancels an in-flight check and waits for its transaction to finish.
func (w *Worker) Shutdown() {
	if w.cancel != nil {
		w.cancel()
		w.done.Wait()
	}
}

// Check queues a due anchor, returning true only after the publication commits.
// An empty chain or no owner addresses leaves the schedule untouched. Delivery
// errors are handled by the normal outbound queue, including bounce notices.
func (w *Worker) Check(ctx context.Context, now time.Time) (bool, error) {
	queued := false
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked bool
		if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", publicationLock).Scan(&locked).Error; err != nil {
			return err
		}
		if !locked {
			return nil
		}
		var previous models.AuditEntry
		if err := tx.Where("action = ? AND outcome = ?", emailAction, models.AuditOutcomeSuccess).Order("seq DESC").Limit(1).Find(&previous).Error; err != nil {
			return err
		}
		// Fast path also avoids querying addresses on every tick inside the limit.
		if previous.Seq > 0 && now.Sub(previous.OccurredAt) < minimumInterval {
			return nil
		}
		last, err := previousHead(previous)
		if err != nil {
			return err
		}
		var head audit.Anchor
		if err := tx.Model(&models.AuditEntry{}).Select("seq, hash").Order("seq DESC").Limit(1).Scan(&head).Error; err != nil {
			return err
		}
		if head.Seq == 0 {
			return nil
		}
		changes, err := adminChanges(tx, last.Seq, head.Seq)
		if err != nil {
			return err
		}
		owners, err := ownerAddresses(tx, now)
		if err != nil {
			return err
		}
		recipients := w.recipients
		if len(recipients) == 0 {
			recipients = owners
		}
		if len(recipients) == 0 {
			return fmt.Errorf("no audit anchor email recipients: configure RESTMAIL_AUDIT_ANCHOR_EMAIL_TO or an installation owner")
		}
		// Send as a hosted mail domain so the outbound worker can DKIM-sign it.
		// When no owner exists yet, an explicit target still works from the server.
		domain := w.server
		if len(owners) > 0 {
			domain = owners[0][strings.LastIndexByte(owners[0], '@')+1:]
		}
		p, err := plan(w.server, "audit-anchor@"+domain, recipients, now, head, previous, changes)
		if err != nil || p == nil {
			return err
		}
		for i := range p.messages {
			if err := tx.Create(&p.messages[i]).Error; err != nil {
				return err
			}
		}
		if err := audit.Append(tx, &p.entry); err != nil {
			return err
		}
		queued = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("queue audit anchor email: %w", err)
	}
	if queued {
		slog.Info("audit anchor email queued")
	}
	return queued, nil
}

// adminChanges spans the successful administrative writes after the last
// anchored seq. HAVING omits an empty aggregate, leaving both times zero.
// Starting at the anchored seq rather than the previous publication's own seq
// includes changes committed between that snapshot and its publication.
func adminChanges(tx *gorm.DB, after, upTo int64) (changeWindow, error) {
	var changes changeWindow
	err := tx.Model(&models.AuditEntry{}).Select("MIN(occurred_at) AS first, MAX(occurred_at) AS last").
		Where("seq > ? AND seq <= ? AND outcome = ?", after, upTo, models.AuditOutcomeSuccess).
		Where(`(route LIKE '/api/v1/admin/%' AND method NOT IN ('GET', 'HEAD', 'OPTIONS')) OR action LIKE 'owner_keys.%' OR action LIKE 'grants.%'`).
		Having("COUNT(*) > 0").
		Scan(&changes).Error
	return changes, err
}

func ownerAddresses(tx *gorm.DB, now time.Time) ([]string, error) {
	var addresses []string
	err := tx.Model(&models.Mailbox{}).Distinct("mailboxes.address").
		Joins("JOIN grants ON grants.holder_id = mailboxes.id AND grants.holder_kind = ?", models.GrantHolderAccount).
		Joins("JOIN roles ON roles.id = grants.role_id").
		Where("mailboxes.active = ? AND roles.name = ? AND grants.scope = ?", true, "superadmin", "*").
		Where("grants.mode IN ? AND (grants.expires_at IS NULL OR grants.expires_at > ?)", []string{models.GrantActive, models.GrantEligible}, now).
		Order("mailboxes.address ASC").Pluck("mailboxes.address", &addresses).Error
	return addresses, err
}
