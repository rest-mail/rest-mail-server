package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
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
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid recipient address in "+tc.field) {
			t.Fatalf("bad %s: expected 400 naming %s, got %d (%s)", tc.field, tc.field, rr.Code, rr.Body.String())
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
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", rr.Code, rr.Body.String())
	}
	if err := tx.Where("id = ? AND is_draft = ?", draft.ID, true).First(&models.Message{}).Error; err != nil {
		t.Fatalf("refused send removed the draft: %v", err)
	}
}
