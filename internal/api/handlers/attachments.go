package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"mime"
	"net/http"
	"os"
	"strings"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// attachmentsRoot is the base directory filesystem-backed attachments are
// written under (see internal/pipeline/filters/extract_attachments.go).
// Retrieval is confined to this tree.
const attachmentsRoot = attachmentstore.FilesystemRoot

// errAttachmentPathEscape is returned when a stored attachment path resolves
// outside the attachments root (directly, via traversal, or via a symlink).
var errAttachmentPathEscape = attachmentstore.ErrPathEscape

// resolveAttachmentPath validates storageRef and returns the real, canonical
// file path to serve. It confines retrieval to root both lexically and after
// resolving symlinks (filepath.EvalSymlinks), so a symlink planted inside the
// attachments tree cannot be used to read files outside it (M-5, CWE-59). It
// returns errAttachmentPathEscape when the ref escapes the root, or the
// underlying error (e.g. os.ErrNotExist) when the path cannot be resolved.
func resolveAttachmentPath(root, storageRef string) (string, error) {
	return attachmentstore.ResolveFilesystemPath(root, storageRef)
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
	s3 *attachmentstore.S3
}

// NewAttachmentHandler creates a new AttachmentHandler.
func NewAttachmentHandler(db *gorm.DB, s3 *attachmentstore.S3) *AttachmentHandler {
	return &AttachmentHandler{db: db, s3: s3}
}

// forRequest gives the handler's SQL work the HTTP request's budget and
// cancellation without changing the shared handler. The object store already
// takes the request's context.
func (h *AttachmentHandler) forRequest(r *http.Request) *AttachmentHandler {
	if h.db == nil {
		return h
	}
	scoped := *h
	scoped.db = h.db.WithContext(r.Context())
	return &scoped
}

// attachmentPath names an attachment by the path's {id}.
type attachmentPath struct {
	ID                uint32 `path:"id" minimum:"1" example:"1"`
	Range             string `header:"Range"`
	IfRange           string `header:"If-Range"`
	IfMatch           string `header:"If-Match"`
	IfNoneMatch       string `header:"If-None-Match"`
	IfModifiedSince   string `header:"If-Modified-Since"`
	IfUnmodifiedSince string `header:"If-Unmodified-Since"`
}

// getAttachment streams an attachment file from storage, with Range and conditional requests
// answered as http.ServeContent answers them.
// GET /api/v1/attachments/{id}
func (h *AttachmentHandler) getAttachment(r *http.Request, in attachmentPath) (endpoint.Served, error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	if claims == nil {
		return endpoint.Served{}, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}

	// Mailbox-scoped: the attachment's message must be in a mailbox the caller
	// may read: its own, or one it owns or has linked. An attachment it may not
	// read is answered as one that does not exist.
	notFound := endpoint.Refuse(http.StatusNotFound, "not_found", "Attachment not found")
	var att models.Attachment
	var mailboxIDs []uint
	if err := h.db.First(&att, in.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint.Served{}, notFound
		}
		return endpoint.Served{}, fmt.Errorf("load attachment: %w", err)
	}
	if err := h.db.Model(&models.Message{}).Where("id = ?", att.MessageID).Pluck("mailbox_id", &mailboxIDs).Error; err != nil {
		return endpoint.Served{}, fmt.Errorf("load attachment mailbox: %w", err)
	}
	if len(mailboxIDs) != 1 {
		return endpoint.Served{}, notFound
	}
	if _, err := mayReach(h.db, claims, mailboxIDs[0], models.CapMailRead); err != nil {
		if errors.Is(err, errNoAccess) {
			return endpoint.Served{}, notFound
		}
		return endpoint.Served{}, fmt.Errorf("authorize attachment download: %w", err)
	}

	// Always bytes to save, never the type the sender claimed: an HTML or SVG
	// attachment must not render as a page on the API's origin, even in a
	// client that ignores Content-Disposition. Its claimed type is in the
	// attachment's metadata (listAttachments' content_type).
	served := endpoint.Served{
		ContentType: "application/octet-stream",
		Header:      http.Header{"Content-Disposition": {contentDispositionAttachment(att.Filename)}},
		Name:        att.Filename,
	}

	// Older extract_attachments configurations could label a disk path as s3.
	// Keep those readable through the confined legacy path until migration.
	legacyS3Path := att.StorageType == "s3" && strings.HasPrefix(att.StorageRef, attachmentsRoot+"/")
	if att.StorageType == "s3" && !legacyS3Path {
		return h.s3Attachment(r, &att, served)
	}
	if att.StorageType != "filesystem" && !legacyS3Path {
		return endpoint.Served{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Unknown attachment storage type")
	}

	// Security: confine retrieval to the attachments tree, following symlinks
	// so a planted symlink cannot escape it (M-5, CWE-59).
	realPath, err := resolveAttachmentPath(attachmentsRoot, att.StorageRef)
	if err != nil {
		if errors.Is(err, errAttachmentPathEscape) {
			return endpoint.Served{}, endpoint.Refuse(http.StatusForbidden, "forbidden", "Invalid storage path")
		}
		return endpoint.Served{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Attachment file not found on disk")
	}

	file, err := os.Open(realPath)
	if err != nil {
		return endpoint.Served{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Attachment file not found on disk")
	}
	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return endpoint.Served{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to stat attachment")
	}
	served.Modified = stat.ModTime()
	served.Content = file
	return served, nil
}

// s3Attachment is served with the object's bytes streaming from the store, and its ETag.
func (h *AttachmentHandler) s3Attachment(r *http.Request, att *models.Attachment, served endpoint.Served) (endpoint.Served, error) {
	if h.s3 == nil {
		return endpoint.Served{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Object storage is not configured")
	}
	file, err := h.s3.Open(r.Context(), att.StorageRef)
	if errors.Is(err, os.ErrNotExist) {
		return endpoint.Served{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Attachment object not found")
	}
	if err != nil {
		slog.Error("opening attachment object failed", "attachment_id", att.ID, "error", err)
		return endpoint.Served{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to open attachment object")
	}
	if file.ETag != "" {
		served.Header.Set("ETag", file.ETag)
	}
	served.Modified = file.Modified
	served.Content = file
	return served, nil
}

// attachmentDownloadHeaders describes the ordinary whole-file response. A stored
// ETag or modification time is optional because not every storage object has one.
func attachmentDownloadHeaders() map[string]oapifly.ResponseHeader {
	return map[string]oapifly.ResponseHeader{
		"Content-Disposition": {Description: "Download as an attachment, with a safely encoded filename.", Required: true},
		"Content-Length":      {Description: "The number of bytes in the complete attachment.", Required: true, Type: oapifly.TypeOf[int64]()},
		"Accept-Ranges":       {Description: "Byte ranges are supported.", Required: true},
		"ETag":                {Description: "The stored object's entity tag, when available."},
		"Last-Modified":       {Description: "The stored object's modification time, when available, as an HTTP date."},
	}
}

// attachmentConditionalResponses describes the outcomes of the same ServeContent
// writer used for filesystem and object-storage downloads. Headers common to a
// status are shared across its media types; Content-Range is only required by a
// single range, so it is optional on the combined single/multipart 206 response.
func attachmentConditionalResponses() []oapifly.EndpointResponse {
	download := attachmentDownloadHeaders()
	ranges := maps.Clone(download)
	ranges["Content-Length"] = oapifly.ResponseHeader{Description: "The number of bytes in this partial or multipart response.", Required: true, Type: oapifly.TypeOf[int64]()}
	ranges["Content-Range"] = oapifly.ResponseHeader{Description: "The selected byte range and complete length for a single-range response; multipart responses put it on each part."}
	conditional := map[string]oapifly.ResponseHeader{
		"Content-Disposition": download["Content-Disposition"],
		"ETag":                download["ETag"], "Last-Modified": download["Last-Modified"],
	}
	precondition := maps.Clone(conditional)
	precondition["Content-Type"] = oapifly.ResponseHeader{Description: "The attachment media type may remain on a bodyless precondition failure."}
	precondition["Content-Length"] = oapifly.ResponseHeader{Description: "Zero when emitted for a bodyless precondition failure.", Type: oapifly.TypeOf[int64]()}
	rangeError := maps.Clone(conditional)
	rangeError["Content-Range"] = oapifly.ResponseHeader{Description: "bytes */length for a non-overlapping range; omitted for a malformed range."}
	rangeError["Content-Length"] = oapifly.ResponseHeader{Description: "The error body's byte length, when emitted.", Type: oapifly.TypeOf[int64]()}
	streamError := maps.Clone(conditional)
	disposition := streamError["Content-Disposition"]
	disposition.Required = false // A handler failure happens before the writer sets it.
	streamError["Content-Disposition"] = disposition
	streamError["Content-Length"] = rangeError["Content-Length"]
	return []oapifly.EndpointResponse{
		{Status: http.StatusPartialContent, Description: "Selected bytes, or multiple byte ranges with a Content-Range on each binary part.", Type: oapifly.TypeOf[[]byte](), MediaType: "application/octet-stream", Headers: ranges},
		{Status: http.StatusPartialContent, Description: "Selected bytes, or multiple byte ranges with a Content-Range on each binary part.", Type: oapifly.TypeOf[[]byte](), MediaType: "multipart/byteranges", Headers: ranges},
		{Status: http.StatusNotModified, Description: "The conditional request's cached representation is current; no response body.", Headers: conditional},
		{Status: http.StatusPreconditionFailed, Description: "An If-Match or If-Unmodified-Since precondition failed; no response body.", Headers: precondition},
		{Status: http.StatusRequestedRangeNotSatisfiable, Description: "The byte range is malformed or does not overlap the attachment.", Type: oapifly.TypeOf[string](), MediaType: "text/plain", Headers: rangeError},
		{Status: http.StatusInternalServerError, Description: "A handler dependency failed, or the stored content could not be served.", Type: oapifly.TypeOf[respond.ErrorResponse](), Headers: streamError},
		{Status: http.StatusInternalServerError, Description: "A handler dependency failed, or the stored content could not be served.", Type: oapifly.TypeOf[string](), MediaType: "text/plain", Headers: streamError},
	}
}

// Register mounts listing a message's attachments and downloading one. Their routes take what
// the mail routes take; an attachment, or a message, in an account the session cannot read is
// 404, as one that is not there.
func (h *AttachmentHandler) Register(rs endpoint.Routes) {
	security := handlerChecked(models.CapMailRead, "CookieAuth", "BearerAuth", "APIKeyAuth", "BasicAuth")
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/messages/{id}/attachments",
		OperationID: "listAttachments", Summary: "List a message's attachments", Tags: []string{"Attachments"},
		Errors: []int{http.StatusNotFound}, Security: security}, h.listAttachments)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/attachments/{id}",
		OperationID: "getAttachment", Summary: "Download an attachment", Tags: []string{"Attachments"},
		Description: "The attachment's bytes are application/octet-stream; listAttachments' content_type is the sender's declared type. " +
			"Range requests return a binary part or multipart/byteranges. Conditional requests can return bodyless 304 or 412; malformed or non-overlapping ranges return plain-text 416.",
		MediaType: "application/octet-stream", Headers: attachmentDownloadHeaders(), Responses: attachmentConditionalResponses(),
		Errors: []int{http.StatusForbidden, http.StatusNotFound}, Security: security}, h.getAttachment)
}

// listAttachments returns attachment metadata for a message.
// GET /api/v1/messages/{id}/attachments
func (h *AttachmentHandler) listAttachments(r *http.Request, in messagePath) (endpoint.List[MessageAttachment], error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	if claims == nil {
		return endpoint.List[MessageAttachment]{}, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	notFound := endpoint.Refuse(http.StatusNotFound, "not_found", "Message not found")

	var msg models.Message
	if err := h.db.First(&msg, in.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint.List[MessageAttachment]{}, notFound
		}
		return endpoint.List[MessageAttachment]{}, fmt.Errorf("read attachment message: %w", err)
	}
	// Mailbox-scoped: the message must be in a mailbox the caller may read.
	if _, err := mayReach(h.db, claims, msg.MailboxID, models.CapMailRead); err != nil {
		if errors.Is(err, errNoAccess) {
			return endpoint.List[MessageAttachment]{}, notFound
		}
		return endpoint.List[MessageAttachment]{}, fmt.Errorf("authorize attachment list: %w", err)
	}

	var rows []models.Attachment
	if err := h.db.Where("message_id = ?", in.ID).Order("id ASC").Find(&rows).Error; err != nil {
		return endpoint.List[MessageAttachment]{}, fmt.Errorf("list attachments: %w", err)
	}

	out := make([]MessageAttachment, len(rows))
	for i, row := range rows {
		out[i] = attachmentOf(row)
	}
	return endpoint.List[MessageAttachment]{Data: out}, nil
}
