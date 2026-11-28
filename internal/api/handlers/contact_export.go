package handlers

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/google/uuid"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/vcard"
	"gorm.io/gorm"
)

var contactExportHeaders = map[string]oapifly.ResponseHeader{
	"Content-Disposition": {Description: "Download as contacts.vcf.", Required: true},
	"Cache-Control":       {Description: "Prevents storage of the exported address book.", Required: true},
}

type contactExportRequest struct {
	ID uint32 `path:"id" minimum:"1" example:"1"`
}

func (h *ContactHandler) exportContacts(r *http.Request, in contactExportRequest) (endpoint.Stream, error) {
	mailbox, err := h.resolveContactAccount(r, in.ID, models.CapMailRead)
	if err != nil {
		return endpoint.Stream{}, err
	}
	ctx := r.Context()
	db := h.db.WithContext(ctx)
	rows, err := db.Model(&models.Contact{}).Where("mailbox_id = ?", mailbox.ID).Order("email").Rows()
	if err != nil {
		return endpoint.Stream{}, fmt.Errorf("query contacts for export: %w", err)
	}
	return endpoint.Stream{
		ContentType: contactVCardMedia + "; charset=utf-8",
		Header: http.Header{
			"Content-Disposition": {contentDispositionAttachment("contacts.vcf")},
			"Cache-Control":       {"no-store"},
		},
		Done: func() { _ = rows.Close() },
		Send: func(w io.Writer, _ func()) {
			if err := writeContactRows(w, db, rows, mailbox.Address); err != nil {
				if ctx.Err() == nil {
					slog.ErrorContext(ctx, "contact export interrupted", "account_id", mailbox.ID, "error", err)
				}
				// Headers have already been sent. A partial address book must fail the download.
				panic(http.ErrAbortHandler)
			}
		},
	}, nil
}

func writeContactRows(w io.Writer, db *gorm.DB, rows *sql.Rows, mailboxAddress string) error {
	for rows.Next() {
		var contact vcard.Contact
		if err := db.ScanRows(rows, &contact); err != nil {
			return fmt.Errorf("read contact for export: %w", err)
		}
		// Derive a stable UID without depending on the request's Host header.
		uid := uuid.NewSHA1(uuid.NameSpaceURL, fmt.Appendf(nil, "mailto:%s?restmail-contact=%d", mailboxAddress, contact.ID)).URN()
		if err := vcard.WriteContact(w, uid, contact); err != nil {
			return err
		}
	}
	return rows.Err()
}
