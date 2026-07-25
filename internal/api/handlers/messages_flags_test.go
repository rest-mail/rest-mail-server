package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

// TestUpdateMessage_DraftFlagPersists is the red-green regression for issue #197:
// an IMAP STORE/APPEND of \Draft reaches the REST API as is_draft. The handler
// must persist it — the buggy version had no is_draft field on its update
// struct, so the key was silently dropped, 200 was returned, and the flag was
// lost on the next SELECT (while PERMANENTFLAGS still advertised \Draft).
func TestUpdateMessage_DraftFlagPersists(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedMsgDeleteFixture(t, tx, 0)
	msg := models.Message{
		MailboxID: fx.mailbox.ID,
		Folder:    "Drafts",
		Sender:    "owner@delq.test",
	}
	if err := tx.Create(&msg).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}

	h := NewMessageHandler(tx, nil, nil, "", nil)
	idStr := uintToStr(msg.ID)
	rr := httptest.NewRecorder()
	req := authzRequest(http.MethodPatch,
		"/api/v1/messages/"+idStr,
		fx.account.ID, map[string]string{"id": idStr}, `{"is_draft":true}`)
	h.UpdateMessage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	var reloaded models.Message
	if err := tx.First(&reloaded, msg.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reloaded.IsDraft {
		t.Fatal("is_draft must persist through the REST API (IMAP \\Draft round-trip); it was silently discarded")
	}
}

// TestUpdateMessage_DraftFlagClears confirms the draft flag also round-trips
// off — clearing \Draft via the API must set is_draft=false, not be ignored.
func TestUpdateMessage_DraftFlagClears(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedMsgDeleteFixture(t, tx, 0)
	msg := models.Message{
		MailboxID: fx.mailbox.ID,
		Folder:    "Drafts",
		Sender:    "owner@delq.test",
		IsDraft:   true,
	}
	if err := tx.Create(&msg).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}

	h := NewMessageHandler(tx, nil, nil, "", nil)
	idStr := uintToStr(msg.ID)
	rr := httptest.NewRecorder()
	req := authzRequest(http.MethodPatch,
		"/api/v1/messages/"+idStr,
		fx.account.ID, map[string]string{"id": idStr}, `{"is_draft":false}`)
	h.UpdateMessage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	var reloaded models.Message
	if err := tx.First(&reloaded, msg.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.IsDraft {
		t.Fatal("clearing is_draft via the REST API must persist (is_draft=false); the key was discarded")
	}
}
