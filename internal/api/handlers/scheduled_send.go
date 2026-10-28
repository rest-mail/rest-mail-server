package handlers

import (
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
	"github.com/restmail/restmail/internal/db/repositories"
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
	s.h.dispatchSendLimitNotices(context.Background())
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.h.runScheduledSends(context.Background())
			s.h.dispatchSendLimitNotices(context.Background())
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

// deliverScheduled replays the stored request through sendMessage. Every
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

	// Give the send its own request budget and restore its submitting identity.
	// A credential is checked live again, with its current scope; sessions use
	// the account's current grants. Neither replay is a human-2FA session.
	sendCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	claims := &auth.Claims{AccountID: row.AccountID, SessionID: row.SessionID}
	if row.CredentialID != 0 {
		var cred models.Credential
		if err := h.db.WithContext(sendCtx).First(&cred, row.CredentialID).Error; err != nil || cred.AccountID != row.AccountID || !cred.Usable(time.Now()) {
			h.finishScheduled(row.ID, "failed", 0, "The submitting credential is no longer usable")
			return
		}
		claims.CredentialID, claims.CredentialKind = cred.ID, cred.Kind
		if cred.Kind == models.CredentialKindAPIKey {
			claims = repositories.APIKeyClaims(&cred, &models.Mailbox{ID: row.AccountID})
		}
	}
	sendCtx = context.WithValue(sendCtx, middleware.ClaimsKey, claims)
	sendCtx = context.WithValue(sendCtx, heldSendIDKey{}, row.ID)

	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", nil).WithContext(sendCtx)
	outcome, err := h.sendMessage(httpReq, req)
	if err == nil {
		if retained, ok := outcome.Value.(ScheduledMessage); ok && retained.Status == "held" {
			return // reserveLayeredSend retained this very row; never mark it sent.
		}
		var messageID uint
		if sent, ok := outcome.Value.(Message); ok {
			messageID = sent.ID
		}
		h.finishScheduled(row.ID, "sent", messageID, "")
		return
	}

	// The refusal is kept as the API writes it, as it was when the send ran
	// through the route.
	rec := httptest.NewRecorder()
	endpoint.WriteError(rec, err)
	reason := rec.Body.String()
	if rec.Code == http.StatusTooManyRequests || rec.Code >= http.StatusInternalServerError {
		if row.Attempts+1 < scheduledSendMaxAttempts {
			err := h.db.Model(&models.ScheduledSend{}).Where("id = ?", row.ID).Updates(map[string]any{
				"status":     "pending",
				"attempts":   row.Attempts + 1,
				"send_at":    time.Now().Add(scheduledSendRetry),
				"last_error": truncateForStore(reason),
			}).Error
			if err == nil {
				return
			}
			// Only pending rows are picked up, so a row left as sending would
			// never be sent, failed or cancelled: fail it, so the account sees
			// the send did not go.
			slog.Warn("scheduled send: could not record the retry", "id", row.ID, "error", err)
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
// address using its stored sending mode. The account's live capability and the
// credential's scope must both permit that mode on the represented mailbox.
func (h *MessageHandler) mayCallBack(ctx context.Context, claims *auth.Claims, address string, onBehalf ...bool) (bool, error) {
	behalf := len(onBehalf) > 0 && onBehalf[0]
	_, err := maySendFrom(h.db.WithContext(ctx), claims, address, behalf)
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
	if err := h.db.WithContext(r.Context()).
		Where("account_id = ? AND status IN ?", claims.AccountID, []string{"pending", "sending", "held"}).
		Order("send_at").
		Find(&rows).Error; err != nil {
		return nil, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to list scheduled messages")
	}
	allowed := map[string]bool{}
	out := make([]ScheduledMessage, 0, len(rows))
	for _, row := range rows {
		var request sendMessageRequest
		_ = json.Unmarshal(row.Request, &request)
		key := row.From
		if request.OnBehalf {
			key += ":on_behalf"
		}
		may, seen := allowed[key]
		if !seen {
			var err error
			if may, err = h.mayCallBack(r.Context(), claims, row.From, request.OnBehalf); err != nil {
				return nil, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to list scheduled messages")
			}
			allowed[key] = may
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
	err := h.db.WithContext(r.Context()).Where("id = ? AND account_id = ? AND status IN ?", in.ID, claims.AccountID, []string{"pending", "held"}).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return endpoint.Empty{}, errNoScheduledSend
	}
	if err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to cancel the scheduled message")
	}
	var request sendMessageRequest
	_ = json.Unmarshal(row.Request, &request)
	may, err := h.mayCallBack(r.Context(), claims, row.From, request.OnBehalf)
	if err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to cancel the scheduled message")
	}
	if !may {
		return endpoint.Empty{}, errNoScheduledSend
	}
	result := h.db.WithContext(r.Context()).Model(&models.ScheduledSend{}).
		Where("id = ? AND status IN ?", row.ID, []string{"pending", "held"}).
		Update("status", "cancelled")
	if result.Error != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to cancel the scheduled message")
	}
	if result.RowsAffected == 0 {
		return endpoint.Empty{}, errNoScheduledSend // it started sending meanwhile
	}
	return endpoint.Empty{}, nil
}
