package handlers

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/sendidempotency"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const sendIdempotencyDescription = "Optional Idempotency-Key (at most 255 bytes) is scoped to the authenticated account across send, send-draft and forward, and retained for 24 hours after a successful send. A retry of the same route, resource and parsed body returns the original status and result without sending again, even if the draft or original message no longer exists. Different bodies, routes or resources with the same live key return 409 idempotency_conflict. Concurrent matching requests wait for the first transaction to commit, then replay it; if the wait is cancelled or fails, the retry returns 503 deferred and queues nothing. Failed sends roll back and do not consume the key. Current sender and source-mailbox permissions are checked on every replay. Omitting the header preserves ordinary sending. "

func (h *MessageHandler) withSendIdempotency(r *http.Request, request any, send func(*MessageHandler) (endpoint.Status[any], error)) (endpoint.Status[any], error) {
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) > 1 || len(key) > 255 {
		return endpoint.Status[any]{}, endpoint.Invalid(map[string]string{"Idempotency-Key": "must be a single key of at most 255 bytes"})
	}
	if key == "" {
		return send(h)
	}
	claims := middleware.GetClaims(r)
	if claims == nil {
		return endpoint.Status[any]{}, errNotSignedIn
	}
	body, err := json.Marshal(request)
	if err != nil {
		return endpoint.Status[any]{}, endpoint.Refuse(http.StatusBadRequest, "bad_request", "Invalid send request")
	}
	keyHash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	requestHash := fmt.Sprintf("%x", sha256.Sum256(append([]byte(r.URL.Path+"\n"), body...)))
	lockHash := sha256.Sum256([]byte(fmt.Sprintf("restmail/send/%d/%s", claims.AccountID, keyHash)))
	var result endpoint.Status[any]
	effects := &draftSendEffects{handler: h}
	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(binary.BigEndian.Uint64(lockHash[:8]))).Error; err != nil {
			return err
		}
		var receipt models.SendIdempotency
		query := tx.Where("account_id = ? AND key_hash = ?", claims.AccountID, keyHash)
		err := query.Clauses(clause.Locking{Strength: "UPDATE"}).First(&receipt).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && receipt.ExpiresAt.After(time.Now()) {
			if receipt.RequestHash != requestHash {
				return endpoint.Refuse(http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different send request")
			}
			sender := *h
			sender.db = tx
			if err := sender.authorizeSendReplay(r, &receipt); err != nil {
				return err
			}
			result = endpoint.Status[any]{Code: receipt.Status, Value: json.RawMessage(receipt.Response)}
			return nil
		}
		if err == nil {
			if err := query.Delete(&models.SendIdempotency{}).Error; err != nil {
				return err
			}
		}
		receipt = models.SendIdempotency{AccountID: claims.AccountID, KeyHash: keyHash, RequestHash: requestHash}
		sender := *h
		sender.db, sender.sendReceipt = tx, &receipt
		if h.broker != nil {
			sender.broker = effects
		}
		if h.newMail != nil {
			sender.newMail = effects
		}
		if h.recorder != nil {
			sender.recorder = effects
		}
		result, err = send(&sender)
		if err != nil {
			return err
		}
		receipt.Response, err = json.Marshal(result.Value)
		if err != nil {
			return err
		}
		receipt.Status, receipt.ExpiresAt = result.Code, time.Now().Add(sendidempotency.Retention)
		if err := tx.Create(&receipt).Error; err != nil {
			return err
		}
		result.Value = json.RawMessage(receipt.Response)
		return nil
	})
	if err != nil {
		var refusal *endpoint.Error
		if !errors.As(err, &refusal) {
			slog.Error("idempotent send: transaction failed", "account_id", claims.AccountID, "error", err)
			err = endpoint.Refuse(http.StatusServiceUnavailable, "deferred", "Try again later")
		}
		return endpoint.Status[any]{}, err
	}
	for _, publish := range effects.pending {
		publish()
	}
	return result, nil
}

func (h *MessageHandler) authorizeSendReplay(r *http.Request, receipt *models.SendIdempotency) error {
	claims := middleware.GetClaims(r)
	sender, err := maySendFrom(h.db, claims, receipt.From, receipt.OnBehalf)
	if errors.Is(err, errNoAccess) {
		return errAccountNotFound
	}
	if refusedAccount(err) {
		return endpoint.Refuse(http.StatusForbidden, "forbidden", "You are not authorized to send from this address")
	}
	if err != nil {
		return endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to check the sender")
	}
	if receipt.SourceMailboxID != 0 {
		_, err := mayReach(h.db, claims, receipt.SourceMailboxID, receipt.SourceCapability)
		if errors.Is(err, errNoAccess) {
			return errMessageAccess
		}
		if errors.Is(err, errNotAllowed) {
			return errNotAllowed
		}
		if err != nil {
			return endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to check source mailbox access")
		}
	}
	middleware.SetAuditTarget(r, "mailbox", sender.Address)
	middleware.SetAuditTarget(r, "mailbox_id", strconv.FormatUint(uint64(sender.ID), 10))
	mode := "send_as"
	if receipt.OnBehalf {
		mode = "send_on_behalf"
	}
	middleware.SetAuditTarget(r, "send_mode", mode)
	return nil
}
