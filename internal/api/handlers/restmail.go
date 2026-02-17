package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// RestmailHandler implements the RESTMAIL server-to-server protocol endpoints.
// These are unauthenticated (like SMTP — any server can deliver to you).
// Authentication is via DKIM/SPF/DMARC verification, not API keys.
type RestmailHandler struct {
	db *gorm.DB
}

func NewRestmailHandler(db *gorm.DB) *RestmailHandler {
	return &RestmailHandler{db: db}
}

// Capabilities returns the RESTMAIL server capabilities.
// GET /restmail/capabilities
func (h *RestmailHandler) Capabilities(w http.ResponseWriter, r *http.Request) {
	respond.Data(w, http.StatusOK, map[string]interface{}{
		"protocol": "RESTMAIL",
		"version":  "1.0",
		"features": []string{
			"delivery",
			"recipient-check",
		},
	})
}

// CheckMailbox verifies a recipient mailbox exists.
// GET /restmail/mailboxes?address=...
func (h *RestmailHandler) CheckMailbox(w http.ResponseWriter, r *http.Request) {
	address := r.URL.Query().Get("address")
	if address == "" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "address query parameter required")
		return
	}

	var mailbox models.Mailbox
	if err := h.db.Where("address = ? AND active = ?", address, true).First(&mailbox).Error; err != nil {
		respond.Data(w, http.StatusOK, map[string]interface{}{
			"exists": false,
		})
		return
	}

	respond.Data(w, http.StatusOK, map[string]interface{}{
		"exists":  true,
		"address": mailbox.Address,
	})
}

// Deliver receives a message from another RESTMAIL server.
// POST /restmail/messages
func (h *RestmailHandler) Deliver(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From       string          `json:"from"`
		To         []string        `json:"to"`
		Subject    string          `json:"subject"`
		BodyText   string          `json:"body_text"`
		BodyHTML   string          `json:"body_html"`
		MessageID  string          `json:"message_id"`
		InReplyTo  string          `json:"in_reply_to"`
		References string          `json:"references"`
		Headers    json.RawMessage `json:"headers"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if len(req.To) == 0 {
		respond.ValidationError(w, map[string]string{"to": "at least one recipient required"})
		return
	}

	// TODO: Verify DKIM/SPF/DMARC here

	var delivered []string
	var failed []string

	for _, rcpt := range req.To {
		var mailbox models.Mailbox
		if err := h.db.Where("address = ? AND active = ?", rcpt, true).First(&mailbox).Error; err != nil {
			failed = append(failed, rcpt)
			continue
		}

		// Check quota
		if mailbox.QuotaUsedBytes >= mailbox.QuotaBytes {
			failed = append(failed, rcpt)
			continue
		}

		sizeBytes := len(req.Subject) + len(req.BodyText) + len(req.BodyHTML)

		threadID := req.MessageID
		if req.InReplyTo != "" {
			threadID = req.InReplyTo
		}

		recipientsTo, _ := json.Marshal([]map[string]string{{"address": rcpt}})

		msg := models.Message{
			MailboxID:    mailbox.ID,
			Folder:       "INBOX",
			MessageID:    req.MessageID,
			InReplyTo:    req.InReplyTo,
			References:   req.References,
			ThreadID:     threadID,
			Sender:       req.From,
			RecipientsTo: models.JSONB(recipientsTo),
			Subject:      req.Subject,
			BodyText:     req.BodyText,
			BodyHTML:     req.BodyHTML,
			Headers:      models.JSONB(req.Headers),
			SizeBytes:    sizeBytes,
		}

		if err := h.db.Create(&msg).Error; err != nil {
			failed = append(failed, rcpt)
			continue
		}

		// Update quota
		h.db.Model(&mailbox).Update("quota_used_bytes", gorm.Expr("quota_used_bytes + ?", sizeBytes))
		h.db.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", mailbox.ID).Updates(map[string]interface{}{
			"subject_bytes": gorm.Expr("subject_bytes + ?", len(req.Subject)),
			"body_bytes":    gorm.Expr("body_bytes + ?", len(req.BodyText)+len(req.BodyHTML)),
			"message_count": gorm.Expr("message_count + 1"),
		})

		delivered = append(delivered, rcpt)
	}

	status := http.StatusCreated
	if len(delivered) == 0 {
		status = http.StatusUnprocessableEntity
	}

	respond.Data(w, status, map[string]interface{}{
		"delivered": delivered,
		"failed":    failed,
	})
}
