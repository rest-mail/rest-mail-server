package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/exportsession"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"gorm.io/gorm"
)

// ExportSessionHandler opens and closes the audited export of an account's
// data (#2116 AC03b). The data itself is read through the ordinary read
// routes; what these record is that an export happened, who made it, of
// which account, in which format, how much it wrote and how it ended.
type ExportSessionHandler struct {
	db *gorm.DB
}

// NewExportSessionHandler is the handler over db.
func NewExportSessionHandler(db *gorm.DB) *ExportSessionHandler {
	return &ExportSessionHandler{db: db}
}

type openExportSessionRequest struct {
	ID   uint32                         `path:"id" minimum:"1" example:"1"`
	Body apitypes.OpenExportSessionBody `body:""`
}

type closeExportSessionRequest struct {
	ID      uint32                          `path:"id" minimum:"1" example:"1"`
	Session string                          `path:"session" pattern:"^[0-9a-f]{32}$" example:"00112233445566778899aabbccddeeff"`
	Body    apitypes.CloseExportSessionBody `body:""`
}

var errExportSessionNotFound = endpoint.Refuse(http.StatusNotFound, "export_session_not_found",
	"The export session does not exist, has closed, or was opened by another account for another mailbox")

// Register adds the operations that open and close an export.
func (h *ExportSessionHandler) Register(rs endpoint.Routes) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/exports",
		OperationID: "createExportSession", Summary: "Open an audited export of the account's data", Tags: []string{"Accounts"}, Status: http.StatusCreated,
		Description: "Records the start of an export of the account's data as an account.export audit entry naming the actor, the account, the data and its format, before anything is written. Each kind of data has one format: mail as maildir, contacts as vcard, calendar as icalendar and settings as json. Requires mail:export on the account. Every account holds it on its own mailbox and the owner role holds it on an owned account; a grant to read another account's mail does not confer it, and the admin and readonly roles do not hold it, so an administrator exporting someone else's account needs a role that names it. Close the export with closeExportSession; one not closed timeout_seconds (one day) after it started is closed by the server as aborted.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound},
		Security:    handlerChecked(models.CapMailExport, folderSchemes...)}, h.open)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/exports/{session}/close",
		OperationID: "closeExportSession", Summary: "Close an export and record how it ended", Tags: []string{"Accounts"},
		Description: "Records the end of the export as an account.export audit entry: completed or aborted, the rows and bytes written as the exporter reports them, and any folders it was asked to leave out. The summary is returned. Only the account that opened the export may close it, and only once; a session that does not exist, has closed or belongs to another opener is not found. Requires mail:export on the account.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound},
		Security:    handlerChecked(models.CapMailExport, folderSchemes...)}, h.close)
}

func exportRefusal(err error) error {
	switch {
	case errors.Is(err, errNoAccess):
		return errAccountNotFound
	case errors.Is(err, errNotAllowed):
		return endpoint.Refuse(http.StatusForbidden, "forbidden", "Insufficient permissions")
	}
	return err
}

func (h *ExportSessionHandler) open(r *http.Request, in openExportSessionRequest) (apitypes.ExportSession, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return apitypes.ExportSession{}, errNotSignedIn
	}
	if exportsession.Formats[in.Body.Data] != in.Body.Format {
		return apitypes.ExportSession{}, endpoint.Invalid(map[string]string{"format": "must be " + exportsession.Formats[in.Body.Data] + " for " + in.Body.Data})
	}
	db := h.db.WithContext(r.Context())
	if _, err := resolveAccountMailbox(db, uint(in.ID), claims, models.CapMailExport); err != nil {
		return apitypes.ExportSession{}, exportRefusal(err)
	}
	now := time.Now()
	s, err := exportsession.Open(db, middleware.NewAuditEntry(exportsession.Action, r, now), uint(in.ID), claims.AccountID,
		in.Body.Data, in.Body.Format, now)
	if err != nil {
		return apitypes.ExportSession{}, err
	}
	return apitypes.ExportSession{ID: s.ID, AccountID: s.MailboxID, Data: s.Data, Format: s.Format, StartedAt: s.StartedAt,
		TimeoutSeconds: int(exportsession.Timeout / time.Second)}, nil
}

func (h *ExportSessionHandler) close(r *http.Request, in closeExportSessionRequest) (apitypes.ExportSessionSummary, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return apitypes.ExportSessionSummary{}, errNotSignedIn
	}
	// Each name goes into the audit log, which keeps it for good: a folder
	// name is at most rmail.MaxFolderNameLen bytes.
	for _, folder := range in.Body.SkippedFolders {
		if folder == "" || len(folder) > rmail.MaxFolderNameLen {
			return apitypes.ExportSessionSummary{}, endpoint.Invalid(map[string]string{"skipped_folders": "each must be a folder name of 1 to 255 bytes"})
		}
	}
	db := h.db.WithContext(r.Context())
	if _, err := resolveAccountMailbox(db, uint(in.ID), claims, models.CapMailExport); err != nil {
		return apitypes.ExportSessionSummary{}, exportRefusal(err)
	}
	now := time.Now()
	summary, err := exportsession.Close(db, in.Session, uint(in.ID), claims.AccountID,
		middleware.NewAuditEntry(exportsession.Action, r, now), in.Body, now)
	if errors.Is(err, exportsession.ErrNotFound) {
		return summary, errExportSessionNotFound
	}
	return summary, err
}
