package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

const (
	// scheduledSendInterval is how often the sender looks for due messages.
	// A minute is close enough for a "send later" the user picked by hand.
	scheduledSendInterval = 30 * time.Second
	// scheduledSendBatch bounds one pass, so a backlog cannot hold the loop.
	scheduledSendBatch = 10
	// scheduledSendRetry is how long a transient refusal (rate limit, a
	// deferred pipeline, an unavailable dependency) waits before the next try.
	scheduledSendRetry = time.Minute
	// scheduledSendMaxAttempts stops retrying a transient failure and marks it
	// failed instead, so one message cannot sit pending forever.
	scheduledSendMaxAttempts = 10
	// scheduledSendStaleRead is how long a "sending" row may be left by a
	// crashed process before another pass may pick it up.
	scheduledSendStaleAfter = 10 * time.Minute
)

// ScheduledSender is the background loop that sends scheduled messages when
// their time comes.
type ScheduledSender struct {
	h    *MessageHandler
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// StartScheduledSender starts the loop. Call Shutdown on server shutdown.
func (h *MessageHandler) StartScheduledSender() *ScheduledSender {
	// A previous process may have died between claiming a row and finishing
	// it. Put those back rather than leaving them in "sending" forever.
	h.db.Model(&models.ScheduledSend{}).
		Where("status = ? AND updated_at < ?", "sending", time.Now().Add(-scheduledSendStaleAfter)).
		Update("status", "pending")

	s := &ScheduledSender{
		h:    h,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go s.run()
	return s
}

// Shutdown stops the loop and waits for the pass in flight.
func (s *ScheduledSender) Shutdown() {
	if s == nil {
		return
	}
	s.once.Do(func() { close(s.stop) })
	<-s.done
}

func (s *ScheduledSender) run() {
	defer close(s.done)
	ticker := time.NewTicker(scheduledSendInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.h.runScheduledSends(context.Background())
		}
	}
}

// runScheduledSends sends one batch of messages whose time has come. It is a
// method on the handler (rather than the loop) so tests can drive it directly.
func (h *MessageHandler) runScheduledSends(ctx context.Context) {
	var due []models.ScheduledSend
	if err := h.db.
		Where("status = ? AND send_at <= ?", "pending", time.Now()).
		Order("send_at").
		Limit(scheduledSendBatch).
		Find(&due).Error; err != nil {
		slog.Warn("scheduled send: could not read due messages", "error", err)
		return
	}
	for _, row := range due {
		claimed := h.db.Model(&models.ScheduledSend{}).
			Where("id = ? AND status = ?", row.ID, "pending").
			Update("status", "sending")
		if claimed.Error != nil || claimed.RowsAffected == 0 {
			continue
		}
		h.deliverScheduled(ctx, row)
	}
}

// deliverScheduled replays the stored request through SendMessage. Every
// check runs again at the due time: the sender may have lost access, the
// domain's policy may have changed, or the send rate limit may be spent.
func (h *MessageHandler) deliverScheduled(ctx context.Context, row models.ScheduledSend) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("scheduled send: panic while sending", "id", row.ID, "panic", recovered)
			h.finishScheduled(row.ID, "failed", 0, "the send failed unexpectedly")
		}
	}()
	var req sendMessageRequest
	if err := json.Unmarshal(row.Request, &req); err != nil {
		h.finishScheduled(row.ID, "failed", 0, "could not read the stored message: "+err.Error())
		return
	}
	req.ScheduledAt = nil

	body, err := json.Marshal(req)
	if err != nil {
		h.finishScheduled(row.ID, "failed", 0, "could not rebuild the stored message: "+err.Error())
		return
	}

	// Give the send its own budget, and the account the request was made
	// with. CredentialKind is deliberately empty: this is not an app password
	// session, so the account's own grants decide what it may send as.
	sendCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	sendCtx = context.WithValue(sendCtx, middleware.ClaimsKey, &auth.Claims{AccountID: row.AccountID})

	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", bytes.NewReader(body)).
		WithContext(sendCtx)
	rec := httptest.NewRecorder()
	h.SendMessage(rec, httpReq)

	if rec.Code >= http.StatusOK && rec.Code < http.StatusMultipleChoices {
		var envelope struct {
			Data models.Message `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
		h.finishScheduled(row.ID, "sent", envelope.Data.ID, "")
		return
	}

	reason := rec.Body.String()
	if rec.Code == http.StatusTooManyRequests || rec.Code >= http.StatusInternalServerError {
		if row.Attempts+1 < scheduledSendMaxAttempts {
			h.db.Model(&models.ScheduledSend{}).Where("id = ?", row.ID).Updates(map[string]any{
				"status":     "pending",
				"attempts":   row.Attempts + 1,
				"send_at":    time.Now().Add(scheduledSendRetry),
				"last_error": truncateForStore(reason),
			})
			return
		}
	}
	h.finishScheduled(row.ID, "failed", 0, reason)
}

func (h *MessageHandler) finishScheduled(id uint, status string, messageID uint, lastError string) {
	updates := map[string]any{
		"status":     status,
		"last_error": truncateForStore(lastError),
	}
	if messageID > 0 {
		updates["sent_message_id"] = messageID
	}
	if err := h.db.Model(&models.ScheduledSend{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		slog.Warn("scheduled send: could not record the result", "id", id, "error", err)
	}
}

// truncateForStore keeps a refusal's text useful without letting a large
// error body fill the column.
func truncateForStore(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// errNoScheduledSend is the answer for a scheduled message that is not waiting, or that the
// request may not send as: the same for both, as the list leaves the second out.
var errNoScheduledSend = endpoint.Refuse(http.StatusNotFound, "not_found", "No scheduled message with that ID is waiting")

// mayCallBack reports whether the request may see and cancel a scheduled message from
// address: what sending it takes, mail:send as that address (#743). A credential scoped
// elsewhere, an API key without mail:send or an app password of another mailbox, may not.
func (h *MessageHandler) mayCallBack(claims *auth.Claims, address string) (bool, error) {
	_, err := mayActAs(h.db, claims, address, models.CapMailSend)
	if errors.Is(err, errNoAccess) {
		return false, nil
	}
	return err == nil, err
}

// listScheduledSends returns the messages still waiting (or being sent) for this account,
// soonest first: those the request may send as.
func (h *MessageHandler) listScheduledSends(r *http.Request, _ struct{}) ([]ScheduledMessage, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return nil, errNotSignedIn
	}
	var rows []models.ScheduledSend
	if err := h.db.
		Where("account_id = ? AND status IN ?", claims.AccountID, []string{"pending", "sending"}).
		Order("send_at").
		Find(&rows).Error; err != nil {
		return nil, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to list scheduled messages")
	}
	allowed := map[string]bool{}
	out := make([]ScheduledMessage, 0, len(rows))
	for _, row := range rows {
		may, seen := allowed[row.From]
		if !seen {
			var err error
			if may, err = h.mayCallBack(claims, row.From); err != nil {
				return nil, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to list scheduled messages")
			}
			allowed[row.From] = may
		}
		if may {
			out = append(out, scheduledMessageOf(row))
		}
	}
	return out, nil
}

// cancelScheduledSend takes a waiting message out of the queue. One already being sent
// cannot be called back.
func (h *MessageHandler) cancelScheduledSend(r *http.Request, in scheduledPath) (endpoint.Empty, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return endpoint.Empty{}, errNotSignedIn
	}
	var row models.ScheduledSend
	err := h.db.Where("id = ? AND account_id = ? AND status = ?", in.ID, claims.AccountID, "pending").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return endpoint.Empty{}, errNoScheduledSend
	}
	if err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to cancel the scheduled message")
	}
	may, err := h.mayCallBack(claims, row.From)
	if err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to cancel the scheduled message")
	}
	if !may {
		return endpoint.Empty{}, errNoScheduledSend
	}
	result := h.db.Model(&models.ScheduledSend{}).
		Where("id = ? AND status = ?", row.ID, "pending").
		Update("status", "cancelled")
	if result.Error != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to cancel the scheduled message")
	}
	if result.RowsAffected == 0 {
		return endpoint.Empty{}, errNoScheduledSend // it started sending meanwhile
	}
	return endpoint.Empty{}, nil
}
