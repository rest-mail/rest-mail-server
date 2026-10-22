package handlers

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

// UpdateDraft holds a draft's to and cc to the rule SaveDraft does: each entry
// is an address (#388). So is a new from, as UpdateDraftRequest's
// format: email says. The refusal comes before any lookup, so the handler
// needs no database to answer it.
func TestUpdateDraft_RefusesARecipientThatIsNotAnAddress(t *testing.T) {
	h := NewMessageHandler(nil, nil, nil, "", nil)
	for _, tc := range []struct{ name, body, field string }{
		{"to", `{"to":["not-an-email"]}`, "to"},
		{"cc", `{"cc":["not-an-email"]}`, "cc"},
		{"to after a good one", `{"to":["a@restmail.test","b@restmail.test\r\nBcc: c@evil.test"]}`, "to"},
		{"empty cc entry", `{"cc":[""]}`, "cc"},
		{"from", `{"from":"not-an-email"}`, "from"},
		{"empty from", `{"from":""}`, "from"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.UpdateDraft(rr, authzRequest(http.MethodPut, "/api/v1/messages/draft/1", 1,
				map[string]string{"id": "1"}, tc.body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", rr.Code, rr.Body.String())
			}
			want := "invalid recipient address in " + tc.field
			if tc.field == "from" {
				want = "invalid from address"
			}
			if !strings.Contains(rr.Body.String(), want) {
				t.Errorf("body %s does not say %q", rr.Body.String(), want)
			}
		})
	}
}

// A caller who may edit a draft may not give it a from address it could not
// have saved it with: the new from goes through mayActAs, as SaveDraft's does.
func TestUpdateDraft_FromNeedsWriteOnItsMailbox(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	h := NewMessageHandler(tx, nil, nil, "", nil)

	draft := models.Message{
		MailboxID: fx.attackerMailbox.ID,
		Folder:    "Drafts",
		Sender:    fx.attackerMailbox.Address,
		IsDraft:   true,
	}
	if err := tx.Create(&draft).Error; err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatUint(uint64(draft.ID), 10)
	update := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.UpdateDraft(rr, authzRequest(http.MethodPut, "/api/v1/messages/draft/"+id,
			fx.attackerMailbox.ID, map[string]string{"id": id}, body))
		return rr
	}
	stored := func() models.Message {
		var m models.Message
		if err := tx.First(&m, draft.ID).Error; err != nil {
			t.Fatal(err)
		}
		return m
	}

	if rr := update(`{"from":"` + fx.ownerMailbox.Address + `"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("foreign from: want 403, got %d: %s", rr.Code, rr.Body)
	}
	if rr := update(`{"from":"nobody@idor.test"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("from with no mailbox: want 403, got %d: %s", rr.Code, rr.Body)
	}
	if m := stored(); m.Sender != fx.attackerMailbox.Address || m.MailboxID != fx.attackerMailbox.ID {
		t.Fatalf("refused update changed the draft: %+v", m)
	}

	if rr := update(`{"from":"` + fx.attackerMailbox.Address + `","subject":"kept"}`); rr.Code != http.StatusOK {
		t.Fatalf("own from: want 200, got %d: %s", rr.Code, rr.Body)
	}
	if m := stored(); m.Sender != fx.attackerMailbox.Address || m.Subject != "kept" {
		t.Errorf("own from not applied: %+v", m)
	}
}
