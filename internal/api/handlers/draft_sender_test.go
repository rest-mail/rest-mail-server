package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// A draft's from may only be changed to an address the caller may act as:
// changing it to someone else's is refused and the draft is left as it was
// (#610).
func TestUpdateDraft_RefusesASenderTheCallerMayNotActAs(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedAuthzFixture(t, tx)
	draft := models.Message{
		MailboxID: fx.attackerMailbox.ID,
		Folder:    "Drafts",
		Sender:    fx.attackerMailbox.Address,
		Subject:   "hello",
		IsDraft:   true,
	}
	if err := tx.Create(&draft).Error; err != nil {
		t.Fatalf("seed draft: %v", err)
	}

	h := NewMessageHandler(tx, nil, nil, "", nil)
	params := map[string]string{"id": uintToStr(draft.ID)}
	update := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		composeRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPut, "/api/v1/messages/draft/"+uintToStr(draft.ID), fx.attackerMailbox.ID, params, body))
		return rr
	}

	rr := update(`{"from":"` + fx.ownerMailbox.Address + `","subject":"changed"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("foreign from: expected 403, got %d (%s)", rr.Code, rr.Body.String())
	}
	var got models.Message
	if err := tx.First(&got, draft.ID).Error; err != nil {
		t.Fatalf("reload draft: %v", err)
	}
	if got.Sender != fx.attackerMailbox.Address || got.Subject != "hello" {
		t.Fatalf("draft changed by a refused update: sender %q subject %q", got.Sender, got.Subject)
	}

	if rr := update(`{"from":"nobody@nowhere.test"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("unknown from: expected 403, got %d (%s)", rr.Code, rr.Body.String())
	}

	for _, tc := range []struct{ body, field string }{
		{`{"to":["not-an-email"]}`, "to"},
		{`{"cc":["b@restmail.test\r\nBcc: c@evil.test"]}`, "cc"},
	} {
		rr := update(tc.body)
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "invalid recipient address in "+tc.field) {
			t.Fatalf("bad %s: expected 422 naming %s, got %d (%s)", tc.field, tc.field, rr.Code, rr.Body.String())
		}
	}

	if rr := update(`{"from":"` + fx.attackerMailbox.Address + `","subject":"mine"}`); rr.Code != http.StatusOK {
		t.Fatalf("own from: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// Sending a draft checks its stored sender again: a draft whose from the
// caller may not send as is refused and kept (#610).
func TestSendDraft_RefusesAStoredSenderTheCallerMayNotSendAs(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedAuthzFixture(t, tx)
	draft := models.Message{
		MailboxID:    fx.attackerMailbox.ID,
		Folder:       "Drafts",
		Sender:       fx.ownerMailbox.Address,
		RecipientsTo: models.JSONB(`["someone@example.test"]`),
		RecipientsCc: models.JSONB(`[]`),
		Subject:      "spoof",
		IsDraft:      true,
	}
	if err := tx.Create(&draft).Error; err != nil {
		t.Fatalf("seed draft: %v", err)
	}

	h := NewMessageHandler(tx, nil, nil, "", nil)
	rr := httptest.NewRecorder()
	composeRoute(h)(rr, sendingAs(httptest.NewRequest(http.MethodPost,
		"/api/v1/messages/draft/"+uintToStr(draft.ID)+"/send", nil), fx.attackerMailbox.ID))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", rr.Code, rr.Body.String())
	}
	if err := tx.Where("id = ? AND is_draft = ?", draft.ID, true).First(&models.Message{}).Error; err != nil {
		t.Fatalf("refused send removed the draft: %v", err)
	}
}

// A draft is kept in the mailbox it is from: changing from to another mailbox the
// caller may write to moves it there, and the old mailbox no longer lists it (#1430).
func TestUpdateDraft_MovesTheDraftToTheMailboxOfItsNewSender(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedAuthzFixture(t, tx)
	var role models.Role
	if err := tx.Where("name = ?", models.RoleOwner).First(&role).Error; err != nil {
		t.Fatalf("find the owner role: %v", err)
	}
	if err := tx.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: fx.attackerMailbox.ID,
		RoleID: role.ID, Scope: grants.Mailbox(fx.ownerMailbox.Address), Mode: models.GrantActive,
		GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatalf("grant the second mailbox: %v", err)
	}
	draft := models.Message{MailboxID: fx.attackerMailbox.ID, Folder: "Drafts",
		Sender: fx.attackerMailbox.Address, Subject: "hello", IsDraft: true}
	if err := tx.Create(&draft).Error; err != nil {
		t.Fatalf("seed draft: %v", err)
	}

	h := NewMessageHandler(tx, nil, nil, "", nil)
	rr := httptest.NewRecorder()
	composeRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPut, "/api/v1/messages/draft/"+uintToStr(draft.ID), fx.attackerMailbox.ID,
		map[string]string{"id": uintToStr(draft.ID)}, `{"from":"`+fx.ownerMailbox.Address+`"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("moving the draft: %d (%s)", rr.Code, rr.Body.String())
	}

	var got models.Message
	if err := tx.First(&got, draft.ID).Error; err != nil {
		t.Fatalf("reload draft: %v", err)
	}
	if got.MailboxID != fx.ownerMailbox.ID || got.Sender != fx.ownerMailbox.Address {
		t.Fatalf("draft is in mailbox %d from %q, want %d from %q", got.MailboxID, got.Sender, fx.ownerMailbox.ID, fx.ownerMailbox.Address)
	}
	var left int64
	if err := tx.Model(&models.Message{}).Where("mailbox_id = ? AND is_draft = ?", fx.attackerMailbox.ID, true).Count(&left).Error; err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("the old mailbox still lists %d drafts", left)
	}
}
