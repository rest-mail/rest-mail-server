package api

import (
	"encoding/json"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"net/http"
	"os"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// Response findings can be fixed independently of #625's undecided request
// authorization/binding order and validation error paths.
func TestSendMessageResponseExamplesFitDescription(t *testing.T) {
	for _, w := range describedRouters(t).generator("test").Report() {
		if w.OperationID == "sendMessage" && w.Status != "" && w.Kind == oapifly.WarnExampleInvalid {
			t.Errorf("%s/%s: %s", w.Example, w.Status, w.Message)
		}
	}
}

func TestSendMessageResponseExamplesUseProductionRouter(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("set DB_HOST to run the isolated PostgreSQL send example proof")
	}
	f := newCredentialFixture(t)
	var sender models.Mailbox
	if err := f.db.First(&sender, f.mailboxID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&sender).Updates(map[string]any{"address": "admin@restmail.test", "local_part": "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	// The receive grant is scoped by address: the renamed mailbox needs its own.
	if err := f.db.First(&sender, f.mailboxID).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureReceive(f.db, &sender); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.Domain{}).Where("id = ?", sender.DomainID).Updates(map[string]any{"name": "restmail.test", "sending_policy": models.SendingPolicyTransactional}).Error; err != nil {
		t.Fatal(err)
	}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"valid", "from_missing", "unsubscribe_not_allowed"} {
		t.Run(name, func(t *testing.T) {
			pair := pairs["sendMessage"][name]
			examplepairs.Proven(t, "sendMessage", name)
			rr := f.do(http.MethodPost, "/api/v1/messages/send", f.session, string(pair.Request.Body))
			if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("got %d %s, want %d JSON", rr.Code, rr.Body, pair.Response.Status)
			}
			body := rr.Body.Bytes()
			if rr.Code == http.StatusCreated {
				var got, want map[string]map[string]any
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
					t.Fatal(err)
				}
				data := got["data"]
				var sent models.Message
				if err := f.db.Where("mailbox_id = ? AND folder = ?", f.mailboxID, "Sent").First(&sent).Error; err != nil {
					t.Fatal(err)
				}
				if data["id"] != float64(sent.ID) || data["mailbox_id"] != float64(f.mailboxID) || data["message_id"] != sent.MsgID || data["thread_id"] != sent.ThreadID || data["raw_size"] != float64(len(sent.RawMessage)) || sent.RawSize != len(sent.RawMessage) {
					t.Fatal("Sent identity/source and response differ")
				}
				assertExampleReceipt(t, data, sent.RawMessage, sent.SealedHash)
				data["sealed_hash"] = want["data"]["sealed_hash"]
				var queued []models.OutboundQueue
				if err := f.db.Where("message_id = ?", sent.ID).Order("id").Find(&queued).Error; err != nil {
					t.Fatal(err)
				}
				// The example's two recipients, the one on this server too, wait on
				// the queue with the bytes that were sent (#1564).
				if len(queued) != 2 || queued[0].Recipient != "admin@restmail.test" || queued[1].Recipient != "someone@imap.restmail.test" || queued[0].RawMessage != sent.RawMessage || queued[1].RawMessage != sent.RawMessage {
					t.Fatalf("unexpected queued work: %d entries", len(queued))
				}
				var delivered int64
				f.db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ?", f.mailboxID, "INBOX").Count(&delivered)
				if delivered != 0 {
					t.Fatalf("the send itself delivered %d local copies", delivered)
				}
				for _, field := range []string{"received_at", "created_at", "updated_at"} {
					stamp, err := time.Parse(time.RFC3339Nano, data[field].(string))
					if err != nil || time.Since(stamp) < -time.Second || time.Since(stamp) > time.Minute {
						t.Fatalf("invalid current %s: %v", field, data[field])
					}
				}
				// Normalize only allocated identities and timestamps after the
				// observed response, Sent row and delivery sources agree.
				for _, field := range []string{"id", "mailbox_id", "message_id", "thread_id", "received_at", "created_at", "updated_at"} {
					data[field] = want["data"][field]
				}
				body, err = json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
			}
			assertPublicExampleBody(t, pair.Response.Body, body)
			var sentCount, queueCount int64
			f.db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ?", f.mailboxID, "Sent").Count(&sentCount)
			f.db.Model(&models.OutboundQueue{}).Count(&queueCount)
			if sentCount != 1 || queueCount != 2 {
				t.Fatal("a refusal added outbound work")
			}
		})
	}
}
