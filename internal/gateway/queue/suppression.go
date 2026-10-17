package queue

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm/clause"
)

// DefaultSuppressionTTL is how long a hard-bounced address stays suppressed
// when the config sets nothing else (#141).
const DefaultSuppressionTTL = 90 * 24 * time.Hour

// SetSuppressionTTL sets how long an address stays suppressed after it hard
// bounces. A ttl <= 0 keeps the default.
func (w *Worker) SetSuppressionTTL(ttl time.Duration) {
	if ttl <= 0 {
		ttl = DefaultSuppressionTTL
	}
	w.suppressionTTL = ttl
}

// senderDomain is the lower-cased domain of an envelope sender, or "" for the
// null sender.
func senderDomain(sender string) string {
	at := strings.LastIndexByte(sender, '@')
	if at < 0 {
		return ""
	}
	return strings.ToLower(sender[at+1:])
}

// suppress records that the message's recipient does not exist, so its sending
// domain sends it nothing more until the suppression expires. A sender whose
// domain isn't hosted here has nothing to record against.
func (w *Worker) suppress(item models.OutboundQueue, category models.FailureCategory, lastError string, now time.Time) {
	domain := senderDomain(item.Sender)
	if domain == "" {
		return
	}
	var d struct{ ID uint }
	res := w.db.Raw("SELECT id FROM domains WHERE LOWER(name) = ? LIMIT 1", domain).Scan(&d)
	if res.Error != nil || res.RowsAffected == 0 {
		return
	}
	s := models.Suppression{
		DomainID:        d.ID,
		Address:         strings.ToLower(item.Recipient),
		Reason:          models.SuppressionHardBounce,
		FailureCategory: category,
		LastError:       lastError,
		MessageID:       item.MessageID,
		ExpiresAt:       now.Add(w.suppressionTTL),
	}
	// A second bounce, or one after the last suppression expired, starts the
	// window again.
	err := w.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "domain_id"}, {Name: "address"}},
		DoUpdates: clause.AssignmentColumns([]string{"reason", "failure_category", "last_error", "message_id", "updated_at", "expires_at"}),
	}).Create(&s).Error
	if err != nil {
		slog.Error("queue: failed to record suppression", "domain", domain, "recipient", item.Recipient, "error", err)
		return
	}
	slog.Info("queue: recipient suppressed", "domain", domain, "recipient", item.Recipient,
		"failure_category", category, "until", s.ExpiresAt)
}

// suppression returns the unexpired suppression of the message's recipient by
// its sending domain, if there is one.
func (w *Worker) suppression(item models.OutboundQueue, now time.Time) (models.Suppression, bool) {
	var s models.Suppression
	domain := senderDomain(item.Sender)
	if domain == "" {
		return s, false
	}
	res := w.db.Raw(`SELECT s.* FROM suppressions s JOIN domains d ON d.id = s.domain_id
		WHERE LOWER(d.name) = ? AND s.address = ? AND s.expires_at > ? LIMIT 1`,
		domain, strings.ToLower(item.Recipient), now).Scan(&s)
	if res.Error != nil {
		// Failing open: a lookup error must not stop mail; the send is tried.
		slog.Error("queue: suppression lookup failed", "id", item.ID, "error", res.Error)
		return s, false
	}
	return s, res.RowsAffected > 0
}

// bounceSuppressed bounces a message to a suppressed recipient without trying
// it, and tells the sender why and until when.
func (w *Worker) bounceSuppressed(item models.OutboundQueue, s models.Suppression) {
	// The claim counted an attempt; none was made.
	item.Attempts--
	item.FailureCategory = s.FailureCategory
	enhanced := "5.1.1"
	if s.FailureCategory == models.FailureBadDomain {
		enhanced = "5.1.2"
	}
	reason := fmt.Sprintf("not sent: an earlier message to this address bounced as undeliverable, "+
		"so mail from %s to it is suppressed until %s",
		senderDomain(item.Sender), s.ExpiresAt.UTC().Format("2 January 2006 15:04 MST"))
	w.db.Model(&item).Updates(map[string]interface{}{
		"status":           "bounced",
		"attempts":         item.Attempts,
		"last_error":       reason,
		"last_error_code":  0,
		"failure_category": s.FailureCategory,
	})
	slog.Warn("queue: recipient suppressed, bouncing without trying", "id", item.ID,
		"recipient", item.Recipient, "until", s.ExpiresAt)
	w.generateBounce(item, &SMTPError{Enhanced: enhanced, Message: reason})
}
