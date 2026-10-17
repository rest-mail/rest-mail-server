package handlers

import (
	"errors"
	"mime"
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

// attachmentsRoot is the base directory filesystem-backed attachments are
// written under (see internal/pipeline/filters/extract_attachments.go).
// Retrieval is confined to this tree.
const attachmentsRoot = "/attachments"

// errAttachmentPathEscape is returned when a stored attachment path resolves
// outside the attachments root (directly, via traversal, or via a symlink).
var errAttachmentPathEscape = errors.New("attachment path escapes storage root")

// resolveAttachmentPath validates storageRef and returns the real, canonical
// file path to serve. It confines retrieval to root both lexically and after
// resolving symlinks (filepath.EvalSymlinks), so a symlink planted inside the
// attachments tree cannot be used to read files outside it (M-5, CWE-59). It
// returns errAttachmentPathEscape when the ref escapes the root, or the
// underlying error (e.g. os.ErrNotExist) when the path cannot be resolved.
func resolveAttachmentPath(root, storageRef string) (string, error) {
	cleanPath := filepath.Clean(storageRef)

	// Lexical guard: must live under the root and contain no parent-traversal
	// component. filepath.Clean has already collapsed any real ".." segments.
	if cleanPath != root && !strings.HasPrefix(cleanPath, root+string(os.PathSeparator)) {
		return "", errAttachmentPathEscape
	}
	if strings.Contains(cleanPath, "..") {
		return "", errAttachmentPathEscape
	}

	// Resolve symlinks in both the root and the target, then confirm the
	// resolved target is still contained in the resolved root. EvalSymlinks
	// also fails for a missing file, which the caller maps to a 404.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(cleanPath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errAttachmentPathEscape
	}
	return realPath, nil
}

// contentDispositionAttachment builds a safe Content-Disposition header value for
// an attachment download from a sender-controlled filename. Control characters
// (including CR/LF header-injection attempts) are stripped, and the name is
// encoded with mime.FormatMediaType (RFC 2045/2231 quoting), so a filename
// containing quotes or other metacharacters cannot break out of the header
// (issue #202). Falls back to a generic value if the name cannot be encoded.
func contentDispositionAttachment(filename string) string {
	filename = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, filename)
	if cd := mime.FormatMediaType("attachment", map[string]string{"filename": filename}); cd != "" {
		return cd
	}
	return "attachment"
}

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

	// Mailbox-scoped: the attachment's message must be in a mailbox the caller
	// may read: its own, or one it owns or has linked. An attachment it may not
	// read is answered as one that does not exist.
	var att models.Attachment
	var mailboxIDs []uint
	if err := h.db.First(&att, id).Error; err == nil {
		h.db.Model(&models.Message{}).Where("id = ?", att.MessageID).Pluck("mailbox_id", &mailboxIDs)
	}
	if len(mailboxIDs) != 1 {
		respond.Error(w, http.StatusNotFound, "not_found", "Attachment not found")
		return
	}
	if _, err := mayReach(h.db, claims, mailboxIDs[0], models.CapMailRead); err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Attachment not found")
		return
	}

	if att.StorageType != "filesystem" {
		respond.Error(w, http.StatusNotImplemented, "not_implemented", "S3 storage retrieval not yet supported")
		return
	}

	// Security: confine retrieval to the attachments tree, following symlinks
	// so a planted symlink cannot escape it (M-5, CWE-59).
	realPath, err := resolveAttachmentPath(attachmentsRoot, att.StorageRef)
	if err != nil {
		if errors.Is(err, errAttachmentPathEscape) {
			respond.Error(w, http.StatusForbidden, "forbidden", "Invalid storage path")
		} else {
			respond.Error(w, http.StatusNotFound, "not_found", "Attachment file not found on disk")
		}
		return
	}

	file, err := os.Open(realPath)
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
	w.Header().Set("Content-Disposition", contentDispositionAttachment(att.Filename))
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
	// Mailbox-scoped: the message must be in a mailbox the caller may read.
	_, err = mayReach(h.db, claims, msg.MailboxID, models.CapMailRead)
	owned := err == nil
	if !owned {
		respond.Error(w, http.StatusNotFound, "not_found", "Message not found")
		return
	}

	var attachments []models.Attachment
	h.db.Where("message_id = ?", messageID).Order("id ASC").Find(&attachments)

	respond.List(w, attachments, nil)
}
