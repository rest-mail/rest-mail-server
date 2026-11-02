package repositories

import (
	"errors"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// InboundQueue is the durable list of accepted messages waiting for their content checks and
// delivery (#1437). Many workers can claim from it at once, on any node.
type InboundQueue struct {
	db *gorm.DB
}

// NewInboundQueue is the queue in db.
func NewInboundQueue(db *gorm.DB) *InboundQueue {
	return &InboundQueue{db: db}
}

// Enqueue records a message, ready now. The gateway answers 250 only after this.
func (q *InboundQueue) Enqueue(message *models.InboundMessage, now time.Time) error {
	message.Status = models.InboundPending
	message.NextAttempt = now
	message.CreatedAt = now
	return q.db.Create(message).Error
}

// Claim takes the message that has waited longest, for one worker alone, or nil if none is ready.
// A message still "processing" after staleAfter belonged to a worker that died, so it is taken
// again. Processing has to finish well inside staleAfter, or a message runs twice.
func (q *InboundQueue) Claim(now time.Time, staleAfter time.Duration) (*models.InboundMessage, error) {
	var claimed *models.InboundMessage
	err := q.db.Transaction(func(tx *gorm.DB) error {
		var message models.InboundMessage
		result := tx.Raw(`SELECT * FROM inbound_messages
			WHERE (status = ? AND next_attempt <= ?)
			   OR (status = ? AND last_attempt < ?)
			ORDER BY next_attempt ASC, id ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED`,
			models.InboundPending, now, models.InboundProcessing, now.Add(-staleAfter)).Scan(&message)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		message.Status = models.InboundProcessing
		message.Attempts++
		message.LastAttempt = &now
		if err := tx.Model(&message).Updates(map[string]any{
			"status": message.Status, "attempts": message.Attempts, "last_attempt": now,
		}).Error; err != nil {
			return err
		}
		claimed = &message
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// Finish removes a message that has been dealt with. Its object is the caller's to remove.
func (q *InboundQueue) Finish(id uint) error {
	return q.one(q.db.Delete(&models.InboundMessage{}, id))
}

// Retry puts a message back to be tried again at next, saying why it did not finish.
func (q *InboundQueue) Retry(id uint, next time.Time, reason string) error {
	return q.one(q.db.Model(&models.InboundMessage{}).Where("id = ?", id).Updates(map[string]any{
		"status": models.InboundPending, "next_attempt": next, "last_error": reason,
	}))
}

// Fail stops trying a message, and keeps it for an operator with the reason.
func (q *InboundQueue) Fail(id uint, reason string) error {
	return q.one(q.db.Model(&models.InboundMessage{}).Where("id = ?", id).Updates(map[string]any{
		"status": models.InboundFailed, "last_error": reason,
	}))
}

// ErrInboundMessageNotFound says a queue row is gone, which is expected only if a stale claim was
// taken over and finished by another worker.
var ErrInboundMessageNotFound = errors.New("inbound message not found")

func (q *InboundQueue) one(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrInboundMessageNotFound
	}
	return nil
}
