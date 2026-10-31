package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// dispatchSendLimitNotices bridges committed inbox notices from all protocol
// processes to the API's existing SSE/APNs mechanisms. The scheduled sender's
// existing lifecycle polls this outbox; SMTP needs no APNs credentials of its
// own. A notification failure cannot lose the durable inbox notice or audit.
func (h *MessageHandler) dispatchSendLimitNotices(ctx context.Context) {
	if h.layeredLimits == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var notices []models.SendLimitNotice
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("notified_at IS NULL AND message_id <> 0").Order("start, owner_id").Limit(100).Find(&notices).Error; err != nil {
			return err
		}
		for _, n := range notices {
			// The inbox message was committed by the submission's transaction already.
			// Sending before the outbox mark commits gives at-least-once dispatch;
			// APNs already coalesces retries, and SSE clients key messages by their id.
			if h.broker != nil {
				h.broker.Publish(n.OwnerID, SSEEvent{Type: "new_message", Data: map[string]any{"message_id": n.MessageID, "folder": "INBOX", "subject": "REST Mail sending limit reached"}})
			}
			if h.newMail != nil {
				h.newMail.NewMail(n.OwnerID)
			}
			if err := tx.Model(&n).Update("notified_at", time.Now()).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		slog.Warn("send limits: could not dispatch owner notifications", "error", err)
	}
}
