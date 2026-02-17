package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
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

	if msg.Folder == "Trash" || msg.IsDeleted {
		h.db.Delete(&msg)
	} else {
		h.db.Model(&msg).Updates(map[string]interface{}{
			"folder":     "Trash",
			"is_deleted": true,
		})
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
		From     string   `json:"from"`
		To       []string `json:"to"`
		Cc       []string `json:"cc"`
		Bcc      []string `json:"bcc"`
		Subject  string   `json:"subject"`
		BodyText string   `json:"body_text"`
		BodyHTML string   `json:"body_html"`
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

		outResult, outErr := h.engine.Execute(r.Context(), outPipelineCfg, outEmailJSON)
		if outErr != nil {
			respond.Error(w, http.StatusInternalServerError, "pipeline_error", "Outbound pipeline execution failed")
			return
		}

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
			// Pipeline passed, continue with sending
		}
	}

	// Generate Message-ID
	uuidBytes := make([]byte, 16)
	if _, err := rand.Read(uuidBytes); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to generate message ID")
		return
	}
	domain := req.From
	if idx := strings.LastIndex(req.From, "@"); idx >= 0 {
		domain = req.From[idx+1:]
	}
	messageID := fmt.Sprintf("<%x-%x-%x-%x-%x@%s>",
		uuidBytes[0:4], uuidBytes[4:6], uuidBytes[6:8], uuidBytes[8:10], uuidBytes[10:16], domain)

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

	// Create message in sender's Sent folder
	sentMsg := models.Message{
		MailboxID:    senderMailbox.ID,
		Folder:       "Sent",
		MessageID:    messageID,
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

	if err := h.db.Create(&sentMsg).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to save sent message")
		return
	}

	// Build raw RFC 2822 message for outbound queue entries.
	var rawMessage string
	{
		var b strings.Builder
		b.WriteString("From: ")
		if senderMailbox.DisplayName != "" {
			b.WriteString(fmt.Sprintf("%q <%s>", senderMailbox.DisplayName, req.From))
		} else {
			b.WriteString(req.From)
		}
		b.WriteString("\r\n")
		b.WriteString("To: " + strings.Join(req.To, ", ") + "\r\n")
		if len(req.Cc) > 0 {
			b.WriteString("Cc: " + strings.Join(req.Cc, ", ") + "\r\n")
		}
		b.WriteString("Subject: " + req.Subject + "\r\n")
		b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
		b.WriteString("Message-ID: " + messageID + "\r\n")
		b.WriteString("MIME-Version: 1.0\r\n")

		if req.BodyText != "" && req.BodyHTML != "" {
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
			// Local delivery
			inboxMsg := models.Message{
				MailboxID:    recipientMailbox.ID,
				Folder:       "INBOX",
				MessageID:    messageID,
				Sender:       req.From,
				SenderName:   senderMailbox.DisplayName,
				RecipientsTo: models.JSONB(toJSON),
				RecipientsCc: models.JSONB(ccJSON),
				Subject:      req.Subject,
				BodyText:     req.BodyText,
				BodyHTML:     req.BodyHTML,
				IsRead:       false,
				SizeBytes:    sizeBytes,
				ReceivedAt:   now,
			}
			h.db.Create(&inboxMsg)
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

	// ── Pipeline execution ───────────────────────────────────────────
	// Convert delivery request to pipeline.EmailJSON for filter processing.
	var toAddrs []pipeline.Address
	if req.RecipientsTo != nil {
		var toStrings []string
		if json.Unmarshal(req.RecipientsTo, &toStrings) == nil {
			for _, addr := range toStrings {
				toAddrs = append(toAddrs, pipeline.Address{Address: addr})
			}
		}
	}
	var ccAddrs []pipeline.Address
	if req.RecipientsCc != nil {
		var ccStrings []string
		if json.Unmarshal(req.RecipientsCc, &ccStrings) == nil {
			for _, addr := range ccStrings {
				ccAddrs = append(ccAddrs, pipeline.Address{Address: addr})
			}
		}
	}

	emailJSON := &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{
			MailFrom:  req.Sender,
			RcptTo:    []string{mailbox.Address},
			ClientIP:  req.ClientIP,
			Helo:      req.HeloName,
			Direction: "inbound",
		},
		Headers: pipeline.Headers{
			From:      []pipeline.Address{{Name: req.SenderName, Address: req.Sender}},
			To:        toAddrs,
			Cc:        ccAddrs,
			Subject:   req.Subject,
			MessageID: req.MessageID,
			InReplyTo: req.InReplyTo,
		},
		Body: pipeline.Body{
			ContentType: "text/plain",
			Content:     req.BodyText,
		},
	}
	// If there is both text and HTML, use multipart/alternative parts.
	if req.BodyText != "" && req.BodyHTML != "" {
		emailJSON.Body = pipeline.Body{
			ContentType: "multipart/alternative",
			Parts: []pipeline.Body{
				{ContentType: "text/plain; charset=utf-8", Content: req.BodyText},
				{ContentType: "text/html; charset=utf-8", Content: req.BodyHTML},
			},
		}
	} else if req.BodyHTML != "" {
		emailJSON.Body = pipeline.Body{
			ContentType: "text/html",
			Content:     req.BodyHTML,
		}
	}

	// Look up the domain's inbound pipeline config from DB.
	var pipelineCfg *pipeline.PipelineConfig
	var dbPipeline models.Pipeline
	if err := h.db.Where("domain_id = ? AND direction = ? AND active = ?", mailbox.DomainID, "inbound", true).
		First(&dbPipeline).Error; err == nil {
		// Found a DB-backed pipeline — parse its filter list.
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
	// Fall back to the default inbound pipeline if none was found in the DB.
	if pipelineCfg == nil {
		pipelineCfg = pipeline.DefaultInboundPipeline(mailbox.DomainID)
	}

	// Run the pipeline.
	if h.engine != nil {
		pipelineResult, pipeErr := h.engine.Execute(r.Context(), pipelineCfg, emailJSON)
		if pipeErr != nil {
			respond.Error(w, http.StatusInternalServerError, "pipeline_error", "Pipeline execution failed")
			return
		}

		h.logPipelineExecution(pipelineCfg.ID, nil, "inbound", pipelineResult)

		switch pipelineResult.FinalAction {
		case pipeline.ActionReject:
			rejectMsg := pipelineResult.RejectMsg
			if rejectMsg == "" {
				rejectMsg = "Message rejected by policy"
			}
			respond.Error(w, 550, "rejected", rejectMsg)
			return

		case pipeline.ActionQuarantine:
			// Insert into quarantine table instead of delivering.
			preview := req.BodyText
			if len(preview) > 200 {
				preview = preview[:200]
			}
			qItem := models.Quarantine{
				MailboxID:        mailbox.ID,
				Sender:           req.Sender,
				Subject:          req.Subject,
				BodyPreview:      preview,
				RawMessage:       req.RawMessage,
				QuarantineReason: "pipeline",
				ReceivedAt:       time.Now(),
				ExpiresAt:        time.Now().Add(30 * 24 * time.Hour),
			}
			h.db.Create(&qItem)
			respond.Data(w, http.StatusOK, map[string]string{"status": "quarantined"})
			return

		case pipeline.ActionDiscard:
			respond.Data(w, http.StatusOK, map[string]string{"status": "discarded"})
			return

		case pipeline.ActionDefer:
			respond.Error(w, 451, "deferred", "Try again later")
			return

		case pipeline.ActionContinue:
			// Update emailJSON from pipeline result in case transforms modified it.
			emailJSON = pipelineResult.FinalEmail
		}
	}

	// ── Insert message (continue action) ────────────────────────────

	sizeBytes := len(req.Subject) + len(req.BodyText) + len(req.BodyHTML)

	threadID := req.MessageID
	if req.InReplyTo != "" {
		threadID = req.InReplyTo
	}

	msg := models.Message{
		MailboxID:    mailbox.ID,
		Folder:       "INBOX",
		MessageID:    req.MessageID,
		InReplyTo:    req.InReplyTo,
		References:   req.References,
		ThreadID:     threadID,
		Sender:       req.Sender,
		SenderName:   req.SenderName,
		RecipientsTo: models.JSONB(req.RecipientsTo),
		RecipientsCc: models.JSONB(req.RecipientsCc),
		Subject:      req.Subject,
		BodyText:     req.BodyText,
		BodyHTML:     req.BodyHTML,
		RawMessage:   req.RawMessage,
		SizeBytes:    sizeBytes,
	}

	if err := h.db.Create(&msg).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to deliver message")
		return
	}

	// Update quota
	h.db.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", mailbox.ID).Updates(map[string]interface{}{
		"subject_bytes": gorm.Expr("subject_bytes + ?", len(req.Subject)),
		"body_bytes":    gorm.Expr("body_bytes + ?", len(req.BodyText)+len(req.BodyHTML)),
		"message_count": gorm.Expr("message_count + 1"),
	})
	h.db.Model(&models.Mailbox{}).Where("id = ?", mailbox.ID).Update("quota_used_bytes", gorm.Expr("quota_used_bytes + ?", sizeBytes))

	// Publish SSE event for real-time notification
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
		Select("folder, COUNT(*) as total, COUNT(CASE WHEN is_read = false THEN 1 END) as unread").
		Where("mailbox_id = ? AND is_deleted = ?", mailboxID, false).
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
		From     string   `json:"from"`
		To       []string `json:"to"`
		Cc       []string `json:"cc"`
		Subject  string   `json:"subject"`
		BodyText string   `json:"body_text"`
		BodyHTML string   `json:"body_html"`
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

	var toList []string
	json.Unmarshal(draft.RecipientsTo, &toList)
	if len(toList) == 0 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Draft has no recipients")
		return
	}

	var ccList []string
	json.Unmarshal(draft.RecipientsCc, &ccList)

	sendBody := map[string]interface{}{
		"from":      draft.Sender,
		"to":        toList,
		"cc":        ccList,
		"subject":   draft.Subject,
		"body_text": draft.BodyText,
		"body_html": draft.BodyHTML,
	}
	bodyBytes, _ := json.Marshal(sendBody)

	h.db.Delete(&draft)

	newReq, _ := http.NewRequestWithContext(r.Context(), "POST", "/api/v1/messages/send", strings.NewReader(string(bodyBytes)))
	newReq.Header.Set("Content-Type", "application/json")

	h.SendMessage(w, newReq)
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

func (h *MessageHandler) resolveAccountMailbox(accountID, webmailAccountID uint) (uint, error) {
	var account models.WebmailAccount
	if err := h.db.First(&account, accountID).Error; err == nil {
		if account.ID == webmailAccountID {
			return account.PrimaryMailboxID, nil
		}
	}

	var linked models.LinkedAccount
	if err := h.db.Where("webmail_account_id = ? AND id = ?", webmailAccountID, accountID).First(&linked).Error; err == nil {
		return linked.MailboxID, nil
	}

	return 0, fmt.Errorf("account not found or access denied")
}
