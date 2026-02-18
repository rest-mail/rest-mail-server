package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
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
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid attachment ID")
		return
	}

	var att models.Attachment
	if err := h.db.First(&att, id).Error; err != nil {
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
	messageID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	var attachments []models.Attachment
	h.db.Where("message_id = ?", messageID).Order("id ASC").Find(&attachments)

	respond.List(w, attachments, nil)
}
