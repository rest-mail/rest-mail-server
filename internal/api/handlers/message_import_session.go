package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/importsession"
)

type openImportSessionRequest struct {
	ID   uint32                         `path:"id" minimum:"1" example:"1"`
	Body apitypes.OpenImportSessionBody `body:""`
}

type closeImportSessionRequest struct {
	ID      uint32 `path:"id" minimum:"1" example:"1"`
	Session string `path:"session" pattern:"^[0-9a-f]{32}$" example:"00112233445566778899aabbccddeeff"`
}

// registerSessions adds the operations that open and close an import, so a run of
// imports is audited as one summary (#1498).
func (h *MessageImportHandler) registerSessions(rs endpoint.Routes) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/messages/import-sessions",
		OperationID: "createImportSession", Summary: "Open an import session", Tags: []string{"Messages"}, Status: http.StatusCreated,
		Description: "Opens an import of many messages into the account, so it is audited as one summary instead of one entry per message. Send the returned id as import_session with each importMessage call, then close it with closeImportSession. Requires mail:import on the account. A session with no import or close for idle_timeout_seconds (one hour) is closed by the server and its summary written. The IMAP gateway opens one at a connection's first APPEND or COPY and closes it at LOGOUT or disconnect, naming the client from its ID command.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound},
		Security:    handlerChecked(models.CapMailImport, folderSchemes...)}, h.openSession)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/accounts/{id}/messages/import-sessions/{session}",
		OperationID: "closeImportSession", Summary: "Close an import session and write its summary", Tags: []string{"Messages"},
		Description: "Closes the import session and writes its summary as one messages.import audit entry: account, client, start and end, and per folder the messages imported, deduplicated and refused with the bytes imported. The summary is returned. Only the account that opened the session may close it; a session that does not exist, has closed or belongs to another opener is not found.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound},
		Security:    handlerChecked(models.CapMailImport, folderSchemes...)}, h.closeSession)
}

func (h *MessageImportHandler) openSession(r *http.Request, in openImportSessionRequest) (apitypes.ImportSession, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return apitypes.ImportSession{}, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	if _, err := resolveAccountMailbox(h.db, uint(in.ID), claims, models.CapMailImport); err != nil {
		return apitypes.ImportSession{}, importRefusal(err)
	}
	opener := middleware.NewAuditEntry(importsession.Action, r, time.Now())
	s, err := importsession.Open(h.db.WithContext(r.Context()), opener, uint(in.ID), claims.AccountID,
		importedText(in.Body.ClientName), importedText(in.Body.ClientVersion), time.Now())
	if err != nil {
		return apitypes.ImportSession{}, err
	}
	return apitypes.ImportSession{ID: s.ID, AccountID: s.MailboxID, ClientName: s.ClientName, ClientVersion: s.ClientVersion,
		StartedAt: s.StartedAt, IdleTimeoutSeconds: int(importsession.IdleTimeout / time.Second)}, nil
}

func (h *MessageImportHandler) closeSession(r *http.Request, in closeImportSessionRequest) (apitypes.ImportSessionSummary, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return apitypes.ImportSessionSummary{}, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	if _, err := resolveAccountMailbox(h.db, uint(in.ID), claims, models.CapMailImport); err != nil {
		return apitypes.ImportSessionSummary{}, importRefusal(err)
	}
	summary, err := importsession.Close(h.db.WithContext(r.Context()), in.Session, uint(in.ID), claims.AccountID,
		middleware.NewAuditEntry(importsession.Action, r, time.Now()), time.Now())
	if errors.Is(err, importsession.ErrNotFound) {
		return summary, errImportSessionNotFound
	}
	return summary, err
}
