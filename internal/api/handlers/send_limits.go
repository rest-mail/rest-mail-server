package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/sendlimits"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type heldSendIDKey struct{}
type releaseSendKey struct{}

func (h *MessageHandler) SetLayeredSendLimits(s *sendlimits.Service) { h.layeredLimits = s }

// The held payload includes Bcc, calendar data and attachment octets, but no
// token or credential secret. Holding, the notice and the hit audit commit together.
func (h *MessageHandler) reserveLayeredSend(r *http.Request, req sendMessageRequest, parts []pipeline.Attachment, senderID uint, force string) (*ScheduledMessage, error) {
	claims := middleware.GetClaims(r)
	var held models.ScheduledSend
	entry := middleware.RequestEntry(r, "send_limits.hit", http.StatusAccepted)
	if entry.SessionID == "" {
		entry.SessionID = claims.SessionID
	}
	bypass, _ := r.Context().Value(releaseSendKey{}).(bool)
	hit, err := h.layeredLimits.Reserve(h.db.WithContext(r.Context()), sendlimits.Attempt{AccountID: claims.AccountID, SenderID: senderID, Recipients: int64(len(req.To) + len(req.Cc) + len(req.Bcc)), Entry: &entry, BypassAccounts: bypass, ForceHit: force,
		Hold: func(tx *gorm.DB, hit *sendlimits.Hit) error {
			body, err := json.Marshal(req)
			if err != nil {
				return err
			}
			retained := parts
			if parts != nil {
				retained = append(make([]pipeline.Attachment, 0, len(parts)), parts...)
				remaining := h.maxForwardBytes
				for i, p := range retained {
					data, err := h.forwardPartBytes(r.Context(), p, remaining)
					if err != nil {
						return err
					}
					remaining -= int64(len(data))
					retained[i].Content = base64.StdEncoding.EncodeToString(data)
					retained[i].Size = int64(len(data))
					retained[i].Storage = ""
					retained[i].Ref = ""
				}
			}
			rawParts, err := json.Marshal(retained)
			if err != nil {
				return err
			}
			to, _ := json.Marshal(req.To)
			held = models.ScheduledSend{AccountID: claims.AccountID, From: req.From, To: models.JSONB(to), Subject: req.Subject, SendAt: hit.RetryAt, Status: "held", Request: models.JSONB(body), ForwardParts: models.JSONB(rawParts), CredentialID: claims.CredentialID, SessionID: claims.SessionID, LastError: hit.Scope + ": " + hit.Dimension}
			if id, ok := r.Context().Value(heldSendIDKey{}).(uint); ok && id != 0 {
				// A due scheduled send or unsuccessful human release stays in its row.
				if err := tx.Model(&models.ScheduledSend{}).Where("id = ?", id).Updates(map[string]any{"status": "held", "last_error": held.LastError, "send_at": held.SendAt, "request": held.Request, "forward_parts": held.ForwardParts}).Error; err != nil {
					return err
				}
				return tx.First(&held, id).Error
			}
			return tx.Create(&held).Error
		}})
	if err != nil {
		return nil, endpoint.Refuse(http.StatusServiceUnavailable, "deferred", "Could not retain or account for the message; try again later")
	}
	if hit == nil {
		return nil, nil
	}
	if h.broker != nil {
		h.broker.Publish(hit.OwnerID, SSEEvent{Type: "send_limit_hit", Data: map[string]any{"held_id": held.ID, "scope": hit.Scope, "dimension": hit.Dimension}})
	}
	out := scheduledMessageOf(held)
	return &out, nil
}

func (h *MessageHandler) RegisterSendLimits(rs endpoint.Routes, checks SessionRouteChecks) {
	manage := handlerChecked(models.CapMailboxManage, "CookieAuth", "BearerAuth")
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/send-limits", OperationID: "getAccountSendLimits", Summary: "Read effective account send limits", Tags: []string{"Accounts"}, Security: manage, Errors: []int{http.StatusNotFound}}, h.getAccountSendLimits)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/accounts/{id}/send-limits", OperationID: "setAccountSendLimits", Summary: "Set account send limits with a two-factor session", Description: "All four values are required; zero is unlimited. Owned accounts may only be capped below their owner's live limits. A two-factor person session and mailbox:manage are required. Changes are audited with the actual actor and session. Domain and server ceilings still apply.", Tags: []string{"Accounts"}, Security: manage, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict}, Middleware: []endpoint.Middleware{checks.Audit("send_limits.update", middleware.AuditFailuresOnly())}}, h.setAccountSendLimits)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/held-messages", OperationID: "listHeldMessages", Summary: "List an account's retained send-limit holds", Tags: []string{"Messages"}, Security: handlerChecked(models.CapMailRead, "CookieAuth", "BearerAuth"), Errors: []int{http.StatusNotFound}}, h.listHeldMessages)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/scheduled/{id}/release", OperationID: "releaseHeldMessage", Summary: "Release held mail with a two-factor session", Description: "Requires a two-factor person session, mailbox:manage on the submitting account and mail:send on the sender. Releases one retained message, bypassing account budgets for this deliberate send while still charging them. Domain/server budgets, live sender permissions and outbound policies remain enforced. Returns the retained record: 200 after release, or 202 if budgets or policies defer and keep the same hold. Concurrent releases cannot send twice. The real releasing actor/session and original submitting account are audited.", Tags: []string{"Messages"}, Security: manage, SuccessStatuses: []int{http.StatusAccepted}, Responses: []oapifly.EndpointResponse{{Status: http.StatusOK, Description: "The retained message after release", Type: oapifly.TypeOf[endpoint.Data[ScheduledMessage]]()}, {Status: http.StatusAccepted, Description: "The same message remains held by domain/server budget or outbound policy", Type: oapifly.TypeOf[endpoint.Data[ScheduledMessage]]()}, {Status: http.StatusUnprocessableEntity, Description: "Binding validation or live sender policy refusal", Type: oapifly.TypeOf[ForwardRefusalResponse]()}}, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusServiceUnavailable}, Middleware: []endpoint.Middleware{checks.Audit("send_limits.release", middleware.AuditFailuresOnly())}}, h.releaseHeldSend)
}

type setSendLimitsRequest struct {
	ID   uint32                 `path:"id" minimum:"1" example:"1"`
	Body models.SendLimitValues `body:""`
}

func humanSendLimitSession(r *http.Request) (*auth.Claims, error) {
	c := middleware.GetClaims(r)
	if c == nil {
		return nil, errAuthenticationRequired
	}
	if !c.TwoFactor || c.CredentialID != 0 || c.CredentialKind != "" || c.SessionID == "" {
		return nil, endpoint.Refuse(http.StatusForbidden, "two_factor_required", "Sign in as a person with two-factor authentication")
	}
	return c, nil
}
func (h *MessageHandler) limitService() error {
	if h.layeredLimits == nil {
		return endpoint.Refuse(http.StatusServiceUnavailable, "unavailable", "Persistent send limits are not configured")
	}
	return nil
}
func (h *MessageHandler) getAccountSendLimits(r *http.Request, in accountPath) (models.SendLimitValues, error) {
	var zero models.SendLimitValues
	c := middleware.GetClaims(r)
	if c == nil {
		return zero, errAuthenticationRequired
	}
	if _, err := mayReach(h.db.WithContext(r.Context()), c, uint(in.ID), models.CapMailboxManage); err != nil {
		if errors.Is(err, errNoAccess) {
			return zero, errAccountNotFound
		}
		return zero, err
	}
	if err := h.limitService(); err != nil {
		return zero, err
	}
	return h.layeredLimits.EffectiveAccount(h.db.WithContext(r.Context()), uint(in.ID))
}
func (h *MessageHandler) setAccountSendLimits(r *http.Request, in setSendLimitsRequest) (models.SendLimitValues, error) {
	var zero models.SendLimitValues
	c, err := humanSendLimitSession(r)
	if err != nil {
		return zero, err
	}
	if err := h.limitService(); err != nil {
		return zero, err
	}
	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if _, err := mayReach(tx, c, uint(in.ID), models.CapMailboxManage); err != nil {
			if errors.Is(err, errNoAccess) {
				return errAccountNotFound
			}
			return err
		}
		if err := h.layeredLimits.SetAccount(tx, uint(in.ID), in.Body); err != nil {
			if errors.Is(err, sendlimits.ErrOwnerCeiling) {
				return endpoint.Refuse(http.StatusConflict, "owner_limit_exceeded", err.Error())
			}
			return err
		}
		e := middleware.RequestEntry(r, "send_limits.update", http.StatusOK)
		if e.SessionID == "" {
			e.SessionID = c.SessionID
		}
		e.Target["account_id"] = strconv.FormatUint(uint64(in.ID), 10)
		for name, value := range map[string]int64{"messages_per_hour": in.Body.MessagesPerHour, "messages_per_day": in.Body.MessagesPerDay, "recipients_per_hour": in.Body.RecipientsPerHour, "recipients_per_day": in.Body.RecipientsPerDay} {
			e.Target[name] = strconv.FormatInt(value, 10)
		}
		return audit.Append(tx, &e)
	})
	if err != nil {
		return zero, err
	}
	return in.Body, nil
}
func (h *MessageHandler) listHeldMessages(r *http.Request, in accountPath) ([]ScheduledMessage, error) {
	c := middleware.GetClaims(r)
	if c == nil {
		return nil, errAuthenticationRequired
	}
	db := h.db.WithContext(r.Context())
	if _, err := mayReach(db, c, uint(in.ID), models.CapMailRead); err != nil {
		if errors.Is(err, errNoAccess) {
			return nil, errAccountNotFound
		}
		return nil, err
	}
	var rows []models.ScheduledSend
	if err := db.Where("account_id = ? AND status = ?", in.ID, "held").Order("id").Limit(200).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ScheduledMessage, 0, len(rows))
	for _, row := range rows {
		out = append(out, scheduledMessageOf(row))
	}
	return out, nil
}
func (h *MessageHandler) releaseHeldSend(r *http.Request, in scheduledPath) (endpoint.Status[any], error) {
	var out endpoint.Status[any]
	c, err := humanSendLimitSession(r)
	if err != nil {
		return out, err
	}
	if err := h.limitService(); err != nil {
		return out, err
	}
	effects := &draftSendEffects{handler: h}
	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var held models.ScheduledSend
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&held, in.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errNoScheduledSend
			}
			return err
		}
		if _, err := mayReach(tx, c, held.AccountID, models.CapMailboxManage); err != nil {
			if errors.Is(err, errNoAccess) {
				return errNoScheduledSend
			}
			return err
		}
		if held.Status != "held" {
			return endpoint.Refuse(http.StatusConflict, "not_held", "The message is not held")
		}
		var req sendMessageRequest
		if err := json.Unmarshal(held.Request, &req); err != nil {
			return err
		}
		req.ScheduledAt = nil
		var parts []pipeline.Attachment
		if len(held.ForwardParts) > 0 {
			if err := json.Unmarshal(held.ForwardParts, &parts); err != nil {
				return err
			}
		}
		sender := *h
		sender.db = tx
		sender.draftSend = effects
		if h.broker != nil {
			sender.broker = effects
		}
		if h.newMail != nil {
			sender.newMail = effects
		}
		if h.recorder != nil {
			sender.recorder = effects
		}
		ctx := context.WithValue(r.Context(), heldSendIDKey{}, held.ID)
		ctx = context.WithValue(ctx, releaseSendKey{}, true)
		out, err = sender.sendMessageWithParts(r.WithContext(ctx), req, parts)
		if err != nil {
			return err
		}
		if out.Code != http.StatusAccepted {
			updates := map[string]any{"status": "sent", "last_error": ""}
			if msg, ok := out.Value.(Message); ok {
				updates["sent_message_id"] = msg.ID
			}
			if out.Code == http.StatusOK {
				updates["status"] = "cancelled"
				updates["last_error"] = "Discarded by outbound policy"
			}
			if err := tx.Model(&held).Updates(updates).Error; err != nil {
				return err
			}
		}
		code := http.StatusOK
		if out.Code == http.StatusAccepted {
			code = http.StatusAccepted
		}
		if err := tx.First(&held, held.ID).Error; err != nil {
			return err
		}
		out = endpoint.Status[any]{Code: code, Value: scheduledMessageOf(held)}
		e := middleware.RequestEntry(r, "send_limits.release", out.Code)
		if e.SessionID == "" {
			e.SessionID = c.SessionID
		}
		e.Target["held_id"] = strconv.FormatUint(uint64(held.ID), 10)
		e.Target["submitted_by"] = strconv.FormatUint(uint64(held.AccountID), 10)
		e.Target["submitted_credential_id"] = strconv.FormatUint(uint64(held.CredentialID), 10)
		e.Target["account_budget_override"] = "true"
		return audit.Append(tx, &e)
	})
	if err != nil {
		return endpoint.Status[any]{}, err
	}
	for _, publish := range effects.pending {
		publish()
	}
	return out, nil
}
