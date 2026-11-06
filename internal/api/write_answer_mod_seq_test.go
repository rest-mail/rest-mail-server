package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// Every write that answers with a message answers the mod_seq the trigger stored, not the
// model's default or the value before the write (#1756). Each folder already holds a
// message, so a stored mod_seq is above the first one a folder gives out and a default
// of 1 cannot pass for it.
func TestWriteAnswersCarryStoredModSeq(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("set DB_HOST for PostgreSQL proofs of the mod_seq write answers carry")
	}
	f := newCredentialFixture(t)
	var sender models.Mailbox
	if err := f.db.First(&sender, f.mailboxID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.Domain{}).Where("id = ?", sender.DomainID).Update("sending_policy", models.SendingPolicyTransactional).Error; err != nil {
		t.Fatal(err)
	}
	// A forward's Sent copy is charged to the sender's quota row.
	if err := f.db.FirstOrCreate(&models.QuotaUsage{}, models.QuotaUsage{MailboxID: f.mailboxID}).Error; err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{"INBOX", "Sent", "Drafts"} {
		m := models.Message{MailboxID: f.mailboxID, Folder: folder, Sender: "earlier@example.net", Subject: "Earlier",
			BodyText: "earlier", RecipientsTo: models.JSONB(`["` + f.address + `"]`), RecipientsCc: models.JSONB(`[]`), IsRead: true}
		if err := f.db.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}

	// answered checks the answer's mod_seq against a fresh read of the row it names.
	answered := func(t *testing.T, rr *httptest.ResponseRecorder, status int) uint {
		t.Helper()
		if rr.Code != status {
			t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), status)
		}
		var body struct {
			Data struct {
				ID     uint   `json:"id"`
				ModSeq uint64 `json:"mod_seq"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var stored models.Message
		if err := f.db.First(&stored, body.Data.ID).Error; err != nil {
			t.Fatalf("read back message %d: %v", body.Data.ID, err)
		}
		if body.Data.ModSeq != stored.ModSeq || stored.ModSeq < 3 {
			t.Errorf("message %d answered mod_seq %d, stored %d (want equal, and above a folder's first)", stored.ID, body.Data.ModSeq, stored.ModSeq)
		}
		return stored.ID
	}

	t.Run("sendMessage", func(t *testing.T) {
		body := fmt.Sprintf(`{"from":%q,"to":[%q],"subject":"Hello","body_text":"Sent."}`, f.address, f.address)
		answered(t, f.do(http.MethodPost, "/api/v1/messages/send", f.session, body), http.StatusCreated)
	})

	var draftID uint
	t.Run("saveDraft", func(t *testing.T) {
		body := fmt.Sprintf(`{"from":%q,"to":[%q],"subject":"Draft","body_text":"Not sent yet."}`, f.address, f.address)
		draftID = answered(t, f.do(http.MethodPost, "/api/v1/messages/draft", f.session, body), http.StatusCreated)
	})

	t.Run("updateDraft", func(t *testing.T) {
		if draftID == 0 {
			t.Skip("saveDraft made no draft")
		}
		answered(t, f.do(http.MethodPut, fmt.Sprintf("/api/v1/messages/draft/%d", draftID), f.session, `{"subject":"Draft, edited"}`), http.StatusOK)
	})

	t.Run("sendDraft", func(t *testing.T) {
		if draftID == 0 {
			t.Skip("saveDraft made no draft")
		}
		answered(t, f.do(http.MethodPost, fmt.Sprintf("/api/v1/messages/draft/%d/send", draftID), f.session, ""), http.StatusCreated)
	})

	t.Run("forwardMessage", func(t *testing.T) {
		var original models.Message
		if err := f.db.Where("mailbox_id = ? AND folder = ?", f.mailboxID, "INBOX").Order("id").First(&original).Error; err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"from":%q,"to":[%q]}`, f.address, f.address)
		answered(t, f.do(http.MethodPost, fmt.Sprintf("/api/v1/messages/%d/forward", original.ID), f.session, body), http.StatusCreated)
	})

	t.Run("deliverMessage", func(t *testing.T) {
		raw := "From: someone@example.test\r\nTo: " + f.address + "\r\nDate: Tue, 03 Nov 2026 12:00:00 +0000\r\n" +
			"Message-ID: <delivered@example.test>\r\nSubject: Delivered\r\n\r\nHello.\r\n"
		body := fmt.Sprintf(`{"mailbox_id":%d,"sender":"someone@example.test","recipients_to":[%q],"subject":"Delivered","body_text":"Hello.","raw_message":%q}`,
			f.mailboxID, f.address, base64.StdEncoding.EncodeToString([]byte(raw)))
		rr := f.do(http.MethodPost, "/api/v1/messages/deliver", "", body)
		if rr.Code == http.StatusOK && strings.Contains(rr.Body.String(), `"processed"`) {
			t.Fatalf("the pipeline kept the delivery: %s", rr.Body.String())
		}
		answered(t, rr, http.StatusCreated)
	})
}
