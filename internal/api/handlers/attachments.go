package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// AttachmentHandler handles attachment retrieval.
type AttachmentHandler struct {
	db *gorm.DB
}

// NewAttachmentHandler creates a new AttachmentHandler.
func NewAttachmentHandler(db *gorm.DB) *AttachmentHandler {
	return &AttachmentHandler{db: db}
}

// GetAttachment streams an attachment file from storage.
// GET /api/v1/attachments/{id}
func (h *AttachmentHandler) GetAttachment(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid attachment ID")
		return
	}

	var att models.Attachment
	query := h.db.Joins("JOIN messages ON messages.id = attachments.message_id").
		Joins("JOIN mailboxes ON mailboxes.id = messages.mailbox_id")

	if !claims.IsAdmin {
		mailboxIDs := h.getUserMailboxIDs(claims.WebmailAccountID)
		if len(mailboxIDs) == 0 {
			respond.Error(w, http.StatusNotFound, "not_found", "Attachment not found")
			return
		}
		query = query.Where("mailboxes.id IN ?", mailboxIDs)
	}

	if err := query.First(&att, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Attachment not found")
		return
	}

	if att.StorageType != "filesystem" {
		respond.Error(w, http.StatusNotImplemented, "not_implemented", "S3 storage retrieval not yet supported")
		return
	}

	// Security: ensure the storage ref is within the attachments directory
	cleanPath := filepath.Clean(att.StorageRef)
	if !strings.HasPrefix(cleanPath, "/attachments/") || strings.Contains(cleanPath, "..") {
		respond.Error(w, http.StatusForbidden, "forbidden", "Invalid storage path")
		return
	}

	file, err := os.Open(cleanPath)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Attachment file not found on disk")
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to stat attachment")
		return
	}

	w.Header().Set("Content-Type", att.ContentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+att.Filename+"\"")
	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))

	http.ServeContent(w, r, att.Filename, stat.ModTime(), file)
}

// ListAttachments returns attachment metadata for a message.
// GET /api/v1/messages/{id}/attachments
func (h *AttachmentHandler) ListAttachments(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	messageID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var msg models.Message
	if err := h.db.First(&msg, messageID).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Message not found")
		return
	}
	if !claims.IsAdmin {
		mailboxIDs := h.getUserMailboxIDs(claims.WebmailAccountID)
		owned := false
		for _, mid := range mailboxIDs {
			if msg.MailboxID == mid {
				owned = true
				break
			}
		}
		if !owned {
			respond.Error(w, http.StatusNotFound, "not_found", "Message not found")
			return
		}
	}

	var attachments []models.Attachment
	h.db.Where("message_id = ?", messageID).Order("id ASC").Find(&attachments)

	respond.List(w, attachments, nil)
}

// getUserMailboxIDs returns all mailbox IDs the user has access to (primary + linked).
func (h *AttachmentHandler) getUserMailboxIDs(webmailAccountID uint) []uint {
	var ids []uint
	var account models.WebmailAccount
	if err := h.db.First(&account, webmailAccountID).Error; err == nil {
		ids = append(ids, account.PrimaryMailboxID)
	}
	var linked []models.LinkedAccount
	h.db.Where("webmail_account_id = ?", webmailAccountID).Find(&linked)
	for _, la := range linked {
		ids = append(ids, la.MailboxID)
	}
	return ids
}
