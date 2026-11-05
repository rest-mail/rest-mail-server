package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// threadMembers asks the thread route for a thread, as a client following getMessage's link does.
func threadMembers(t *testing.T, handler *MessageHandler, mailbox models.Mailbox, thread string) []uint {
	t.Helper()
	recorder := httptest.NewRecorder()
	r := authzRequest(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/threads/%s", mailbox.ID, thread), mailbox.ID, nil, "")
	messageRoutes(handler).ServeHTTP(recorder, r)
	if recorder.Code != http.StatusOK {
		t.Fatalf("thread %q answered %d %s", thread, recorder.Code, recorder.Body)
	}
	var page struct {
		Data []struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	ids := make([]uint, 0, len(page.Data))
	for _, row := range page.Data {
		ids = append(ids, row.ID)
	}
	return ids
}

// A released message keeps the Message-ID and thread its source states. Without them getMessage
// links to /threads/ with nothing after it, and a reply it holds drops out of its conversation.
func TestReleasedMessageKeepsItsThread(t *testing.T) {
	for name, raw := range map[string]string{
		"a reply":          "Message-ID: <reply@example.test>\r\nIn-Reply-To: <parent@example.test>\r\nReferences: <parent@example.test>\r\nSubject: Held message\r\n\r\nhéllo",
		"a new message":    "Message-ID: <fresh@example.test>\r\nSubject: Held message\r\n\r\nhéllo",
		"no message id":    "Subject: Held message\r\n\r\nhéllo",
		"empty message id": "Message-ID: <>\r\nSubject: Held message\r\n\r\nhéllo",
		"oversized ids":    "Message-ID: <" + strings.Repeat("a", 1000) + "@example.test>\r\nIn-Reply-To: <" + strings.Repeat("b", 1000) + "@example.test>\r\nReferences: <" + strings.Repeat("c", 1000) + "@example.test>\r\nSubject: Held message\r\n\r\nhéllo",
	} {
		t.Run(name, func(t *testing.T) {
			db, mailbox, held := quarantineReleaseFixture(t)
			if err := db.Model(&held).Update("raw_message", raw).Error; err != nil {
				t.Fatal(err)
			}
			parent := models.Message{MailboxID: mailbox.ID, Folder: "INBOX", Sender: "sender@example.test", MsgID: "<parent@example.test>", ThreadID: "<parent@example.test>"}
			if err := db.Create(&parent).Error; err != nil {
				t.Fatal(err)
			}
			if rr := requestQuarantineRelease(db, mailbox, held, t.Context()); rr.Code != http.StatusOK {
				t.Fatalf("release answered %d %s", rr.Code, rr.Body)
			}
			var released models.Message
			if err := db.Where("mailbox_id = ? AND id <> ?", mailbox.ID, parent.ID).First(&released).Error; err != nil {
				t.Fatal(err)
			}
			if name == "oversized ids" && (released.ThreadID != released.MsgID || !strings.HasSuffix(released.MsgID, "@rest-mail.invalid>")) {
				t.Fatalf("thread_id %q, message_id %q: want one generated identity", released.ThreadID, released.MsgID)
			}
			if released.ThreadID == "" || released.MsgID == "" {
				t.Fatalf("thread_id %q, message_id %q: both must be set", released.ThreadID, released.MsgID)
			}
			want := "<" + map[string]string{"a reply": "parent@example.test", "a new message": "fresh@example.test"}[name] + ">"
			if want != "<>" && released.ThreadID != want {
				t.Fatalf("thread_id %q, want %q", released.ThreadID, want)
			}
			if name == "a reply" && (released.InReplyTo != "<parent@example.test>" || released.References != "<parent@example.test>") {
				t.Fatalf("in_reply_to %q, references %q", released.InReplyTo, released.References)
			}
			handler := NewMessageHandler(db, nil, nil, "", nil)
			found := false
			for _, id := range threadMembers(t, handler, mailbox, released.ThreadID) {
				found = found || id == released.ID
			}
			if !found {
				t.Fatalf("thread %q does not hold message %d", released.ThreadID, released.ID)
			}
		})
	}
}
