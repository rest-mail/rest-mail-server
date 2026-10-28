package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type messageEventPublisher interface {
	Publish(uint, SSEEvent)
}

// sendDraft holds the draft's row lock through the send and its removal. Queue
// workers and other send requests cannot observe a partially committed send.
func (h *MessageHandler) sendDraft(r *http.Request, in sendDraftRequest) (endpoint.Status[any], error) {
	var sent endpoint.Status[any]
	if middleware.GetClaims(r) == nil {
		return sent, errNotSignedIn
	}
	effects := &draftSendEffects{handler: h}
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		sender := *h
		sender.db, sender.draftSend = tx, effects
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
			slog.Error("send draft: transaction failed", "draft_id", in.ID, "error", err)
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

func (e *draftSendEffects) Publish(mailboxID uint, event SSEEvent) {
	e.pending = append(e.pending, func() { e.handler.broker.Publish(mailboxID, event) })
}

func (e *draftSendEffects) NewMail(mailboxID uint) {
	e.pending = append(e.pending, func() { e.handler.newMail.NewMail(mailboxID) })
}

func (e *draftSendEffects) Record(trace models.MessageTrace) {
	// A policy refusal remains useful audit evidence even when no send commits.
	if trace.Outcome == outcomeRejected || trace.Outcome == outcomeDeferred {
		e.handler.recordTrace(trace)
		return
	}
	e.pending = append(e.pending, func() { e.handler.recordTrace(trace) })
}
