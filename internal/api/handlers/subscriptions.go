package handlers

import (
	"fmt"
	"net/http"
	"slices"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"gorm.io/gorm/clause"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// subscriptionRule is what the subscription routes say of the list they keep (#1675).
const subscriptionRule = "The account's IMAP subscription list (RFC 9051 §6.3.7, RFC 3501 §6.3.6): the names LSUB and " +
	"LIST (SUBSCRIBED) return. A name need not be a folder: deleting or renaming a folder leaves its subscription, " +
	"as RFC 9051 §§6.3.6-6.3.7 have it. A new account starts subscribed to INBOX, Sent, Drafts, Trash, Spam and Archive."

// registerSubscriptions mounts the account's subscription list beside its folders.
func (h *MessageHandler) registerSubscriptions(rs endpoint.Routes, reads, writes []oapifly.SecurityRequirement) {
	tags := []string{"Folders"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/subscriptions",
		OperationID: "listSubscriptions", Summary: "List the account's subscribed folder names", Tags: tags,
		Description: subscriptionRule + " Sorted by name.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound}, Security: reads}, h.listSubscriptions)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/accounts/{id}/subscriptions/{folder}",
		OperationID: "subscribeFolder", Summary: "Subscribe to a folder name", Tags: tags,
		Description: subscriptionRule + " Subscribing a name already subscribed answers the same. " + folderNameRule,
		Errors:      []int{http.StatusUnprocessableEntity, http.StatusForbidden, http.StatusNotFound}, Security: writes}, h.subscribeFolder)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/accounts/{id}/subscriptions/{folder}",
		OperationID: "unsubscribeFolder", Summary: "Unsubscribe from a folder name", Tags: tags, Status: http.StatusNoContent,
		Description: subscriptionRule + " Unsubscribing a name that is not subscribed answers the same.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound}, Security: writes}, h.unsubscribeFolder)
}

// listSubscriptions returns the account's subscribed names.
func (h *MessageHandler) listSubscriptions(r *http.Request, in accountPath) (endpoint.List[FolderName], error) {
	h = h.forRequest(r)
	mailboxID, err := h.accountMailbox(r, in.ID, models.CapMailRead)
	if err != nil {
		return endpoint.List[FolderName]{}, err
	}
	var rows []models.FolderSubscription
	if err := h.db.WithContext(r.Context()).Where("mailbox_id = ?", mailboxID).Find(&rows).Error; err != nil {
		return endpoint.List[FolderName]{}, fmt.Errorf("list subscriptions: %w", err)
	}
	subscribed := make(map[string]bool, len(rmail.StandardFolders)+len(rows))
	for _, name := range rmail.StandardFolders {
		subscribed[name] = true
	}
	for _, row := range rows {
		subscribed[row.Folder] = row.Subscribed
	}
	names := make([]string, 0, len(subscribed))
	for name, on := range subscribed {
		if on {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	out := make([]FolderName, 0, len(names))
	for _, name := range names {
		out = append(out, FolderName{Name: name})
	}
	return endpoint.List[FolderName]{Data: out}, nil
}

// subscribeFolder adds a name to the account's subscription list.
func (h *MessageHandler) subscribeFolder(r *http.Request, in folderPath) (FolderName, error) {
	h = h.forRequest(r)
	mailboxID, err := h.accountMailbox(r, in.ID, models.CapMailWrite)
	if err != nil {
		return FolderName{}, err
	}
	if problem := rmail.FolderNameProblem(in.Folder); problem != "" {
		return FolderName{}, endpoint.Invalid(map[string]string{"folder": problem})
	}
	name := rmail.CanonicalFolder(in.Folder)
	if err := h.setSubscription(r, mailboxID, name, true); err != nil {
		return FolderName{}, fmt.Errorf("subscribe folder: %w", err)
	}
	return FolderName{Name: name}, nil
}

// unsubscribeFolder removes a name from the account's subscription list.
func (h *MessageHandler) unsubscribeFolder(r *http.Request, in folderPath) (endpoint.Empty, error) {
	h = h.forRequest(r)
	mailboxID, err := h.accountMailbox(r, in.ID, models.CapMailWrite)
	if err != nil {
		return endpoint.Empty{}, err
	}
	if err := h.setSubscription(r, mailboxID, rmail.CanonicalFolder(in.Folder), false); err != nil {
		return endpoint.Empty{}, fmt.Errorf("unsubscribe folder: %w", err)
	}
	return endpoint.Empty{}, nil
}

// setSubscription records the account's choice for name. A standard folder is
// subscribed with no row, so its row is kept either way; any other name is on
// the list only while its row says so, so unsubscribing it removes the row.
func (h *MessageHandler) setSubscription(r *http.Request, mailboxID uint, name string, on bool) error {
	db := h.db.WithContext(r.Context())
	if !on && !rmail.IsStandardFolder(name) {
		return db.Where("mailbox_id = ? AND folder = ?", mailboxID, name).Delete(&models.FolderSubscription{}).Error
	}
	return db.Omit(clause.Associations).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "mailbox_id"}, {Name: "folder"}},
		DoUpdates: clause.AssignmentColumns([]string{"subscribed", "updated_at"}),
	}).Create(&models.FolderSubscription{MailboxID: mailboxID, Folder: name, Subscribed: on}).Error
}
