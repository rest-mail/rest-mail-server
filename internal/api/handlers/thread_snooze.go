package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm/clause"
)

const visibleThreads = `NOT EXISTS (SELECT 1 FROM thread_snoozes WHERE
	(thread_snoozes.mailbox_id, thread_snoozes.thread_id) = (messages.mailbox_id, messages.thread_id)
	AND snoozed_until IS NOT NULL)`

const messageThreadFields = messageDueFields + `, (SELECT snoozed_until FROM thread_snoozes WHERE
	(thread_snoozes.mailbox_id, thread_snoozes.thread_id) = (messages.mailbox_id, messages.thread_id)) AS snoozed_until`

type snoozeThreadRequest struct {
	ID       uint32 `path:"id" minimum:"1" example:"1"`
	ThreadID string `path:"threadID" example:"1"`
	Body     struct {
		SnoozedUntil *time.Time `json:"snoozed_until" description:"A future RFC 3339 date-time; null cancels the snooze"`
	} `body:""`
}

type ThreadSnooze struct {
	ThreadID     string     `json:"thread_id"`
	SnoozedUntil *time.Time `json:"snoozed_until"`
}

func (h *MessageHandler) snoozeThread(r *http.Request, in snoozeThreadRequest) (ThreadSnooze, error) {
	h = h.forRequest(r)
	mailboxID, err := h.accountMailbox(r, in.ID, models.CapMailWrite)
	if err != nil {
		return ThreadSnooze{}, err
	}
	until := in.Body.SnoozedUntil
	if until != nil && !until.After(time.Now()) {
		return ThreadSnooze{}, endpoint.Invalid(map[string]string{"snoozed_until": "must be in the future"})
	}
	var count int64
	if err := h.db.Model(&models.Message{}).Where("mailbox_id = ? AND thread_id = ? AND is_deleted = false", mailboxID, in.ThreadID).Count(&count).Error; err != nil {
		return ThreadSnooze{}, fmt.Errorf("find snoozed thread: %w", err)
	}
	if count == 0 {
		return ThreadSnooze{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Thread not found")
	}
	row := models.ThreadSnooze{MailboxID: mailboxID, ThreadID: in.ThreadID, SnoozedUntil: until}
	if err := h.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "mailbox_id"}, {Name: "thread_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"snoozed_until"}),
	}).Create(&row).Error; err != nil {
		return ThreadSnooze{}, fmt.Errorf("store thread snooze: %w", err)
	}
	h.publishThreadSnooze(row)
	return ThreadSnooze{ThreadID: row.ThreadID, SnoozedUntil: row.SnoozedUntil}, nil
}

func (h *MessageHandler) returnSnoozedThreads(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var returned []models.ThreadSnooze
	if err := h.db.WithContext(ctx).Model(&returned).Clauses(clause.Returning{}).
		Where("snoozed_until <= ?", time.Now()).Update("snoozed_until", nil).Error; err != nil {
		slog.Warn("snooze: could not return due threads", "error", err)
		return
	}
	for _, row := range returned {
		h.publishThreadSnooze(row)
	}
}

func (h *MessageHandler) publishThreadSnooze(row models.ThreadSnooze) {
	if h.broker != nil {
		h.broker.Publish(row.MailboxID, SSEEvent{Type: "folder_update", Data: map[string]interface{}{
			"mailbox_id": row.MailboxID, "thread_id": row.ThreadID, "snoozed_until": row.SnoozedUntil,
		}})
	}
}
