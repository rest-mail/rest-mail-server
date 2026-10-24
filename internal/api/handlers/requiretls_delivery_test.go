package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
)

// A message the gateway delivers with require_tls keeps the REQUIRETLS tag, and the forward a
// Sieve redirect queues for it carries the tag too: "all instances of the message MUST be
// tagged in the same manner" (RFC 8689 §4.1, #724). Without it neither is tagged.
func TestDelivery_KeepsTheREQUIRETLSTag(t *testing.T) {
	db := openRedirectTestDB(t)
	engine := pipeline.NewEngine(pipeline.DefaultRegistry, slog.Default())
	for _, tagged := range []bool{true, false} {
		t.Run(fmt.Sprintf("require_tls=%t", tagged), func(t *testing.T) {
			suffix := time.Now().UnixNano()
			dom := models.Domain{Name: fmt.Sprintf("requiretls-%d.test", suffix), Active: true}
			if err := db.Create(&dom).Error; err != nil {
				t.Fatal(err)
			}
			mb := models.Mailbox{DomainID: dom.ID, LocalPart: "dana", Address: "dana@" + dom.Name, Active: true, QuotaBytes: 1 << 30}
			if err := db.Create(&mb).Error; err != nil {
				t.Fatal(err)
			}
			// The default redirect policy allows only the recipient's own domains (OSI-13).
			target := "fwd@" + dom.Name
			cfg, _ := json.Marshal(map[string]string{"script": `require ["copy"]; redirect :copy "` + target + `";`})
			filters, _ := json.Marshal([]pipeline.FilterConfig{{Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true, Config: cfg}})
			pl := models.Pipeline{DomainID: dom.ID, Direction: "inbound", Filters: filters, Active: true}
			if err := db.Create(&pl).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				db.Where("recipient = ?", target).Delete(&models.OutboundQueue{})
				db.Where("mailbox_id = ?", mb.ID).Delete(&models.Message{})
				db.Where("id = ?", pl.ID).Delete(&models.Pipeline{})
				db.Where("id = ?", mb.ID).Delete(&models.Mailbox{})
				db.Where("id = ?", dom.ID).Delete(&models.Domain{})
			})

			body, _ := json.Marshal(map[string]any{
				"address": mb.Address, "sender": "alice@remote.test", "subject": "Hi", "body_text": "hello",
				"raw_message": []byte("From: alice@remote.test\r\nTo: " + mb.Address + "\r\nSubject: Hi\r\n\r\nhello\r\n"),
				"require_tls": tagged,
			})
			rr := httptest.NewRecorder()
			deliveryRoute(NewMessageHandler(db, nil, engine, "", nil))(rr, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", bytes.NewReader(body)))
			if rr.Code != http.StatusCreated {
				t.Fatalf("deliver = %d %s", rr.Code, rr.Body)
			}
			var stored models.Message
			if err := db.Where("mailbox_id = ?", mb.ID).First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			var forward models.OutboundQueue
			if err := db.Where("recipient = ?", target).First(&forward).Error; err != nil {
				t.Fatalf("no forward queued: %v", err)
			}
			if stored.RequireTLS != tagged || forward.RequireTLS != tagged {
				t.Fatalf("stored tag %t, forward tag %t, want %t", stored.RequireTLS, forward.RequireTLS, tagged)
			}
		})
	}
}

// A message sent through the API with require_tls is queued tagged REQUIRETLS, so the queue
// delivers it only as RFC 8689 §4.2.1 allows; without it the queued copy is untagged (#724).
func TestSendMessage_RequireTLSTagsTheQueuedCopy(t *testing.T) {
	h, domain, _ := seedSigningDomain(t, "requiretls-send")
	sender := seedSendingMailbox(t, h.db, domain, "dana")
	claims := &auth.Claims{AccountID: sender.ID, CredentialKind: "app_password"}
	for _, tagged := range []bool{true, false} {
		body := fmt.Sprintf(`{"from":%q,"to":["bob@example.net"],"subject":"Hi","body_text":"hello","require_tls":%t}`, sender.Address, tagged)
		rr := sendAs(h, claims, body)
		if rr.Code != http.StatusCreated {
			t.Fatalf("send require_tls=%t: %d %s", tagged, rr.Code, rr.Body)
		}
		var sent models.Message
		if err := json.Unmarshal(dataOf(t, rr), &sent); err != nil {
			t.Fatal(err)
		}
		var q models.OutboundQueue
		if err := h.db.Where("message_id = ?", sent.ID).First(&q).Error; err != nil {
			t.Fatalf("find the queued copy: %v", err)
		}
		if q.RequireTLS != tagged {
			t.Fatalf("require_tls=%t queued RequireTLS=%t", tagged, q.RequireTLS)
		}
	}
}
