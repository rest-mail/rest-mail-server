package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// TestDeliverMessage_HonorsFolderFlagsAndInternalDate is the DB-backed red-green
// regression for issue #190. The IMAP gateway's COPY/APPEND now deliver a message
// directly into its destination folder carrying its flags and INTERNALDATE, in a
// single call. The delivery endpoint must honor those: create the row in the given
// folder, with the given flags, stamped with the supplied received_at — not
// default it to INBOX / unread / now(). The pre-fix handler had no folder/flag/
// received_at fields, so the JSON keys were silently dropped.
func TestDeliverMessage_HonorsFolderFlagsAndInternalDate(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	// The delivery path also bumps quota_usage; migrate it so the transaction's
	// quota update matches a real table (0 rows for a fresh mailbox is not an error).
	if err := db.AutoMigrate(&models.QuotaUsage{}); err != nil {
		t.Skipf("deliver DB test skipped: migrate quota_usage failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedMsgDeleteFixture(t, tx, 0)

	// A deliberately-past internal date so a now() default is unmistakably wrong.
	want := time.Date(2021, 1, 2, 3, 4, 5, 0, time.UTC)
	body := fmt.Sprintf(`{
		"mailbox_id": %d,
		"sender": "someone@remote.test",
		"subject": "Copied message",
		"body_text": "hello",
		"folder": "Archive",
		"is_read": true,
		"is_flagged": true,
		"is_starred": true,
		"received_at": "2021-01-02T03:04:05Z"
	}`, fx.mailbox.ID)

	h := NewMessageHandler(tx, nil, nil, "", nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(body))
	h.DeliverMessage(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("deliver: expected 201, got %d (%s)", rr.Code, rr.Body.String())
	}

	var msg models.Message
	if err := tx.Where("mailbox_id = ? AND subject = ?", fx.mailbox.ID, "Copied message").
		First(&msg).Error; err != nil {
		t.Fatalf("reload delivered message: %v", err)
	}

	if msg.Folder != "Archive" {
		t.Errorf("message created in folder %q, want Archive (folder was dropped)", msg.Folder)
	}
	if !msg.IsRead {
		t.Error("delivered message lost the \\Seen flag (is_read=false)")
	}
	if !msg.IsFlagged {
		t.Error("delivered message lost the \\Flagged flag (is_flagged=false)")
	}
	if !msg.IsStarred {
		t.Error("delivered message lost the mirrored star (is_starred=false)")
	}
	if msg.ReceivedAt.Unix() != want.Unix() {
		t.Errorf("delivered message INTERNALDATE = %v, want %v (received_at was dropped, defaulted to now())",
			msg.ReceivedAt.UTC(), want)
	}
}
