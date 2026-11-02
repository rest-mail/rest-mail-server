package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm/clause"
)

const messageDueFields = `messages.*, (SELECT due FROM thread_dues WHERE
	(thread_dues.mailbox_id, thread_dues.thread_id) = (messages.mailbox_id, messages.thread_id)) AS due,
	(SELECT due_source FROM thread_dues WHERE
	(thread_dues.mailbox_id, thread_dues.thread_id) = (messages.mailbox_id, messages.thread_id)) AS due_source`

type setThreadDueRequest struct {
	ID       uint32 `path:"id" minimum:"1" example:"1"`
	ThreadID string `path:"threadID" example:"1"`
	Body     struct {
		Due       *string `json:"due" format:"date-time" description:"An RFC 3339 date-time; null clears the date and its source"`
		DueSource string  `json:"due_source" enums:"user" description:"Only user-set dates can be written through this route"`
	} `body:""`
}

type ThreadDue struct {
	ThreadID  string     `json:"thread_id"`
	Due       *time.Time `json:"due"`
	DueSource *string    `json:"due_source" enums:"user,rule,parsed"`
}

func (h *MessageHandler) setThreadDue(r *http.Request, in setThreadDueRequest) (ThreadDue, error) {
	h = h.forRequest(r)
	mailboxID, err := h.accountMailbox(r, in.ID, models.CapMailWrite)
	if err != nil {
		return ThreadDue{}, err
	}
	row := models.ThreadDue{MailboxID: mailboxID, ThreadID: in.ThreadID}
	if in.Body.Due != nil {
		due, err := time.Parse(time.RFC3339, *in.Body.Due)
		if err != nil {
			return ThreadDue{}, endpoint.Invalid(map[string]string{"due": "must be an RFC 3339 date-time"})
		}
		row.Due = &due
		row.DueSource = &in.Body.DueSource
	}
	var count int64
	if err := h.db.Model(&models.Message{}).Where("mailbox_id = ? AND thread_id = ? AND is_deleted = false", mailboxID, in.ThreadID).Count(&count).Error; err != nil {
		return ThreadDue{}, fmt.Errorf("find due thread: %w", err)
	}
	if count == 0 {
		return ThreadDue{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Thread not found")
	}
	if err := h.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "mailbox_id"}, {Name: "thread_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"due", "due_source"}),
	}).Create(&row).Error; err != nil {
		return ThreadDue{}, fmt.Errorf("store thread due: %w", err)
	}
	return ThreadDue{ThreadID: row.ThreadID, Due: row.Due, DueSource: row.DueSource}, nil
}
