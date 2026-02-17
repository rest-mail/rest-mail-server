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
	"gorm.io/gorm"
)

type MessageHandler struct {
	db     *gorm.DB
	broker *SSEBroker
}

func NewMessageHandler(db *gorm.DB, broker *SSEBroker) *MessageHandler {
	return &MessageHandler{db: db, broker: broker}
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
			// Remote delivery - queue it
			recipientDomain := rcpt
			if idx := strings.LastIndex(rcpt, "@"); idx >= 0 {
				recipientDomain = rcpt[idx+1:]
			}
			queueEntry := models.OutboundQueue{
				Sender:    req.From,
				Recipient: rcpt,
				Domain:    recipientDomain,
				Status:    "pending",
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
