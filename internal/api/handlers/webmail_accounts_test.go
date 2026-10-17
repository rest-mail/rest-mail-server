package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/db/models"
)

// Deleting a webmail account takes its linked accounts with it (#358). A link
// left behind would keep its mailbox from ever being linked to another account,
// because a mailbox may be linked to at most one (OSI-21).
func TestWebmailAccountDelete_RemovesItsLinks(t *testing.T) {
	gdb := openAuthzTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	domain := models.Domain{Name: fmt.Sprintf("unlink-%d.test", time.Now().UnixNano())}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	mk := func(local string) models.Mailbox {
		t.Helper()
		mb := models.Mailbox{
			DomainID:  domain.ID,
			LocalPart: local,
			Address:   local + "@" + domain.Name,
			Password:  "x",
		}
		if err := tx.Create(&mb).Error; err != nil {
			t.Fatalf("create mailbox %s: %v", local, err)
		}
		return mb
	}
	primary, shared, other := mk("primary"), mk("shared"), mk("other")

	account := models.WebmailAccount{PrimaryMailboxID: primary.ID}
	if err := tx.Create(&account).Error; err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := tx.Create(&models.LinkedAccount{WebmailAccountID: account.ID, MailboxID: shared.ID}).Error; err != nil {
		t.Fatalf("link mailbox: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/admin/webmail-accounts/%d", account.ID), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", fmt.Sprint(account.ID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	NewWebmailAccountHandler(tx).Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 (body %s)", rr.Code, rr.Body.String())
	}

	var left int64
	if err := tx.Model(&models.LinkedAccount{}).Where("webmail_account_id = ?", account.ID).Count(&left).Error; err != nil {
		t.Fatalf("count links: %v", err)
	}
	if left != 0 {
		t.Fatalf("deleting the account left %d linked account row(s) behind", left)
	}

	// The mailbox it had linked is free to be linked by another account.
	otherAccount := models.WebmailAccount{PrimaryMailboxID: other.ID}
	if err := tx.Create(&otherAccount).Error; err != nil {
		t.Fatalf("create other account: %v", err)
	}
	if err := tx.Create(&models.LinkedAccount{WebmailAccountID: otherAccount.ID, MailboxID: shared.ID}).Error; err != nil {
		t.Fatalf("relink the mailbox to another account: %v", err)
	}
}
