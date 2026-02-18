package digest

import (
	"log/slog"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// QuotaReconciler periodically recalculates mailbox quota usage
// from actual message sizes to fix any drift.
type QuotaReconciler struct {
	db       *gorm.DB
	interval time.Duration
	stop     chan struct{}
}

// NewQuotaReconciler creates a new reconciler that runs every interval.
func NewQuotaReconciler(db *gorm.DB, interval time.Duration) *QuotaReconciler {
	return &QuotaReconciler{
		db:       db,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

// Start begins the periodic reconciliation loop in a goroutine.
func (q *QuotaReconciler) Start() {
	go q.loop()
	slog.Info("quota reconciler started", "interval", q.interval)
}

// Shutdown stops the reconciler.
func (q *QuotaReconciler) Shutdown() {
	close(q.stop)
}

func (q *QuotaReconciler) loop() {
	// Run once on startup after a brief delay
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-q.stop:
			return
		case <-timer.C:
			q.reconcile()
			timer.Reset(q.interval)
		}
	}
}

func (q *QuotaReconciler) reconcile() {
	start := time.Now()

	// Get all active mailboxes
	var mailboxes []models.Mailbox
	if err := q.db.Where("active = ?", true).Find(&mailboxes).Error; err != nil {
		slog.Error("quota reconciler: failed to list mailboxes", "error", err)
		return
	}

	var updated int
	for _, mb := range mailboxes {
		// Calculate actual usage from message sizes (excluding deleted)
		var actualUsage int64
		err := q.db.Model(&models.Message{}).
			Where("mailbox_id = ? AND deleted = ?", mb.ID, false).
			Select("COALESCE(SUM(size_bytes), 0)").
			Scan(&actualUsage).Error
		if err != nil {
			slog.Error("quota reconciler: failed to calculate usage",
				"mailbox_id", mb.ID, "error", err)
			continue
		}

		// Also add attachment sizes
		var attachmentUsage int64
		q.db.Model(&models.Attachment{}).
			Joins("JOIN messages ON messages.id = attachments.message_id").
			Where("messages.mailbox_id = ? AND messages.deleted = ?", mb.ID, false).
			Select("COALESCE(SUM(attachments.size_bytes), 0)").
			Scan(&attachmentUsage)

		totalUsage := actualUsage + attachmentUsage

		if totalUsage != mb.QuotaUsedBytes {
			slog.Info("quota reconciler: correcting drift",
				"mailbox_id", mb.ID,
				"address", mb.Address,
				"recorded", mb.QuotaUsedBytes,
				"actual", totalUsage,
				"diff", totalUsage-mb.QuotaUsedBytes,
			)
			q.db.Model(&mb).Update("quota_used_bytes", totalUsage)
			updated++
		}
	}

	slog.Info("quota reconciler: complete",
		"mailboxes_checked", len(mailboxes),
		"corrected", updated,
		"duration", time.Since(start),
	)
}
