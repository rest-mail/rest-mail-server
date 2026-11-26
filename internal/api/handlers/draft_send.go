package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

type messageEventPublisher interface {
	Publish(context.Context, uint, SSEEvent)
}

// sendDraft holds the draft's row lock through the send and its removal. Queue
// workers and other send requests cannot observe a partially committed send.
func (h *MessageHandler) sendDraft(r *http.Request, in sendDraftRequest) (endpoint.Status[any], error) {
	if r.Header.Get("Idempotency-Key") != "" || len(r.Header.Values("Idempotency-Key")) > 1 {
		return h.withSendIdempotency(r, in, func(sender *MessageHandler) (endpoint.Status[any], error) {
			return sender.sendDraftInTransaction(r, in)
		})
	}
	var sent endpoint.Status[any]
	if middleware.GetClaims(r) == nil {
		return sent, errNotSignedIn
	}
	effects := &draftSendEffects{handler: h}
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		sender := *h
		sender.db = tx
		if h.broker != nil {
			sender.broker = effects
		}
		if h.newMail != nil {
			sender.newMail = effects
		}
		if h.recorder != nil {
			sender.recorder = effects
		}
		var err error
		sent, err = sender.sendDraftInTransaction(r, in)
		return err
	})
	if err != nil {
		var refusal *endpoint.Error
		if !errors.As(err, &refusal) {
			slog.ErrorContext(r.Context(), "send draft: transaction failed", "draft_id", in.ID, "error", err)
			err = endpoint.Refuse(http.StatusServiceUnavailable, "deferred", "Try again later")
		}
		return endpoint.Status[any]{}, err
	}
	for _, publish := range effects.pending {
		publish()
	}
	return sent, nil
}

// Notifications and successful traces describe durable rows. Buffer them on
// the request's handler copy, and release them only after the transaction commits.
type draftSendEffects struct {
	handler *MessageHandler
	pending []func()
}

func (e *draftSendEffects) Publish(ctx context.Context, mailboxID uint, event SSEEvent) {
	e.pending = append(e.pending, func() { e.handler.broker.Publish(ctx, mailboxID, event) })
}

func (e *draftSendEffects) NewMail(mailboxID uint) {
	e.pending = append(e.pending, func() { e.handler.newMail.NewMail(mailboxID) })
}

// Record receives a trace the sending handler copy has already stamped with
// its request's ID, so it hands it on without a context of its own.
func (e *draftSendEffects) Record(trace models.MessageTrace) {
	// A policy refusal remains useful audit evidence even when no send commits.
	if trace.Outcome == outcomeRejected || trace.Outcome == outcomeDeferred {
		e.handler.recordTrace(context.Background(), trace)
		return
	}
	e.pending = append(e.pending, func() { e.handler.recordTrace(context.Background(), trace) })
}

// draftSendRefusal keeps sendDraft's 422 a validation failure with details, as
// it was before drafts carried uploads. A Sent copy with uploads is charged to
// the sender's quota (commitForward), and that refusal names from, the field
// that chose the account, as updateDraft does when a move would overfill it.
func draftSendRefusal(err error) error {
	var refusal *endpoint.Error
	if errors.As(err, &refusal) && refusal.Status == http.StatusUnprocessableEntity && refusal.Code == "mailbox_full" {
		return endpoint.Invalid(map[string]string{"from": "The sender account has insufficient quota"})
	}
	return err
}
