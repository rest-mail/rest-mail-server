package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
	"github.com/restmail/restmail/internal/metrics"
	rmime "github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/outbound"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

type MessageHandler struct {
	db        *gorm.DB
	broker    *SSEBroker
	engine    *pipeline.Engine
	masterKey string
	recorder  traceRecorder
}

func NewMessageHandler(db *gorm.DB, broker *SSEBroker, engine *pipeline.Engine, masterKey string, recorder traceRecorder) *MessageHandler {
	return &MessageHandler{db: db, broker: broker, engine: engine, masterKey: masterKey, recorder: recorder}
}

// recordTrace hands a MessageTrace to the async recorder when one is configured.
// It never blocks or errors — trace persistence must not delay or fail message
// processing (a nil recorder simply skips capture).
func (h *MessageHandler) recordTrace(t models.MessageTrace) {
	if h.recorder != nil {
		h.recorder.Record(t)
	}
}

// signOutboundDKIM prepends a DKIM-Signature computed over the ACTUAL raw bytes
// with the sender domain's key, returning the signed message. DKIM must be
// signed over what is transmitted — signing a reconstructed EmailJSON produced
// signatures whose header hash never matched the wire form, so every outbound
// message failed verification at receivers. If the domain has no key configured
// (or signing fails), the message is returned unchanged. If the domain HAS a
// key configured but it cannot be loaded (OSI-8: an encrypted-at-rest key that
// fails to decrypt), it returns an error so the caller fails closed (temp-fail)
// rather than sending the message unsigned.
//
// The signing itself lives in internal/outbound.SignDKIM, shared with the queue
// worker so submitted mail is signed by the same code (#171).
func (h *MessageHandler) signOutboundDKIM(senderDomain, raw string) (string, error) {
	return outbound.SignDKIM(h.db, h.masterKey, senderDomain, raw)
}

// ListMessages returns messages in a folder with cursor-based pagination.
func (h *MessageHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	folder := chi.URLParam(r, "folder")
	if folder == "" {
		folder = "INBOX"
	}

	mailboxID, err := h.resolveAccountMailbox(uint(accountID), claims.WebmailAccountID)
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	query := h.db.Where("mailbox_id = ? AND folder = ? AND is_deleted = ?", mailboxID, folder, false).
		Order("received_at DESC").
		Limit(limit + 1)

	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		decoded, err := base64.StdEncoding.DecodeString(cursor)
		if err == nil {
			var cursorData struct {
				ID uint `json:"id"`
			}
			if json.Unmarshal(decoded, &cursorData) == nil {
				query = query.Where("id < ?", cursorData.ID)
			}
		}
	}

	var messages []models.Message
	if err := query.Find(&messages).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list messages")
		return
	}

	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}

	var nextCursor string
	if hasMore && len(messages) > 0 {
		last := messages[len(messages)-1]
		cursorJSON, _ := json.Marshal(map[string]uint{"id": last.ID})
		nextCursor = base64.StdEncoding.EncodeToString(cursorJSON)
	}

	var total int64
	h.db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ? AND is_deleted = ?", mailboxID, folder, false).Count(&total)

	respond.List(w, messages, &respond.Pagination{
		Cursor:  nextCursor,
		HasMore: hasMore,
		Total:   total,
	})
}

// GetMessage returns a single message by ID.
func (h *MessageHandler) GetMessage(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var msg models.Message
	if err := h.db.First(&msg, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "message_not_found", "Message not found")
		return
	}

	if !h.verifyMessageOwnership(w, r, &msg) {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	respond.Data(w, http.StatusOK, msg)
}

// UpdateMessage updates message flags or moves to a folder.
func (h *MessageHandler) UpdateMessage(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var msg models.Message
	if err := h.db.First(&msg, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "message_not_found", "Message not found")
		return
	}

	if !h.verifyMessageOwnership(w, r, &msg) {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	var req struct {
		IsRead    *bool   `json:"is_read"`
		IsFlagged *bool   `json:"is_flagged"`
		IsStarred *bool   `json:"is_starred"`
		Folder    *string `json:"folder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	updates := map[string]interface{}{}
	if req.IsRead != nil {
		updates["is_read"] = *req.IsRead
	}
	if req.IsFlagged != nil {
		updates["is_flagged"] = *req.IsFlagged
	}
	if req.IsStarred != nil {
		updates["is_starred"] = *req.IsStarred
	}
	if req.Folder != nil {
		updates["folder"] = *req.Folder
	}

	if len(updates) > 0 {
		h.db.Model(&msg).Updates(updates)
	}

	h.db.First(&msg, id)

	if h.broker != nil {
		h.broker.Publish(msg.MailboxID, SSEEvent{
			Type: "message_updated",
			Data: map[string]interface{}{
				"message_id": msg.ID,
				"folder":     msg.Folder,
				"is_read":    msg.IsRead,
				"is_flagged": msg.IsFlagged,
				"is_starred": msg.IsStarred,
			},
		})

		// Emit folder_update if the message was moved
		if req.Folder != nil {
			h.broker.Publish(msg.MailboxID, SSEEvent{
				Type: "folder_update",
				Data: map[string]interface{}{
					"folder": msg.Folder,
				},
			})
		}
	}

	respond.Data(w, http.StatusOK, msg)
}

// DeleteMessage soft-deletes a message.
func (h *MessageHandler) DeleteMessage(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var msg models.Message
	if err := h.db.First(&msg, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "message_not_found", "Message not found")
		return
	}

	if !h.verifyMessageOwnership(w, r, &msg) {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	// A permanent delete removes the message for good and reclaims its storage.
	// Gateway EXPUNGE (IMAP) and QUIT-DELE (POP3) request this via ?permanent=true
	// — those protocols have no "trash" stage, so a soft delete would strand the
	// row (unreachable, quota never reclaimed). A message already in Trash, or
	// already flagged deleted, is likewise purged. Everything else (a webmail
	// first delete) moves to Trash as a real, visible item.
	permanent := r.URL.Query().Get("permanent") == "true"
	hardDelete := permanent || msg.Folder == "Trash" || msg.IsDeleted

	if hardDelete {
		// Only reclaim for a message that still counted toward quota. An already
		// soft-deleted row (is_deleted=true) was already excluded from usage, so
		// decrementing again would under-count. Match the quota reconciler's
		// formula: message size + its attachment sizes.
		var reclaimBytes int64
		if !msg.IsDeleted {
			reclaimBytes = int64(msg.SizeBytes)
			if msg.HasAttachments {
				var attBytes int64
				h.db.Model(&models.Attachment{}).Where("message_id = ?", msg.ID).
					Select("COALESCE(SUM(size_bytes), 0)").Scan(&attBytes)
				reclaimBytes += attBytes
			}
		}

		// Purge the row, its attachment rows, and the quota decrement together so
		// a failure can never leave quota and rows inconsistent.
		if err := h.db.Transaction(func(tx *gorm.DB) error {
			if msg.HasAttachments {
				if err := tx.Where("message_id = ?", msg.ID).Delete(&models.Attachment{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Delete(&models.Message{}, msg.ID).Error; err != nil {
				return err
			}
			if reclaimBytes > 0 {
				if err := tx.Model(&models.Mailbox{}).Where("id = ?", msg.MailboxID).
					Update("quota_used_bytes", gorm.Expr("GREATEST(quota_used_bytes - ?, 0)", reclaimBytes)).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			respond.Error(w, http.StatusInternalServerError, "delete_failed", "Failed to delete message")
			return
		}
	} else {
		// Move to Trash as a real, visible item. is_deleted stays false so it
		// still appears in the Trash listing and continues to count toward quota
		// (it is still stored); it is reclaimed only when purged from Trash.
		h.db.Model(&msg).Update("folder", "Trash")
	}

	if h.broker != nil {
		h.broker.Publish(msg.MailboxID, SSEEvent{
			Type: "message_deleted",
			Data: map[string]interface{}{
				"message_id": msg.ID,
			},
		})
	}

	w.WriteHeader(http.StatusNoContent)
}

// SendMessage composes and sends a message from an authenticated user.
func (h *MessageHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req struct {
		From          string                  `json:"from"`
		To            []string                `json:"to"`
		Cc            []string                `json:"cc"`
		Bcc           []string                `json:"bcc"`
		Subject       string                  `json:"subject"`
		BodyText      string                  `json:"body_text"`
		BodyHTML      string                  `json:"body_html"`
		InReplyTo     string                  `json:"in_reply_to"`
		CalendarEvent *pipeline.CalendarEvent `json:"calendar_event,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.From == "" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "from is required")
		return
	}
	if len(req.To) == 0 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "to is required and must not be empty")
		return
	}

	// Validate every address before it is serialized into the DKIM-signed
	// To:/Cc: headers or queued as an SMTP envelope recipient. Without this a
	// recipient like "victim@real.com>\r\nRCPT TO:<attacker@evil.com" is written
	// raw into headers and passed to the queue worker's client.Rcpt, which
	// net/smtp does not sanitize — enabling CRLF header forgery and SMTP command
	// injection (#166).
	for _, addr := range req.To {
		if err := rmail.ValidateAddress(addr); err != nil {
			respond.Error(w, http.StatusBadRequest, "bad_request", "invalid recipient address in to")
			return
		}
	}
	for _, addr := range req.Cc {
		if err := rmail.ValidateAddress(addr); err != nil {
			respond.Error(w, http.StatusBadRequest, "bad_request", "invalid recipient address in cc")
			return
		}
	}
	for _, addr := range req.Bcc {
		if err := rmail.ValidateAddress(addr); err != nil {
			respond.Error(w, http.StatusBadRequest, "bad_request", "invalid recipient address in bcc")
			return
		}
	}
	if err := rmail.ValidateAddress(req.From); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "invalid from address")
		return
	}

	// Verify sender belongs to authenticated user
	var senderMailbox models.Mailbox
	authorized := false

	// Check primary mailbox
	var webmailAccount models.WebmailAccount
	if err := h.db.Preload("PrimaryMailbox").First(&webmailAccount, claims.WebmailAccountID).Error; err == nil {
		if webmailAccount.PrimaryMailbox.Address == req.From {
			senderMailbox = webmailAccount.PrimaryMailbox
			authorized = true
		}
	}

	// Check linked accounts
	if !authorized {
		var linked models.LinkedAccount
		if err := h.db.Joins("Mailbox").Where("linked_accounts.webmail_account_id = ? AND \"Mailbox\".address = ?", claims.WebmailAccountID, req.From).First(&linked).Error; err == nil {
			senderMailbox = linked.Mailbox
			authorized = true
		}
	}

	if !authorized {
		respond.Error(w, http.StatusForbidden, "forbidden", "You are not authorized to send from this address")
		return
	}

	// ── Outbound pipeline execution ────────────────────────────────
	var extraHeaders map[string]string // populated by pipeline transforms (e.g. DKIM-Signature)
	// queuedTrace, set in the outbound continue branch, defers recording the
	// happy-path trace until the sent Message row exists (message_id non-nil);
	// the outbound continue outcome is "queued" (handed to the delivery queue).
	var queuedTrace *traceInputs
	if h.engine != nil {
		var outToAddrs []pipeline.Address
		for _, addr := range req.To {
			outToAddrs = append(outToAddrs, pipeline.Address{Address: addr})
		}
		var outCcAddrs []pipeline.Address
		for _, addr := range req.Cc {
			outCcAddrs = append(outCcAddrs, pipeline.Address{Address: addr})
		}
		allRecipients := make([]string, 0, len(req.To)+len(req.Cc)+len(req.Bcc))
		allRecipients = append(allRecipients, req.To...)
		allRecipients = append(allRecipients, req.Cc...)
		allRecipients = append(allRecipients, req.Bcc...)

		outEmailJSON := &pipeline.EmailJSON{
			Envelope: pipeline.Envelope{
				MailFrom:  req.From,
				RcptTo:    allRecipients,
				Direction: "outbound",
			},
			Headers: pipeline.Headers{
				From:    []pipeline.Address{{Name: senderMailbox.DisplayName, Address: req.From}},
				To:      outToAddrs,
				Cc:      outCcAddrs,
				Subject: req.Subject,
			},
			Body: pipeline.Body{
				ContentType: "text/plain",
				Content:     req.BodyText,
			},
		}
		if req.BodyText != "" && req.BodyHTML != "" {
			outEmailJSON.Body = pipeline.Body{
				ContentType: "multipart/alternative",
				Parts: []pipeline.Body{
					{ContentType: "text/plain; charset=utf-8", Content: req.BodyText},
					{ContentType: "text/html; charset=utf-8", Content: req.BodyHTML},
				},
			}
		} else if req.BodyHTML != "" {
			outEmailJSON.Body = pipeline.Body{
				ContentType: "text/html",
				Content:     req.BodyHTML,
			}
		}

		var outPipelineCfg *pipeline.PipelineConfig
		var dbOutPipeline models.Pipeline
		if err := h.db.Where("domain_id = ? AND direction = ? AND active = ?", senderMailbox.DomainID, "outbound", true).
			First(&dbOutPipeline).Error; err == nil {
			var filterConfigs []pipeline.FilterConfig
			if jsonErr := json.Unmarshal(dbOutPipeline.Filters, &filterConfigs); jsonErr == nil {
				outPipelineCfg = &pipeline.PipelineConfig{
					ID:        dbOutPipeline.ID,
					DomainID:  dbOutPipeline.DomainID,
					Direction: dbOutPipeline.Direction,
					Filters:   filterConfigs,
					Active:    dbOutPipeline.Active,
				}
			}
		}
		if outPipelineCfg == nil {
			outPipelineCfg = pipeline.DefaultOutboundPipeline(senderMailbox.DomainID)
		}

		outPipelineCtx := pipeline.WithDB(r.Context(), h.db)
		outResult, outErr := h.engine.Execute(outPipelineCtx, outPipelineCfg, outEmailJSON)
		if outErr != nil {
			respond.Error(w, http.StatusInternalServerError, "pipeline_error", "Outbound pipeline execution failed")
			return
		}

		// Base trace inputs shared by every outbound terminal branch. transport is
		// "" (local submission, not an inbound-MX arrival); the RFC Message-ID is
		// generated only on the continue path (below), so non-continue outbound
		// traces correlate via sender/recipient.
		outTraceBase := traceInputs{
			PipelineID: outPipelineCfg.ID,
			Direction:  "outbound",
			Result:     outResult,
			Envelope:   outEmailJSON.Envelope,
		}

		switch outResult.FinalAction {
		case pipeline.ActionReject:
			rejectMsg := outResult.RejectMsg
			if rejectMsg == "" {
				rejectMsg = "Message rejected by outbound policy"
			}
			outTraceBase.Outcome = outcomeRejected
			h.recordTrace(buildTrace(outTraceBase))
			respond.Error(w, http.StatusForbidden, "rejected", rejectMsg)
			return
		case pipeline.ActionQuarantine:
			outTraceBase.Outcome = outcomeQuarantined
			h.recordTrace(buildTrace(outTraceBase))
			respond.Error(w, http.StatusForbidden, "quarantined", "Message held for review by outbound policy")
			return
		case pipeline.ActionDiscard:
			outTraceBase.Outcome = outcomeDiscarded
			h.recordTrace(buildTrace(outTraceBase))
			respond.Data(w, http.StatusOK, map[string]string{"status": "discarded"})
			return
		case pipeline.ActionDefer:
			outTraceBase.Outcome = outcomeDeferred
			h.recordTrace(buildTrace(outTraceBase))
			respond.Error(w, http.StatusServiceUnavailable, "deferred", "Try again later")
			return
		case pipeline.ActionContinue:
			// Stash the trace; recorded as "queued" after the sent Message row is
			// created so message_id is non-nil.
			base := outTraceBase
			queuedTrace = &base
			// Feed pipeline transforms back into req so downstream code
			// (sent message creation + raw RFC 2822 builder) uses the
			// pipeline output (e.g. header_cleanup, dkim_sign).
			if outResult.FinalEmail != nil {
				req.Subject = outResult.FinalEmail.Headers.Subject

				bodyText, bodyHTML := extractBodyParts(outResult.FinalEmail.Body)
				if bodyText != "" {
					req.BodyText = bodyText
				}
				if bodyHTML != "" {
					req.BodyHTML = bodyHTML
				}

				// Update recipients if the pipeline modified them.
				if len(outResult.FinalEmail.Envelope.RcptTo) > 0 {
					req.To = outResult.FinalEmail.Envelope.RcptTo
					req.Cc = nil
					req.Bcc = nil
				}

				// Capture extra headers (e.g. DKIM-Signature) for the raw message.
				extraHeaders = outResult.FinalEmail.Headers.Extra
			}
		}
	}

	// Generate ICS data if a calendar event is being composed
	var icsData []byte
	if req.CalendarEvent != nil {
		evt := req.CalendarEvent
		// Fill in defaults
		if evt.Organizer.Address == "" {
			evt.Organizer = pipeline.CalendarAddress{Name: senderMailbox.DisplayName, Address: req.From}
		}
		if evt.DTStamp.IsZero() {
			evt.DTStamp = time.Now()
		}
		if evt.UID == "" {
			evt.UID = rmail.GenerateMessageID(rmail.DomainFromAddress(req.From))
		}
		var icsErr error
		icsData, icsErr = rmime.GenerateICS(*evt)
		if icsErr != nil {
			respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid calendar event: "+icsErr.Error())
			return
		}
	}

	// Generate Message-ID
	messageID := rmail.GenerateMessageID(rmail.DomainFromAddress(req.From))

	// Marshal recipient lists
	toJSON, _ := json.Marshal(req.To)
	ccJSON, _ := json.Marshal(req.Cc)
	if req.Cc == nil {
		ccJSON = []byte("[]")
	}
	// BCC recipients are not stored in the message metadata; they are only used for delivery routing.
	_ = req.Bcc

	now := time.Now()
	sizeBytes := len(req.Subject) + len(req.BodyText) + len(req.BodyHTML)

	// Compute thread ID for sent message. Canonicalize so it matches — byte for
	// byte — the bracketed Message-IDs used everywhere else (thread grouping is
	// an equality match on thread_id).
	threadID := messageID
	if req.InReplyTo != "" {
		threadID = req.InReplyTo
	}
	threadID = rmail.CanonicalID(threadID)

	// Create message in sender's Sent folder
	sentMsg := models.Message{
		MailboxID:    senderMailbox.ID,
		Folder:       "Sent",
		MsgID:        messageID,
		InReplyTo:    req.InReplyTo,
		ThreadID:     threadID,
		Sender:       req.From,
		SenderName:   senderMailbox.DisplayName,
		RecipientsTo: models.JSONB(toJSON),
		RecipientsCc: models.JSONB(ccJSON),
		Subject:      req.Subject,
		BodyText:     req.BodyText,
		BodyHTML:     req.BodyHTML,
		IsRead:       true,
		SizeBytes:    sizeBytes,
		ReceivedAt:   now,
	}
	if req.CalendarEvent != nil {
		calJSON, _ := json.Marshal([]pipeline.CalendarEvent{*req.CalendarEvent})
		sentMsg.CalendarEventsRaw = models.JSONB(calJSON)
	}

	if err := h.db.Create(&sentMsg).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to save sent message")
		return
	}

	// ── Record the queued trace ──────────────────────────────────────
	// The outbound pipeline said continue and the sent Message row now exists —
	// record it as "queued" (handed to the outbound delivery queue) with
	// message_id non-nil and the generated RFC Message-ID.
	if queuedTrace != nil {
		queuedTrace.Outcome = outcomeQueued
		queuedTrace.RFCMessageID = sentMsg.MsgID
		mid := sentMsg.ID
		queuedTrace.MessageID = &mid
		h.recordTrace(buildTrace(*queuedTrace))
	}

	// Build raw RFC 2822 message for outbound queue entries.
	var rawMessage string
	{
		var b strings.Builder
		fromAddr := &mail.Address{Name: senderMailbox.DisplayName, Address: req.From}
		b.WriteString("From: " + fromAddr.String() + "\r\n")
		b.WriteString("To: " + strings.Join(req.To, ", ") + "\r\n")
		if len(req.Cc) > 0 {
			b.WriteString("Cc: " + strings.Join(req.Cc, ", ") + "\r\n")
		}
		b.WriteString("Subject: " + req.Subject + "\r\n")
		b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
		b.WriteString("Message-ID: " + messageID + "\r\n")
		if req.InReplyTo != "" {
			b.WriteString("In-Reply-To: <" + req.InReplyTo + ">\r\n")
		}
		b.WriteString("MIME-Version: 1.0\r\n")

		// Write extra headers added by pipeline transforms (e.g. header_cleanup).
		// DKIM-Signature/ARC-* are deliberately skipped here: the pipeline signs a
		// reconstructed EmailJSON, whose header bytes don't match this serialized
		// form, so its signature never verifies. DKIM is signed authoritatively
		// over the finalized raw below (signOutboundDKIM).
		for name, value := range extraHeaders {
			switch {
			case strings.EqualFold(name, "DKIM-Signature"),
				strings.HasPrefix(strings.ToLower(name), "arc-"):
				continue
			}
			b.WriteString(name + ": " + value + "\r\n")
		}

		if icsData != nil {
			// Calendar invite: multipart/mixed with text body + text/calendar attachment
			mixedBoundary := fmt.Sprintf("=_restmail_mixed_%d", now.UnixNano())
			b.WriteString("Content-Type: multipart/mixed; boundary=\"" + mixedBoundary + "\"\r\n")
			b.WriteString("\r\n")

			// Text body part
			b.WriteString("--" + mixedBoundary + "\r\n")
			if req.BodyText != "" && req.BodyHTML != "" {
				altBoundary := fmt.Sprintf("=_restmail_alt_%d", now.UnixNano())
				b.WriteString("Content-Type: multipart/alternative; boundary=\"" + altBoundary + "\"\r\n")
				b.WriteString("\r\n")
				b.WriteString("--" + altBoundary + "\r\n")
				b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
				b.WriteString(req.BodyText + "\r\n")
				b.WriteString("--" + altBoundary + "\r\n")
				b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
				b.WriteString(req.BodyHTML + "\r\n")
				b.WriteString("--" + altBoundary + "--\r\n")
			} else if req.BodyHTML != "" {
				b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
				b.WriteString(req.BodyHTML + "\r\n")
			} else {
				b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
				b.WriteString(req.BodyText + "\r\n")
			}

			// Calendar part
			method := strings.ToUpper(req.CalendarEvent.Method)
			if method == "" {
				method = "REQUEST"
			}
			b.WriteString("--" + mixedBoundary + "\r\n")
			b.WriteString("Content-Type: text/calendar; charset=utf-8; method=" + method + "\r\n")
			b.WriteString("Content-Disposition: attachment; filename=\"invite.ics\"\r\n")
			b.WriteString("Content-Transfer-Encoding: base64\r\n")
			b.WriteString("\r\n")
			encoded := base64.StdEncoding.EncodeToString(icsData)
			for i := 0; i < len(encoded); i += 76 {
				end := i + 76
				if end > len(encoded) {
					end = len(encoded)
				}
				b.WriteString(encoded[i:end] + "\r\n")
			}
			b.WriteString("--" + mixedBoundary + "--\r\n")
		} else if req.BodyText != "" && req.BodyHTML != "" {
			boundary := fmt.Sprintf("=_restmail_%d", now.UnixNano())
			b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n")
			b.WriteString("\r\n")
			b.WriteString("--" + boundary + "\r\n")
			b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
			b.WriteString(req.BodyText + "\r\n")
			b.WriteString("--" + boundary + "\r\n")
			b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
			b.WriteString(req.BodyHTML + "\r\n")
			b.WriteString("--" + boundary + "--\r\n")
		} else if req.BodyHTML != "" {
			b.WriteString("Content-Type: text/html; charset=utf-8\r\n")
			b.WriteString("\r\n")
			b.WriteString(req.BodyHTML + "\r\n")
		} else {
			b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
			b.WriteString("\r\n")
			b.WriteString(req.BodyText + "\r\n")
		}
		rawMessage = b.String()
	}

	// Sign the finalized outbound message over its actual transmitted bytes.
	// OSI-8: a key that should decrypt but cannot fails closed — temp-fail the
	// send (client retries) rather than deliver the message unsigned.
	signedRaw, signErr := h.signOutboundDKIM(rmail.DomainFromAddress(req.From), rawMessage)
	if signErr != nil {
		respond.Error(w, http.StatusServiceUnavailable, "deferred", "Try again later")
		return
	}
	rawMessage = signedRaw

	// Deliver to each recipient in to + cc + bcc
	allRecipients := make([]string, 0, len(req.To)+len(req.Cc)+len(req.Bcc))
	allRecipients = append(allRecipients, req.To...)
	allRecipients = append(allRecipients, req.Cc...)
	allRecipients = append(allRecipients, req.Bcc...)

	for _, rcpt := range allRecipients {
		var recipientMailbox models.Mailbox
		if err := h.db.Where("address = ? AND active = ?", rcpt, true).First(&recipientMailbox).Error; err == nil {
			// Local delivery with full inbound pipeline
			_, deliverErr := h.deliverToLocal(r.Context(), localDeliveryParams{
				Mailbox:      recipientMailbox,
				Sender:       req.From,
				SenderName:   senderMailbox.DisplayName,
				RecipientsTo: toJSON,
				RecipientsCc: ccJSON,
				Subject:      req.Subject,
				BodyText:     req.BodyText,
				BodyHTML:     req.BodyHTML,
				MessageID:    messageID,
				InReplyTo:    req.InReplyTo,
				RawMessage:   rawMessage,
			})
			if deliverErr != nil {
				slog.Warn("local delivery failed for recipient", "recipient", rcpt, "error", deliverErr)
			}
		} else {
			// Remote delivery - queue it with raw message and message reference
			recipientDomain := rcpt
			if idx := strings.LastIndex(rcpt, "@"); idx >= 0 {
				recipientDomain = rcpt[idx+1:]
			}
			msgID := sentMsg.ID
			queueEntry := models.OutboundQueue{
				MessageID:  &msgID,
				Sender:     req.From,
				Recipient:  rcpt,
				Domain:     recipientDomain,
				RawMessage: rawMessage,
				Status:     "pending",
			}
			h.db.Create(&queueEntry)
		}
	}

	// Publish SSE event for sent message
	if h.broker != nil {
		h.broker.Publish(senderMailbox.ID, SSEEvent{
			Type: "message_sent",
			Data: map[string]interface{}{
				"message_id": sentMsg.ID,
				"folder":     "Sent",
				"subject":    sentMsg.Subject,
			},
		})
	}

	// Auto-collect recipient contacts
	for _, rcptAddr := range allRecipients {
		contact := models.Contact{MailboxID: senderMailbox.ID, Email: rcptAddr}
		h.db.Where(contact).Attrs(models.Contact{
			Name:       "",
			TrustLevel: "auto",
			Source:     "sent",
		}).FirstOrCreate(&models.Contact{})
	}

	respond.Data(w, http.StatusCreated, sentMsg)
}

// DeliverMessage is the inbound delivery endpoint for the gateway.
func (h *MessageHandler) DeliverMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MailboxID    uint            `json:"mailbox_id"`
		Address      string          `json:"address"`
		Sender       string          `json:"sender"`
		SenderName   string          `json:"sender_name"`
		RecipientsTo json.RawMessage `json:"recipients_to"`
		RecipientsCc json.RawMessage `json:"recipients_cc"`
		Subject      string          `json:"subject"`
		BodyText     string          `json:"body_text"`
		BodyHTML     string          `json:"body_html"`
		MessageID    string          `json:"message_id"`
		InReplyTo    string          `json:"in_reply_to"`
		References   string          `json:"references"`
		// RawMessage is base64-decoded from the wire ([]byte, not string) so the
		// pristine RFC 2822 octets survive JSON transport unchanged — a JSON
		// string field would mangle every non-UTF-8 byte to U+FFFD (see #173).
		RawMessage []byte `json:"raw_message"`
		ClientIP   string `json:"client_ip"`
		HeloName   string `json:"helo_name"`
		// Inbound transport-security metrics (always-on, inbound-MX only). A nil
		// ReceivedTLS means the caller is not an inbound-MX delivery, persisted as
		// NULL. TLSCipher is accepted for wire completeness but only the version is
		// persisted on the message.
		ReceivedTLS *bool  `json:"received_tls"`
		TLSVersion  string `json:"tls_version"`
		TLSCipher   string `json:"tls_cipher"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	var mailbox models.Mailbox
	if req.MailboxID > 0 {
		if err := h.db.First(&mailbox, req.MailboxID).Error; err != nil {
			respond.Error(w, http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
			return
		}
	} else if req.Address != "" {
		if err := h.db.Where("address = ? AND active = ?", req.Address, true).First(&mailbox).Error; err != nil {
			respond.Error(w, http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
			return
		}
	} else {
		respond.Error(w, http.StatusBadRequest, "bad_request", "mailbox_id or address required")
		return
	}

	// Delegate to shared local delivery helper (pipeline + quota + attachments + SSE)
	msg, err := h.deliverToLocal(r.Context(), localDeliveryParams{
		Mailbox:      mailbox,
		Sender:       req.Sender,
		SenderName:   req.SenderName,
		RecipientsTo: req.RecipientsTo,
		RecipientsCc: req.RecipientsCc,
		Subject:      req.Subject,
		BodyText:     req.BodyText,
		BodyHTML:     req.BodyHTML,
		MessageID:    req.MessageID,
		InReplyTo:    req.InReplyTo,
		References:   req.References,
		RawMessage:   string(req.RawMessage),
		ClientIP:     req.ClientIP,
		HeloName:     req.HeloName,
		ReceivedTLS:  req.ReceivedTLS,
		TLSVersion:   req.TLSVersion,
	})
	if err != nil {
		errStr := err.Error()
		switch {
		case strings.HasPrefix(errStr, "rejected:"):
			respond.Error(w, 550, "rejected", strings.TrimPrefix(errStr, "rejected: "))
		case strings.HasPrefix(errStr, "deferred:"):
			respond.Error(w, 451, "deferred", "Try again later")
		case strings.HasPrefix(errStr, "mailbox_full:"):
			respond.Error(w, http.StatusUnprocessableEntity, "mailbox_full", "Recipient mailbox is over quota")
		default:
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to deliver message")
		}
		return
	}

	if msg == nil {
		// Quarantined or discarded
		respond.Data(w, http.StatusOK, map[string]string{"status": "processed"})
		return
	}

	respond.Data(w, http.StatusCreated, msg)
}

// ListFolders returns all folders for an account.
func (h *MessageHandler) ListFolders(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	mailboxID, err := h.resolveAccountMailbox(uint(accountID), claims.WebmailAccountID)
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	type folderInfo struct {
		Folder string `json:"name"`
		Total  int64  `json:"total"`
		Unread int64  `json:"unread"`
	}

	var folders []folderInfo
	h.db.Model(&models.Message{}).
		Select("folder, COUNT(CASE WHEN is_deleted = false THEN 1 END) as total, COUNT(CASE WHEN is_deleted = false AND is_read = false THEN 1 END) as unread").
		Where("mailbox_id = ?", mailboxID).
		Group("folder").
		Order("folder ASC").
		Scan(&folders)

	standardFolders := []string{"INBOX", "Sent", "Drafts", "Trash"}
	folderMap := make(map[string]bool)
	for _, f := range folders {
		folderMap[f.Folder] = true
	}
	for _, sf := range standardFolders {
		if !folderMap[sf] {
			folders = append(folders, folderInfo{Folder: sf, Total: 0, Unread: 0})
		}
	}

	respond.List(w, folders, nil)
}

// CreateFolder creates a new mailbox folder.
// POST /api/v1/accounts/{id}/folders
func (h *MessageHandler) CreateFolder(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	mailboxID, err := h.resolveAccountMailbox(uint(accountID), claims.WebmailAccountID)
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		respond.ValidationError(w, map[string]string{"name": "required"})
		return
	}

	// Check if folder already has messages (i.e. exists)
	var count int64
	h.db.Model(&models.Message{}).
		Where("mailbox_id = ? AND folder = ?", mailboxID, req.Name).
		Count(&count)
	if count > 0 {
		respond.Error(w, http.StatusConflict, "already_exists", "Folder already exists")
		return
	}

	// Create a placeholder so the folder shows up in ListFolders (store as a message-less folder marker)
	// We use a zero-content message with is_deleted=true as a folder marker
	marker := models.Message{
		MailboxID: mailboxID,
		Folder:    req.Name,
		Sender:    "system",
		Subject:   "",
		IsDeleted: true,
		SizeBytes: 0,
	}
	h.db.Create(&marker)

	respond.Data(w, http.StatusCreated, map[string]string{"name": req.Name})
}

// RenameFolder renames a mailbox folder.
// PATCH /api/v1/accounts/{id}/folders/{folder}
func (h *MessageHandler) RenameFolder(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	mailboxID, err := h.resolveAccountMailbox(uint(accountID), claims.WebmailAccountID)
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	oldName := chi.URLParam(r, "folder")
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		respond.ValidationError(w, map[string]string{"name": "required"})
		return
	}

	result := h.db.Model(&models.Message{}).
		Where("mailbox_id = ? AND folder = ?", mailboxID, oldName).
		Update("folder", req.Name)
	if result.RowsAffected == 0 {
		respond.Error(w, http.StatusNotFound, "not_found", "Folder not found or empty")
		return
	}

	respond.Data(w, http.StatusOK, map[string]string{"name": req.Name})
}

// DeleteFolder deletes a mailbox folder and all its messages.
// DELETE /api/v1/accounts/{id}/folders/{folder}
func (h *MessageHandler) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	mailboxID, err := h.resolveAccountMailbox(uint(accountID), claims.WebmailAccountID)
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	folderName := chi.URLParam(r, "folder")

	// Prevent deletion of standard folders
	standardFolders := []string{"INBOX", "Sent", "Drafts", "Trash", "Spam", "Archive"}
	for _, sf := range standardFolders {
		if folderName == sf {
			respond.Error(w, http.StatusBadRequest, "bad_request", "Cannot delete standard folder")
			return
		}
	}

	// Sum message sizes for quota adjustment
	var totalSize int64
	h.db.Model(&models.Message{}).
		Where("mailbox_id = ? AND folder = ?", mailboxID, folderName).
		Select("COALESCE(SUM(size_bytes), 0)").
		Scan(&totalSize)

	result := h.db.Where("mailbox_id = ? AND folder = ?", mailboxID, folderName).
		Delete(&models.Message{})
	if result.RowsAffected == 0 {
		respond.Error(w, http.StatusNotFound, "not_found", "Folder not found")
		return
	}

	// Update quota
	if totalSize > 0 {
		h.db.Model(&models.Mailbox{}).Where("id = ?", mailboxID).
			Update("quota_used_bytes", gorm.Expr("GREATEST(quota_used_bytes - ?, 0)", totalSize))
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetQuota returns quota usage for a mailbox.
// GET /api/v1/accounts/{id}/quota
func (h *MessageHandler) GetQuota(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	mailboxID, err := h.resolveAccountMailbox(uint(accountID), claims.WebmailAccountID)
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	var mailbox models.Mailbox
	if err := h.db.First(&mailbox, mailboxID).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Mailbox not found")
		return
	}

	var messageCount int64
	h.db.Model(&models.Message{}).Where("mailbox_id = ?", mailboxID).Count(&messageCount)

	respond.Data(w, http.StatusOK, map[string]interface{}{
		"quota_bytes":      mailbox.QuotaBytes,
		"quota_used_bytes": mailbox.QuotaUsedBytes,
		"message_count":    messageCount,
		"percent_used":     float64(mailbox.QuotaUsedBytes) / float64(mailbox.QuotaBytes) * 100,
	})
}

// resolveSenderMailbox verifies the given from address belongs to the
// authenticated user (via primary or linked mailbox) and returns the mailbox.
func (h *MessageHandler) resolveSenderMailbox(from string, webmailAccountID uint) (*models.Mailbox, error) {
	var account models.WebmailAccount
	if err := h.db.Preload("PrimaryMailbox").First(&account, webmailAccountID).Error; err == nil {
		if account.PrimaryMailbox.Address == from {
			return &account.PrimaryMailbox, nil
		}
	}
	var linked models.LinkedAccount
	if err := h.db.Joins("Mailbox").Where("linked_accounts.webmail_account_id = ? AND \"Mailbox\".address = ?", webmailAccountID, from).First(&linked).Error; err == nil {
		return &linked.Mailbox, nil
	}
	return nil, fmt.Errorf("sender not authorized")
}

// SaveDraft creates a new draft message.
// POST /api/v1/messages/draft
func (h *MessageHandler) SaveDraft(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req struct {
		From       string   `json:"from"`
		To         []string `json:"to"`
		Cc         []string `json:"cc"`
		Subject    string   `json:"subject"`
		BodyText   string   `json:"body_text"`
		BodyHTML   string   `json:"body_html"`
		InReplyTo  string   `json:"in_reply_to"`
		References string   `json:"references"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	var mailboxID uint
	if req.From != "" {
		mb, err := h.resolveSenderMailbox(req.From, claims.WebmailAccountID)
		if err != nil {
			respond.Error(w, http.StatusForbidden, "forbidden", "You are not authorized to send from this address")
			return
		}
		mailboxID = mb.ID
	} else {
		var account models.WebmailAccount
		if err := h.db.First(&account, claims.WebmailAccountID).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to resolve account")
			return
		}
		mailboxID = account.PrimaryMailboxID
	}

	toJSON, _ := json.Marshal(req.To)
	if req.To == nil {
		toJSON = []byte("[]")
	}
	ccJSON, _ := json.Marshal(req.Cc)
	if req.Cc == nil {
		ccJSON = []byte("[]")
	}

	draft := models.Message{
		MailboxID:    mailboxID,
		Folder:       "Drafts",
		Sender:       req.From,
		RecipientsTo: models.JSONB(toJSON),
		RecipientsCc: models.JSONB(ccJSON),
		Subject:      req.Subject,
		BodyText:     req.BodyText,
		BodyHTML:     req.BodyHTML,
		InReplyTo:    req.InReplyTo,
		References:   req.References,
		IsDraft:      true,
		IsRead:       true,
		SizeBytes:    len(req.Subject) + len(req.BodyText) + len(req.BodyHTML),
		ReceivedAt:   time.Now(),
	}

	if err := h.db.Create(&draft).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to save draft")
		return
	}

	respond.Data(w, http.StatusCreated, draft)
}

// UpdateDraft updates an existing draft message.
// PUT /api/v1/messages/draft/{id}
func (h *MessageHandler) UpdateDraft(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var draft models.Message
	if err := h.db.Where("id = ? AND is_draft = ?", id, true).First(&draft).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Draft not found")
		return
	}

	if !h.verifyMessageOwnership(w, r, &draft) {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	var req struct {
		From     *string  `json:"from"`
		To       []string `json:"to"`
		Cc       []string `json:"cc"`
		Subject  *string  `json:"subject"`
		BodyText *string  `json:"body_text"`
		BodyHTML *string  `json:"body_html"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	updates := map[string]interface{}{}
	if req.From != nil {
		updates["sender"] = *req.From
	}
	if req.To != nil {
		toJSON, _ := json.Marshal(req.To)
		updates["recipients_to"] = models.JSONB(toJSON)
	}
	if req.Cc != nil {
		ccJSON, _ := json.Marshal(req.Cc)
		updates["recipients_cc"] = models.JSONB(ccJSON)
	}
	if req.Subject != nil {
		updates["subject"] = *req.Subject
	}
	if req.BodyText != nil {
		updates["body_text"] = *req.BodyText
	}
	if req.BodyHTML != nil {
		updates["body_html"] = *req.BodyHTML
	}

	if len(updates) > 0 {
		h.db.Model(&draft).Updates(updates)
	}

	h.db.First(&draft, id)
	respond.Data(w, http.StatusOK, draft)
}

// SendDraft converts a draft to a sent message by deleting it and delegating to SendMessage.
// POST /api/v1/messages/draft/{id}/send
func (h *MessageHandler) SendDraft(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var draft models.Message
	if err := h.db.Where("id = ? AND is_draft = ?", id, true).First(&draft).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Draft not found")
		return
	}

	if !h.verifyMessageOwnership(w, r, &draft) {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	var toList []string
	_ = json.Unmarshal(draft.RecipientsTo, &toList)
	if len(toList) == 0 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Draft has no recipients")
		return
	}

	var ccList []string
	_ = json.Unmarshal(draft.RecipientsCc, &ccList)

	sendBody := map[string]interface{}{
		"from":        draft.Sender,
		"to":          toList,
		"cc":          ccList,
		"subject":     draft.Subject,
		"body_text":   draft.BodyText,
		"body_html":   draft.BodyHTML,
		"in_reply_to": draft.InReplyTo,
	}
	bodyBytes, _ := json.Marshal(sendBody)

	// Use a recorder to capture the send response so we only delete the
	// draft if the send actually succeeds.
	newReq, _ := http.NewRequestWithContext(r.Context(), "POST", "/api/v1/messages/send", strings.NewReader(string(bodyBytes)))
	newReq.Header.Set("Content-Type", "application/json")
	newReq.Header.Set("Authorization", r.Header.Get("Authorization"))

	rec := httptest.NewRecorder()
	h.SendMessage(rec, newReq)

	// Only delete the draft if send succeeded
	if rec.Code >= 200 && rec.Code < 300 {
		h.db.Delete(&draft)
	}

	// Copy recorded response to the actual ResponseWriter
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

// GetThread returns all messages sharing the same thread_id.
// GET /api/v1/accounts/{id}/threads/{threadID}
func (h *MessageHandler) GetThread(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	threadID := chi.URLParam(r, "threadID")
	if threadID == "" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Thread ID required")
		return
	}

	mailboxID, err := h.resolveAccountMailbox(uint(accountID), claims.WebmailAccountID)
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	var messages []models.Message
	if err := h.db.Where("mailbox_id = ? AND thread_id = ? AND is_deleted = ?", mailboxID, threadID, false).
		Order("received_at ASC").
		Find(&messages).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve thread")
		return
	}

	respond.List(w, messages, nil)
}

// GetRawMessage returns the raw RFC 2822 message content.
// GET /api/v1/messages/{id}/raw
func (h *MessageHandler) GetRawMessage(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var msg models.Message
	if err := h.db.First(&msg, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "message_not_found", "Message not found")
		return
	}

	if !h.verifyMessageOwnership(w, r, &msg) {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	if msg.RawMessage == "" {
		respond.Error(w, http.StatusNotFound, "not_found", "Raw message not available")
		return
	}

	w.Header().Set("Content-Type", "message/rfc822")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(msg.RawMessage))
}

// ForwardMessage forwards an existing message to new recipients.
// POST /api/v1/messages/{id}/forward
func (h *MessageHandler) ForwardMessage(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var original models.Message
	if err := h.db.First(&original, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "message_not_found", "Message not found")
		return
	}

	if !h.verifyMessageOwnership(w, r, &original) {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	var req struct {
		From     string   `json:"from"`
		To       []string `json:"to"`
		BodyText string   `json:"body_text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.From == "" || len(req.To) == 0 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "from and to are required")
		return
	}

	// Build forwarded body
	fwdBody := req.BodyText
	if fwdBody != "" {
		fwdBody += "\n\n"
	}
	fwdBody += "---------- Forwarded message ----------\n"
	fwdBody += fmt.Sprintf("From: %s\n", original.Sender)
	fwdBody += fmt.Sprintf("Subject: %s\n\n", original.Subject)
	fwdBody += original.BodyText

	// Build the send request body and delegate to SendMessage
	sendBody := map[string]interface{}{
		"from":      req.From,
		"to":        req.To,
		"subject":   "Fwd: " + original.Subject,
		"body_text": fwdBody,
		"body_html": original.BodyHTML,
	}
	bodyBytes, _ := json.Marshal(sendBody)

	newReq, _ := http.NewRequestWithContext(r.Context(), "POST", "/api/v1/messages/send", strings.NewReader(string(bodyBytes)))
	newReq.Header.Set("Content-Type", "application/json")
	newReq.Header.Set("Authorization", r.Header.Get("Authorization"))

	h.SendMessage(w, newReq)
}

// RespondToCalendar handles Accept/Decline/Tentative responses to calendar invites.
// POST /api/v1/messages/{id}/calendar-reply
func (h *MessageHandler) RespondToCalendar(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var msg models.Message
	if err := h.db.First(&msg, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "message_not_found", "Message not found")
		return
	}

	if !h.verifyMessageOwnership(w, r, &msg) {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	var req struct {
		Response string `json:"response"` // "ACCEPTED", "DECLINED", or "TENTATIVE"
		From     string `json:"from"`     // Sender address for the reply
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	// Validate response
	response := strings.ToUpper(req.Response)
	if response != "ACCEPTED" && response != "DECLINED" && response != "TENTATIVE" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "response must be ACCEPTED, DECLINED, or TENTATIVE")
		return
	}

	// Parse calendar events from the message
	var calEvents []pipeline.CalendarEvent
	if len(msg.CalendarEventsRaw) > 0 {
		_ = json.Unmarshal(msg.CalendarEventsRaw, &calEvents)
	}

	if len(calEvents) == 0 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Message does not contain calendar events")
		return
	}

	event := calEvents[0]

	if req.From == "" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "from is required")
		return
	}

	// Verify sender belongs to authenticated user
	senderMailbox, err := h.resolveSenderMailbox(req.From, claims.WebmailAccountID)
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "You are not authorized to send from this address")
		return
	}

	// Build the iCalendar REPLY body
	icsReply := rmime.BuildCalendarReply(event, req.From, response)

	// Build the MIME message with the calendar reply
	organizer := event.Organizer.Address
	if organizer == "" {
		organizer = msg.Sender
	}

	// The organizer address originates from an externally-received .ics file, so
	// it is fully attacker-controlled. Validate it before it is written into the
	// To: header and queued as an SMTP envelope recipient, or a CRLF-bearing
	// organizer would enable header forgery / SMTP command injection (#166).
	if err := rmail.ValidateAddress(organizer); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "invalid organizer address in calendar event")
		return
	}

	subject := event.Summary
	if subject == "" {
		subject = msg.Subject
	}
	switch response {
	case "ACCEPTED":
		subject = "Accepted: " + subject
	case "DECLINED":
		subject = "Declined: " + subject
	case "TENTATIVE":
		subject = "Tentative: " + subject
	}

	messageID := rmail.GenerateMessageID(rmail.DomainFromAddress(req.From))
	now := time.Now()

	// Build raw RFC 2822 message with calendar reply
	var b strings.Builder
	fromAddr := &mail.Address{Name: senderMailbox.DisplayName, Address: req.From}
	b.WriteString("From: " + fromAddr.String() + "\r\n")
	b.WriteString("To: " + organizer + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + messageID + "\r\n")
	if msg.MsgID != "" {
		b.WriteString("In-Reply-To: " + msg.MsgID + "\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\n")

	boundary := fmt.Sprintf("=_restmail_cal_%d", now.UnixNano())
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n")
	b.WriteString("\r\n")

	// Text part
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(fmt.Sprintf("Calendar invite %s: %s\r\n", strings.ToLower(response), event.Summary))
	b.WriteString("\r\n")

	// Calendar reply part
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/calendar; charset=utf-8; method=REPLY\r\n\r\n")
	b.WriteString(icsReply)
	b.WriteString("\r\n")
	b.WriteString("--" + boundary + "--\r\n")

	rawMessage := b.String()
	// OSI-8: fail closed if the DKIM key cannot be loaded (see signOutboundDKIM).
	signedRaw, signErr := h.signOutboundDKIM(rmail.DomainFromAddress(req.From), rawMessage)
	if signErr != nil {
		respond.Error(w, http.StatusServiceUnavailable, "deferred", "Try again later")
		return
	}
	rawMessage = signedRaw

	// Save the reply to Sent folder
	toJSON, _ := json.Marshal([]string{organizer})
	sentMsg := models.Message{
		MailboxID:    senderMailbox.ID,
		Folder:       "Sent",
		MsgID:        messageID,
		InReplyTo:    msg.MsgID,
		ThreadID:     msg.ThreadID,
		Sender:       req.From,
		SenderName:   senderMailbox.DisplayName,
		RecipientsTo: models.JSONB(toJSON),
		RecipientsCc: models.JSONB([]byte("[]")),
		Subject:      subject,
		BodyText:     fmt.Sprintf("Calendar invite %s: %s", strings.ToLower(response), event.Summary),
		IsRead:       true,
		SizeBytes:    len(rawMessage),
		ReceivedAt:   now,
	}

	if err := h.db.Create(&sentMsg).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to save calendar reply")
		return
	}

	// Deliver to organizer if local, else queue for remote delivery
	var recipientMailbox models.Mailbox
	if err := h.db.Where("address = ? AND active = ?", organizer, true).First(&recipientMailbox).Error; err == nil {
		_, deliverErr := h.deliverToLocal(r.Context(), localDeliveryParams{
			Mailbox:      recipientMailbox,
			Sender:       req.From,
			SenderName:   senderMailbox.DisplayName,
			RecipientsTo: toJSON,
			RecipientsCc: json.RawMessage("[]"),
			Subject:      subject,
			BodyText:     sentMsg.BodyText,
			MessageID:    messageID,
			InReplyTo:    msg.MsgID,
			RawMessage:   rawMessage,
		})
		if deliverErr != nil {
			slog.Warn("calendar reply local delivery failed", "recipient", organizer, "error", deliverErr)
		}
	} else {
		// Remote delivery
		recipientDomain := organizer
		if idx := strings.LastIndex(organizer, "@"); idx >= 0 {
			recipientDomain = organizer[idx+1:]
		}
		msgID := sentMsg.ID
		queueEntry := models.OutboundQueue{
			MessageID:  &msgID,
			Sender:     req.From,
			Recipient:  organizer,
			Domain:     recipientDomain,
			RawMessage: rawMessage,
			Status:     "pending",
		}
		h.db.Create(&queueEntry)
	}

	respond.Data(w, http.StatusOK, map[string]string{
		"status":   "sent",
		"response": response,
	})
}

// verifyMessageOwnership checks that the authenticated user owns the message.
// Returns true only if the message belongs to one of the authenticated mailbox
// account's mailboxes. These are mailbox-scoped webmail routes; there is no
// cross-mailbox admin bypass here (the deprecated IsAdmin claim was removed —
// OSI-14). Admin management happens on the capability-gated /admin surface.
func (h *MessageHandler) verifyMessageOwnership(w http.ResponseWriter, r *http.Request, msg *models.Message) bool {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return false
	}
	// Check primary mailbox
	var account models.WebmailAccount
	if err := h.db.First(&account, claims.WebmailAccountID).Error; err == nil {
		if msg.MailboxID == account.PrimaryMailboxID {
			return true
		}
	}
	// Check linked accounts
	var linked []models.LinkedAccount
	h.db.Where("webmail_account_id = ?", claims.WebmailAccountID).Find(&linked)
	for _, la := range linked {
		if msg.MailboxID == la.MailboxID {
			return true
		}
	}
	return false
}

// localDeliveryParams holds the information needed for local message delivery with
// full inbound pipeline processing, quota checks, attachment extraction, and SSE events.
type localDeliveryParams struct {
	Mailbox      models.Mailbox
	Sender       string
	SenderName   string
	RecipientsTo json.RawMessage
	RecipientsCc json.RawMessage
	Subject      string
	BodyText     string
	BodyHTML     string
	MessageID    string
	InReplyTo    string
	References   string
	RawMessage   string
	ClientIP     string
	HeloName     string
	// Inbound transport-security (always-on, inbound-MX only). Nil ReceivedTLS =
	// not an inbound-MX delivery (local send / IMAP APPEND / submission) →
	// persisted as NULL, never counted as a plaintext arrival.
	ReceivedTLS *bool
	TLSVersion  string
}

// receivedTLSFlag reports whether an inbound delivery arrived over TLS, for the
// pipeline Envelope.TLS so filters/pipeline see transport. A nil ReceivedTLS
// (non inbound-MX: IMAP APPEND / local webmail send, where transport is not
// applicable) is treated as false.
func receivedTLSFlag(receivedTLS *bool) bool {
	return receivedTLS != nil && *receivedTLS
}

// recordInboundReceived counts one inbound-MX arrival on messages_received,
// labelled by transport (tls when ReceivedTLS is true, plaintext when false).
// A nil ReceivedTLS marks a non inbound-MX delivery (IMAP APPEND, local webmail
// send) which is NOT counted — this metric measures mail received from the
// internet. The transport label is strictly bounded to tls|plaintext.
func recordInboundReceived(receivedTLS *bool) {
	if receivedTLS == nil {
		return
	}
	transport := "plaintext"
	if *receivedTLS {
		transport = "tls"
	}
	metrics.MessagesReceived.WithLabelValues(transport).Inc()
}

// deliverToLocal runs the inbound pipeline and delivers a message to a local mailbox.
// This is the shared logic used by both DeliverMessage (gateway inbound) and
// SendMessage (local recipient delivery).
func (h *MessageHandler) deliverToLocal(ctx context.Context, params localDeliveryParams) (*models.Message, error) {
	mailbox := params.Mailbox

	// Always-on: count every inbound-MX arrival (before the pipeline runs, so a
	// later reject/quarantine still counts as received) on messages_received,
	// labelled by transport. Non inbound-MX deliveries (nil ReceivedTLS) are not
	// counted — this measures mail received from the internet.
	recordInboundReceived(params.ReceivedTLS)

	// ── Build pipeline EmailJSON ─────────────────────────────────────
	var toAddrs []pipeline.Address
	if params.RecipientsTo != nil {
		var toStrings []string
		if json.Unmarshal(params.RecipientsTo, &toStrings) == nil {
			for _, addr := range toStrings {
				toAddrs = append(toAddrs, pipeline.Address{Address: addr})
			}
		}
	}
	var ccAddrs []pipeline.Address
	if params.RecipientsCc != nil {
		var ccStrings []string
		if json.Unmarshal(params.RecipientsCc, &ccStrings) == nil {
			for _, addr := range ccStrings {
				ccAddrs = append(ccAddrs, pipeline.Address{Address: addr})
			}
		}
	}

	// The raw MIME message is the authoritative source for body, attachments,
	// inline parts, calendar events, and headers (Date/Subject/Message-ID).
	// Build the pipeline message by PARSING it — the gateway only forwards a few
	// cherry-picked params, so constructing solely from those silently dropped
	// every attachment (and the Date header, which header_validate then rejected
	// on). Fall back to the params only when there is no parseable raw message.
	var emailJSON *pipeline.EmailJSON
	if params.RawMessage != "" {
		if parsed, perr := rmime.Parse([]byte(params.RawMessage)); perr == nil {
			emailJSON = parsed
		}
	}
	if emailJSON == nil {
		emailJSON = &pipeline.EmailJSON{
			Headers: pipeline.Headers{
				Subject:   params.Subject,
				MessageID: params.MessageID,
				InReplyTo: params.InReplyTo,
			},
			Body: pipeline.Body{ContentType: "text/plain", Content: params.BodyText},
		}
		if params.BodyText != "" && params.BodyHTML != "" {
			emailJSON.Body = pipeline.Body{
				ContentType: "multipart/alternative",
				Parts: []pipeline.Body{
					{ContentType: "text/plain; charset=utf-8", Content: params.BodyText},
					{ContentType: "text/html; charset=utf-8", Content: params.BodyHTML},
				},
			}
		} else if params.BodyHTML != "" {
			emailJSON.Body = pipeline.Body{ContentType: "text/html", Content: params.BodyHTML}
		}
	}

	// The SMTP envelope is not part of the message body — always set it from the
	// delivery params, with the resolved mailbox as the authoritative recipient.
	emailJSON.Envelope = pipeline.Envelope{
		MailFrom:  params.Sender,
		RcptTo:    []string{mailbox.Address},
		ClientIP:  params.ClientIP,
		Helo:      params.HeloName,
		TLS:       receivedTLSFlag(params.ReceivedTLS),
		Direction: "inbound",
	}
	// Fill any header the parse didn't populate from the gateway-extracted params.
	if len(emailJSON.Headers.From) == 0 {
		emailJSON.Headers.From = []pipeline.Address{{Name: params.SenderName, Address: params.Sender}}
	}
	if len(emailJSON.Headers.To) == 0 {
		emailJSON.Headers.To = toAddrs
	}
	if len(emailJSON.Headers.Cc) == 0 {
		emailJSON.Headers.Cc = ccAddrs
	}
	if emailJSON.Headers.Subject == "" {
		emailJSON.Headers.Subject = params.Subject
	}
	if emailJSON.Headers.MessageID == "" {
		emailJSON.Headers.MessageID = params.MessageID
	}
	if emailJSON.Headers.InReplyTo == "" {
		emailJSON.Headers.InReplyTo = params.InReplyTo
	}

	// The raw source is the authoritative bytes for DKIM verification — thread
	// it through so dkim_verify can canonicalize exactly what the signer signed.
	if params.RawMessage != "" {
		if emailJSON.Metadata == nil {
			emailJSON.Metadata = make(map[string]string)
		}
		emailJSON.Metadata["raw_message"] = params.RawMessage
	}

	// ── Run inbound pipeline ─────────────────────────────────────────
	// deliveredTrace, set in the pipeline's continue branch, defers recording the
	// happy-path trace until the Message row exists so its message_id is non-nil.
	var deliveredTrace *traceInputs
	if h.engine != nil {
		var pipelineCfg *pipeline.PipelineConfig
		var dbPipeline models.Pipeline
		if err := h.db.Where("domain_id = ? AND direction = ? AND active = ?", mailbox.DomainID, "inbound", true).
			First(&dbPipeline).Error; err == nil {
			var filterConfigs []pipeline.FilterConfig
			if jsonErr := json.Unmarshal(dbPipeline.Filters, &filterConfigs); jsonErr == nil {
				pipelineCfg = &pipeline.PipelineConfig{
					ID:        dbPipeline.ID,
					DomainID:  dbPipeline.DomainID,
					Direction: dbPipeline.Direction,
					Filters:   filterConfigs,
					Active:    dbPipeline.Active,
				}
			}
		}
		if pipelineCfg == nil {
			pipelineCfg = pipeline.DefaultInboundPipeline(mailbox.DomainID)
		}

		pipelineCtx := pipeline.WithDB(ctx, h.db)
		pipelineResult, pipeErr := h.engine.Execute(pipelineCtx, pipelineCfg, emailJSON)
		if pipeErr != nil {
			return nil, fmt.Errorf("pipeline execution failed: %w", pipeErr)
		}

		// Base trace inputs shared by every terminal branch. The envelope,
		// transport and RFC Message-ID are snapshotted here (pre-continue) so a
		// later FinalEmail reassignment can't alter them; per-branch code sets
		// Outcome (and, on delivery, MessageID) before recording.
		traceBase := traceInputs{
			PipelineID:   pipelineCfg.ID,
			Direction:    "inbound",
			Result:       pipelineResult,
			Envelope:     emailJSON.Envelope,
			Transport:    transportLabel(params.ReceivedTLS),
			RFCMessageID: emailJSON.Headers.MessageID,
		}

		switch pipelineResult.FinalAction {
		case pipeline.ActionReject:
			traceBase.Outcome = outcomeRejected
			h.recordTrace(buildTrace(traceBase))
			return nil, fmt.Errorf("rejected: %s", pipelineResult.RejectMsg)
		case pipeline.ActionQuarantine:
			preview := params.BodyText
			if len(preview) > 200 {
				preview = preview[:200]
			}
			h.db.Create(&models.Quarantine{
				MailboxID:        mailbox.ID,
				Sender:           params.Sender,
				Subject:          params.Subject,
				BodyPreview:      preview,
				RawMessage:       params.RawMessage,
				QuarantineReason: "pipeline",
				ReceivedAt:       time.Now(),
				ExpiresAt:        time.Now().Add(30 * 24 * time.Hour),
			})
			traceBase.Outcome = outcomeQuarantined
			h.recordTrace(buildTrace(traceBase))
			return nil, nil // quarantined, not an error but no message created
		case pipeline.ActionDiscard:
			traceBase.Outcome = outcomeDiscarded
			h.recordTrace(buildTrace(traceBase))
			return nil, nil // discarded, not an error but no message created
		case pipeline.ActionDefer:
			traceBase.Outcome = outcomeDeferred
			h.recordTrace(buildTrace(traceBase))
			return nil, fmt.Errorf("deferred: try again later")
		case pipeline.ActionContinue:
			emailJSON = pipelineResult.FinalEmail
			// Stash the delivered trace; it is recorded after the Message row is
			// created so message_id is non-nil (the only path where it is set).
			base := traceBase
			deliveredTrace = &base
		}
	}

	// ── Persist Authentication-Results ───────────────────────────────
	// Prepend any Authentication-Results the inbound pipeline produced (the
	// dkim/spf/dmarc verdicts) onto the stored raw message — standard receiver
	// behavior — so the outcome is visible via the message API, not buried in
	// the pipeline execution log.
	if emailJSON != nil && params.RawMessage != "" {
		if ars := emailJSON.Headers.Raw["Authentication-Results"]; len(ars) > 0 {
			var b strings.Builder
			for _, ar := range ars {
				b.WriteString("Authentication-Results: ")
				b.WriteString(ar)
				b.WriteString("\r\n")
			}
			params.RawMessage = b.String() + params.RawMessage
		}
	}

	// ── Ensure Message-ID exists ────────────────────────────────────
	if params.MessageID == "" {
		params.MessageID = rmail.GenerateMessageID(rmail.DomainFromAddress(params.Sender))
	}

	// ── Quota check ──────────────────────────────────────────────────
	sizeBytes := len(params.Subject) + len(params.BodyText) + len(params.BodyHTML)
	if mailbox.QuotaBytes > 0 && mailbox.QuotaUsedBytes+int64(sizeBytes) > mailbox.QuotaBytes {
		return nil, fmt.Errorf("mailbox_full: recipient mailbox is over quota")
	}

	// ── Compute thread ID ────────────────────────────────────────────
	threadID := params.MessageID
	if params.References != "" {
		refs := strings.Fields(params.References)
		if len(refs) > 0 {
			threadID = refs[0]
		}
	} else if params.InReplyTo != "" {
		threadID = params.InReplyTo
	}
	// Canonicalize to the bracketed form so a References-derived thread ID matches
	// the root message's Message-ID (thread grouping is an equality match).
	threadID = rmail.CanonicalID(threadID)

	// ── Create message ───────────────────────────────────────────────
	msg := models.Message{
		MailboxID:    mailbox.ID,
		Folder:       "INBOX",
		MsgID:        params.MessageID,
		InReplyTo:    params.InReplyTo,
		References:   params.References,
		ThreadID:     threadID,
		Sender:       params.Sender,
		SenderName:   params.SenderName,
		RecipientsTo: models.JSONB(params.RecipientsTo),
		RecipientsCc: models.JSONB(params.RecipientsCc),
		Subject:      params.Subject,
		BodyText:     params.BodyText,
		BodyHTML:     params.BodyHTML,
		RawMessage:   params.RawMessage,
		SizeBytes:    sizeBytes,
		// Exact octet count of the stored raw (IMAP RFC822.SIZE / POP3 LIST
		// report this; it must equal the bytes served verbatim). Zero when no
		// raw was provided — consumers fall back to SizeBytes.
		RawSize: len(params.RawMessage),
		// Always-on inbound transport-security metrics. Nil/empty for non
		// inbound-MX deliveries (persisted as NULL / "" = unknown), correlatable
		// with the Authentication-Results prepended above.
		ReceivedTLS: params.ReceivedTLS,
		TLSVersion:  params.TLSVersion,
	}

	if err := h.db.Create(&msg).Error; err != nil {
		return nil, fmt.Errorf("failed to create message: %w", err)
	}

	// ── Record the delivered trace ───────────────────────────────────
	// The pipeline said continue and the Message row now exists — record the
	// happy-path trace with message_id non-nil (the sole path that sets it) and
	// the persisted RFC Message-ID (generated above if the sender supplied none).
	if deliveredTrace != nil {
		deliveredTrace.Outcome = outcomeDelivered
		deliveredTrace.RFCMessageID = msg.MsgID
		mid := msg.ID
		deliveredTrace.MessageID = &mid
		h.recordTrace(buildTrace(*deliveredTrace))
	}

	// ── Update quota ─────────────────────────────────────────────────
	h.db.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", mailbox.ID).Updates(map[string]interface{}{
		"subject_bytes": gorm.Expr("subject_bytes + ?", len(params.Subject)),
		"body_bytes":    gorm.Expr("body_bytes + ?", len(params.BodyText)+len(params.BodyHTML)),
		"message_count": gorm.Expr("message_count + 1"),
	})
	h.db.Model(&models.Mailbox{}).Where("id = ?", mailbox.ID).Update("quota_used_bytes", gorm.Expr("quota_used_bytes + ?", sizeBytes))

	// ── Persist attachments ──────────────────────────────────────────
	if emailJSON != nil {
		var hasAttachments bool
		allAttachments := append(emailJSON.Attachments, emailJSON.Inline...)
		for _, att := range allAttachments {
			if att.Ref == "" {
				continue
			}
			dbAtt := models.Attachment{
				MessageID:   msg.ID,
				Filename:    att.Filename,
				ContentType: att.ContentType,
				SizeBytes:   att.Size,
				StorageType: att.Storage,
				StorageRef:  att.Ref,
				Checksum:    att.Checksum,
			}
			if err := h.db.Create(&dbAtt).Error; err != nil {
				slog.Error("deliver: failed to persist attachment", "message_id", msg.ID, "filename", att.Filename, "error", err)
				continue
			}
			hasAttachments = true
		}
		if hasAttachments {
			h.db.Model(&msg).Update("has_attachments", true)

			var totalAttBytes int64
			for _, att := range allAttachments {
				if att.Ref != "" {
					totalAttBytes += att.Size
				}
			}
			if totalAttBytes > 0 {
				h.db.Model(&models.Mailbox{}).Where("id = ?", mailbox.ID).
					Update("quota_used_bytes", gorm.Expr("quota_used_bytes + ?", totalAttBytes))
				h.db.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", mailbox.ID).
					Update("attachment_bytes", gorm.Expr("attachment_bytes + ?", totalAttBytes))
			}
		}
	}

	// ── Extract calendar events ─────────────────────────────────────
	if params.RawMessage != "" {
		if parsed, parseErr := rmime.Parse([]byte(params.RawMessage)); parseErr == nil && len(parsed.CalendarEvents) > 0 {
			if calJSON, jsonErr := json.Marshal(parsed.CalendarEvents); jsonErr == nil {
				h.db.Model(&msg).Update("calendar_events", models.JSONB(calJSON))
				msg.CalendarEventsRaw = models.JSONB(calJSON)
			}

			// Track calendar event versions for update/cancel detection
			for _, evt := range parsed.CalendarEvents {
				if evt.UID == "" {
					continue
				}
				msgID := msg.ID
				var dtStart, dtEnd *time.Time
				if !evt.DTStart.IsZero() {
					t := evt.DTStart
					dtStart = &t
				}
				if !evt.DTEnd.IsZero() {
					t := evt.DTEnd
					dtEnd = &t
				}
				ver := models.CalendarEventVersion{
					MailboxID: mailbox.ID,
					UID:       evt.UID,
					Sequence:  evt.Sequence,
					Method:    evt.Method,
					Status:    evt.Status,
					Summary:   evt.Summary,
					DTStart:   dtStart,
					DTEnd:     dtEnd,
					Organizer: evt.Organizer.Address,
					MessageID: &msgID,
				}
				h.db.Create(&ver)
			}
		}
	}

	// ── SSE notifications ────────────────────────────────────────────
	if h.broker != nil {
		h.broker.Publish(mailbox.ID, SSEEvent{
			Type: "new_message",
			Data: map[string]interface{}{
				"message_id": msg.ID,
				"folder":     msg.Folder,
				"sender":     msg.Sender,
				"subject":    msg.Subject,
			},
		})
		var unreadCount int64
		h.db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ? AND is_read = ? AND is_deleted = ?",
			mailbox.ID, "INBOX", false, false).Count(&unreadCount)
		h.broker.Publish(mailbox.ID, SSEEvent{
			Type: "folder_update",
			Data: map[string]interface{}{
				"folder":       "INBOX",
				"unread_count": unreadCount,
			},
		})
	}

	return &msg, nil
}

func (h *MessageHandler) resolveAccountMailbox(accountID, webmailAccountID uint) (uint, error) {
	return resolveAccountMailbox(h.db, accountID, webmailAccountID)
}

// ListCalendarEvents returns calendar event versions for a mailbox,
// grouped by UID with the latest sequence number, enabling the frontend
// to detect superseded or cancelled events.
func (h *MessageHandler) ListCalendarEvents(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	mailboxID, err := h.resolveAccountMailbox(uint(accountID), claims.WebmailAccountID)
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	var versions []models.CalendarEventVersion
	h.db.Where("mailbox_id = ?", mailboxID).
		Order("uid, sequence DESC").
		Find(&versions)

	// Group by UID: for each UID, mark which is the latest version
	type calEventSummary struct {
		UID         string `json:"uid"`
		Method      string `json:"method"`
		Status      string `json:"status"`
		Summary     string `json:"summary"`
		Sequence    int    `json:"sequence"`
		IsCancelled bool   `json:"is_cancelled"`
		MessageID   *uint  `json:"message_id,omitempty"`
		Versions    int    `json:"versions"`
	}

	seen := map[string]*calEventSummary{}
	var order []string // preserve insertion order
	for _, v := range versions {
		if existing, ok := seen[v.UID]; ok {
			existing.Versions++
			// Check if any version is a CANCEL
			if v.Method == "CANCEL" {
				existing.IsCancelled = true
			}
			continue
		}
		s := &calEventSummary{
			UID:         v.UID,
			Method:      v.Method,
			Status:      v.Status,
			Summary:     v.Summary,
			Sequence:    v.Sequence,
			IsCancelled: v.Method == "CANCEL" || v.Status == "CANCELLED",
			MessageID:   v.MessageID,
			Versions:    1,
		}
		seen[v.UID] = s
		order = append(order, v.UID)
	}

	result := make([]calEventSummary, 0, len(order))
	for _, uid := range order {
		result = append(result, *seen[uid])
	}

	respond.Data(w, http.StatusOK, result)
}
