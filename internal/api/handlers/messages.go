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
	rmime "github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

type MessageHandler struct {
	db     *gorm.DB
	broker *SSEBroker
	engine *pipeline.Engine
}

func NewMessageHandler(db *gorm.DB, broker *SSEBroker, engine *pipeline.Engine) *MessageHandler {
	return &MessageHandler{db: db, broker: broker, engine: engine}
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

	if msg.Folder == "Trash" || msg.IsDeleted {
		h.db.Delete(&msg)
	} else {
		h.db.Model(&msg).Updates(map[string]interface{}{
			"folder":     "Trash",
			"is_deleted": true,
		})
	}

	// Reclaim quota on permanent delete
	if msg.Folder == "Trash" || msg.IsDeleted {
		reclaimBytes := int64(msg.SizeBytes)
		if msg.HasAttachments {
			var attBytes int64
			h.db.Model(&models.Attachment{}).Where("message_id = ?", msg.ID).
				Select("COALESCE(SUM(size_bytes), 0)").Scan(&attBytes)
			reclaimBytes += attBytes
		}
		if reclaimBytes > 0 {
			h.db.Model(&models.Mailbox{}).Where("id = ?", msg.MailboxID).
				Update("quota_used_bytes", gorm.Expr("GREATEST(quota_used_bytes - ?, 0)", reclaimBytes))
		}
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
		From           string                 `json:"from"`
		To             []string               `json:"to"`
		Cc             []string               `json:"cc"`
		Bcc            []string               `json:"bcc"`
		Subject        string                 `json:"subject"`
		BodyText       string                 `json:"body_text"`
		BodyHTML       string                 `json:"body_html"`
		InReplyTo      string                 `json:"in_reply_to"`
		CalendarEvent  *pipeline.CalendarEvent `json:"calendar_event,omitempty"`
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

		h.logPipelineExecution(outPipelineCfg.ID, nil, "outbound", outResult)

		switch outResult.FinalAction {
		case pipeline.ActionReject:
			rejectMsg := outResult.RejectMsg
			if rejectMsg == "" {
				rejectMsg = "Message rejected by outbound policy"
			}
			respond.Error(w, http.StatusForbidden, "rejected", rejectMsg)
			return
		case pipeline.ActionQuarantine:
			respond.Error(w, http.StatusForbidden, "quarantined", "Message held for review by outbound policy")
			return
		case pipeline.ActionDiscard:
			respond.Data(w, http.StatusOK, map[string]string{"status": "discarded"})
			return
		case pipeline.ActionDefer:
			respond.Error(w, http.StatusServiceUnavailable, "deferred", "Try again later")
			return
		case pipeline.ActionContinue:
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

	// Compute thread ID for sent message
	threadID := messageID
	if req.InReplyTo != "" {
		threadID = req.InReplyTo
	}

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

		// Write extra headers added by pipeline transforms (e.g. DKIM-Signature).
		for name, value := range extraHeaders {
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
		RawMessage   string          `json:"raw_message"`
		ClientIP     string          `json:"client_ip"`
		HeloName     string          `json:"helo_name"`
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
		RawMessage:   req.RawMessage,
		ClientIP:     req.ClientIP,
		HeloName:     req.HeloName,
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
	json.Unmarshal(draft.RecipientsTo, &toList)
	if len(toList) == 0 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Draft has no recipients")
		return
	}

	var ccList []string
	json.Unmarshal(draft.RecipientsCc, &ccList)

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
	w.Write(rec.Body.Bytes())
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

// logPipelineExecution persists a pipeline execution result to the pipeline_logs table.
func (h *MessageHandler) logPipelineExecution(pipelineID uint, messageID *uint, direction string, result *pipeline.ExecutionResult) {
	stepsJSON, _ := json.Marshal(result.Steps)
	h.db.Create(&models.PipelineLog{
		PipelineID: pipelineID,
		MessageID:  messageID,
		Direction:  direction,
		Action:     string(result.FinalAction),
		Steps:      stepsJSON,
		DurationMS: result.Duration.Milliseconds(),
	})
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
	w.Write([]byte(msg.RawMessage))
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
		json.Unmarshal(msg.CalendarEventsRaw, &calEvents)
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
// Returns true if the message belongs to one of the user's mailboxes, or if the user is an admin.
func (h *MessageHandler) verifyMessageOwnership(w http.ResponseWriter, r *http.Request, msg *models.Message) bool {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return false
	}
	if claims.IsAdmin {
		return true
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
}

// deliverToLocal runs the inbound pipeline and delivers a message to a local mailbox.
// This is the shared logic used by both DeliverMessage (gateway inbound) and
// SendMessage (local recipient delivery).
func (h *MessageHandler) deliverToLocal(ctx context.Context, params localDeliveryParams) (*models.Message, error) {
	mailbox := params.Mailbox

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

	emailJSON := &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{
			MailFrom:  params.Sender,
			RcptTo:    []string{mailbox.Address},
			ClientIP:  params.ClientIP,
			Helo:      params.HeloName,
			Direction: "inbound",
		},
		Headers: pipeline.Headers{
			From:      []pipeline.Address{{Name: params.SenderName, Address: params.Sender}},
			To:        toAddrs,
			Cc:        ccAddrs,
			Subject:   params.Subject,
			MessageID: params.MessageID,
			InReplyTo: params.InReplyTo,
		},
		Body: pipeline.Body{
			ContentType: "text/plain",
			Content:     params.BodyText,
		},
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
		emailJSON.Body = pipeline.Body{
			ContentType: "text/html",
			Content:     params.BodyHTML,
		}
	}

	// ── Run inbound pipeline ─────────────────────────────────────────
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

		h.logPipelineExecution(pipelineCfg.ID, nil, "inbound", pipelineResult)

		switch pipelineResult.FinalAction {
		case pipeline.ActionReject:
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
			return nil, nil // quarantined, not an error but no message created
		case pipeline.ActionDiscard:
			return nil, nil // discarded, not an error but no message created
		case pipeline.ActionDefer:
			return nil, fmt.Errorf("deferred: try again later")
		case pipeline.ActionContinue:
			emailJSON = pipelineResult.FinalEmail
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
			threadID = strings.Trim(refs[0], "<>")
		}
	} else if params.InReplyTo != "" {
		threadID = params.InReplyTo
	}

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
	}

	if err := h.db.Create(&msg).Error; err != nil {
		return nil, fmt.Errorf("failed to create message: %w", err)
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
	var account models.WebmailAccount
	if err := h.db.First(&account, accountID).Error; err == nil {
		if account.ID == webmailAccountID {
			return account.PrimaryMailboxID, nil
		}
	}

	// Check if user has a linked account whose mailbox matches the target account
	var targetAccount models.WebmailAccount
	if err := h.db.First(&targetAccount, accountID).Error; err == nil {
		var linked models.LinkedAccount
		if err := h.db.Where("webmail_account_id = ? AND mailbox_id = ?", webmailAccountID, targetAccount.PrimaryMailboxID).First(&linked).Error; err == nil {
			return linked.MailboxID, nil
		}
	}

	return 0, fmt.Errorf("account not found or access denied")
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
	var result []calEventSummary
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
		result = append(result, *s)
	}

	respond.Data(w, http.StatusOK, result)
}
