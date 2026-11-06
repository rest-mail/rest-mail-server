package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"gorm.io/gorm"
)

func threadRootFixture(t *testing.T) (*gorm.DB, authzFixture, *MessageHandler) {
	t.Helper()
	database, err := connectTestDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := rmdb.AutoMigrate(database); err != nil {
		t.Fatal(err)
	}
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedAuthzFixture(t, tx)
	seedReceiveFixture(t, tx, fixture.ownerMailbox, fixture.attackerMailbox)
	// Mail is delivered only to a live domain's mailboxes (#1845).
	if err := tx.Model(&models.Domain{}).Where("id IN ?", []uint{fixture.ownerMailbox.DomainID, fixture.attackerMailbox.DomainID}).Update("active", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&[]models.QuotaUsage{{MailboxID: fixture.ownerMailbox.ID}, {MailboxID: fixture.attackerMailbox.ID}}).Error; err != nil {
		t.Fatal(err)
	}
	return tx, fixture, NewMessageHandler(tx, nil, nil, "", nil)
}

func TestThreadRootDeliveryAndDraftRoutes(t *testing.T) {
	tx, fixture, handler := threadRootFixture(t)
	owner, other := fixture.ownerMailbox, fixture.attackerMailbox
	parents := []models.Message{
		{MailboxID: owner.ID, MsgID: "<parent@thread.test>", ThreadID: "<root@thread.test>", Sender: other.Address},
		{MailboxID: other.ID, MsgID: "<foreign@thread.test>", ThreadID: "<private-root@thread.test>", Sender: other.Address},
		{MailboxID: other.ID, MsgID: "<parent@thread.test>", ThreadID: "<wrong-root@thread.test>", Sender: other.Address},
	}
	if err := tx.Create(&parents).Error; err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"gateway", "restmail", "import", "draft", "send"} {
		for _, test := range []struct{ name, parent, refs, want string }{
			{"known parent", "parent@thread.test", "", "<root@thread.test>"},
			{"known reference", "", "<untrusted@thread.test> <parent@thread.test>", "<root@thread.test>"},
			{"known parent overrides reference", "<parent@thread.test>", "<foreign@thread.test>", "<root@thread.test>"},
			{"unknown parent", "<unknown@thread.test>", "", "<unknown@thread.test>"},
			{"unknown references", "<unknown@thread.test>", "<header-root@thread.test> <unknown@thread.test>", "<header-root@thread.test>"},
			{"foreign parent", "<foreign@thread.test>", "", "<foreign@thread.test>"},
			{"foreign reference", "", "<foreign@thread.test>", "<foreign@thread.test>"},
		} {
			if route == "send" && test.refs != "" {
				continue
			}
			t.Run(route+"/"+test.name, func(t *testing.T) {
				messageID := fmt.Sprintf("<%s-%s@thread.test>", route, strings.ReplaceAll(test.name, " ", "-"))
				raw := "Message-ID: " + messageID + "\r\nFrom: " + other.Address + "\r\nTo: " + owner.Address + "\r\nSubject: reply\r\n"
				if test.parent != "" {
					raw += "In-Reply-To: " + test.parent + "\r\n"
				}
				if test.refs != "" {
					raw += "References: " + test.refs + "\r\n"
				}
				raw += "\r\nbody"
				body := map[string]any{"message_id": messageID, "in_reply_to": test.parent, "references": test.refs, "subject": "reply", "body_text": "body"}
				path := "/api/v1/messages/deliver"
				var serve http.HandlerFunc
				switch route {
				case "gateway":
					body["mailbox_id"], body["sender"], body["raw_message"] = owner.ID, other.Address, []byte(raw)
					serve = deliveryRoute(handler)
				case "restmail":
					body["from"], body["to"], body["raw_message"] = other.Address, []string{owner.Address}, raw
					path = "/restmail/messages"
					serve = restmailRoutes(NewRestmailHandler(tx, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"192.0.2.0/24"}})).ServeHTTP
				case "import":
					body = map[string]any{"raw_message": []byte(raw), "internal_date": time.Now()}
					path = fmt.Sprintf("/api/v1/accounts/%d/messages/import", owner.ID)
					serve = importMessageRoutes(NewMessageImportHandler(tx, 4096, nil, nil)).ServeHTTP
				case "draft":
					delete(body, "message_id")
					body["from"], body["to"] = owner.Address, []string{other.Address}
					path = "/api/v1/messages/draft"
					serve = composeRoute(handler)
				case "send":
					delete(body, "message_id")
					delete(body, "references")
					body["from"], body["to"] = owner.Address, []string{"remote@thread-root.example"}
					path = "/api/v1/messages/send"
					serve = composeRoute(handler)
				}
				encoded, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				response := httptest.NewRecorder()
				serve(response, authzRequest(http.MethodPost, path, owner.ID, nil, string(encoded)))
				// A RESTMAIL peer's in_reply_to is held to RFC 5322 msg-id
				// syntax (#1608): a bare id is refused, not threaded.
				if route == "restmail" && test.parent != "" && !strings.HasPrefix(test.parent, "<") {
					if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"in_reply_to"`) {
						t.Fatalf("bare in_reply_to: %d %s, want 422 naming in_reply_to", response.Code, response.Body)
					}
					return
				}
				if response.Code >= 300 {
					t.Fatalf("delivery: %d %s", response.Code, response.Body)
				}
				var row models.Message
				if err := tx.Where("mailbox_id = ?", owner.ID).Order("id DESC").First(&row).Error; err != nil {
					t.Fatal(err)
				}
				if row.ThreadID != test.want {
					t.Errorf("thread_id = %q, want %q", row.ThreadID, test.want)
				}
				if route == "send" && test.name == "foreign parent" {
					if row.References != "<foreign@thread.test>" {
						t.Errorf("outbound References read another mailbox: %q", row.References)
					}
				}
			})
		}
	}
}

func TestThreadRootConversationThroughSendRoutes(t *testing.T) {
	tx, fixture, handler := threadRootFixture(t)
	accounts := []models.Mailbox{fixture.ownerMailbox, fixture.attackerMailbox}
	due := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	snoozedUntil := due.Add(-time.Hour)
	var root, parent string
	var references []string
	for turn := range 4 {
		from, to := accounts[turn%2], accounts[(turn+1)%2]
		body, err := json.Marshal(map[string]any{
			"from": from.Address, "to": []string{to.Address, "remote@thread-root.example"},
			"subject": "conversation", "body_text": "turn", "in_reply_to": parent,
		})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		composeRoute(handler)(response, sendingAs(httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", strings.NewReader(string(body))), from.ID))
		if response.Code >= 300 {
			t.Fatalf("turn %d send: %d %s", turn, response.Code, response.Body)
		}
		var sent models.Message
		if err := tx.Where("mailbox_id = ? AND folder = ?", from.ID, "Sent").Order("id DESC").First(&sent).Error; err != nil {
			t.Fatal(err)
		}
		if turn == 0 {
			root = sent.MsgID
			response := httptest.NewRecorder()
			path := fmt.Sprintf("/api/v1/accounts/%d/threads/%s/due", accounts[0].ID, root)
			messageRoutes(handler).ServeHTTP(response, authzRequest(http.MethodPost, path, accounts[0].ID, nil,
				fmt.Sprintf(`{"due":%q,"due_source":"user"}`, due.Format(time.RFC3339))))
			if response.Code != http.StatusOK {
				t.Fatalf("set due: %d %s", response.Code, response.Body)
			}
			snoozeCall(t, handler, accounts[0].ID, root, fmt.Sprintf(`{"snoozed_until":%q}`, snoozedUntil.Format(time.RFC3339)), http.StatusOK)
		}
		for _, account := range accounts {
			var row models.Message
			if err := tx.Where("mailbox_id = ? AND message_id = ?", account.ID, sent.MsgID).First(&row).Error; err != nil {
				t.Fatal(err)
			}
			if row.ThreadID != root {
				t.Errorf("turn %d mailbox %d: thread_id = %q, want root %q", turn, account.ID, row.ThreadID, root)
			}
		}
		var queued models.OutboundQueue
		if err := tx.Where("message_id = ?", sent.ID).First(&queued).Error; err != nil {
			t.Fatal(err)
		}
		parsed, err := rmime.Parse([]byte(queued.RawMessage))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := strings.Join(parsed.Headers.References, " "), strings.Join(references, " "); got != want {
			t.Errorf("turn %d References = %q, want %q", turn, got, want)
		}
		if parsed.Headers.InReplyTo != parent {
			t.Errorf("turn %d In-Reply-To = %q, want %q", turn, parsed.Headers.InReplyTo, parent)
		}
		parent = sent.MsgID
		references = append(references, parent)
	}
	for _, account := range accounts {
		response := httptest.NewRecorder()
		path := fmt.Sprintf("/api/v1/accounts/%d/threads/%s", account.ID, root)
		messageRoutes(handler).ServeHTTP(response, authzRequest(http.MethodGet, path, account.ID, nil, ""))
		var result struct {
			Data []Message `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || len(result.Data) != 4 {
			t.Errorf("mailbox %d thread read: %d %s", account.ID, response.Code, response.Body)
		}
		for _, row := range result.Data {
			if account.ID == accounts[0].ID {
				if row.Due == nil || !row.Due.Equal(due) || row.SnoozedUntil == nil || !row.SnoozedUntil.Equal(snoozedUntil) {
					t.Errorf("message %d lost due/snooze: %#v", row.ID, row)
				}
			} else if row.Due != nil || row.SnoozedUntil != nil {
				t.Errorf("message %d inherited another account's due/snooze", row.ID)
			}
		}
	}
	response := httptest.NewRecorder()
	path := fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages", accounts[0].ID)
	messageRoutes(handler).ServeHTTP(response, authzRequest(http.MethodGet, path, accounts[0].ID, nil, ""))
	var listing struct {
		Data []Message `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(listing.Data) != 0 {
		t.Errorf("snoozed conversation visible in INBOX: %d %s", response.Code, response.Body)
	}
}

func TestThreadRootDraftUpdateAndSend(t *testing.T) {
	tx, fixture, handler := threadRootFixture(t)
	owner := fixture.ownerMailbox
	parent := models.Message{MailboxID: owner.ID, MsgID: "<parent@draft.test>", InReplyTo: "<root@draft.test>", ThreadID: "<root@draft.test>", Sender: owner.Address}
	if err := tx.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) {
		t.Helper()
		response := httptest.NewRecorder()
		composeRoute(handler)(response, authzRequest(method, path, owner.ID, nil, body))
		if response.Code >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body)
		}
	}
	call(http.MethodPost, "/api/v1/messages/draft", fmt.Sprintf(`{"from":%q,"to":[%q],"subject":"reply","body_text":"body"}`, owner.Address, fixture.attackerMailbox.Address))
	var draft models.Message
	if err := tx.Where("mailbox_id = ? AND is_draft", owner.ID).First(&draft).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/messages/draft/%d", draft.ID)
	call(http.MethodPut, path, `{"in_reply_to":"<parent@draft.test>"}`)
	if err := tx.First(&draft, draft.ID).Error; err != nil {
		t.Fatal(err)
	}
	if draft.ThreadID != parent.ThreadID {
		t.Errorf("updated draft thread = %q, want %q", draft.ThreadID, parent.ThreadID)
	}
	call(http.MethodPost, path+"/send", "")
	var sent models.Message
	if err := tx.Where("mailbox_id = ? AND folder = ?", owner.ID, "Sent").Order("id DESC").First(&sent).Error; err != nil {
		t.Fatal(err)
	}
	if sent.ThreadID != parent.ThreadID || sent.References != "<root@draft.test> <parent@draft.test>" {
		t.Errorf("sent draft thread = %q, References = %q", sent.ThreadID, sent.References)
	}
	call(http.MethodPost, "/api/v1/messages/draft", fmt.Sprintf(`{"from":%q,"to":[%q],"subject":"reply","references":"<root@draft.test> <parent@draft.test>"}`, owner.Address, fixture.attackerMailbox.Address))
	var referencesOnly models.Message
	if err := tx.Where("mailbox_id = ? AND is_draft", owner.ID).First(&referencesOnly).Error; err != nil {
		t.Fatal(err)
	}
	call(http.MethodPost, fmt.Sprintf("/api/v1/messages/draft/%d/send", referencesOnly.ID), "")
	sent = models.Message{}
	if err := tx.Where("mailbox_id = ? AND folder = ?", owner.ID, "Sent").Order("id DESC").First(&sent).Error; err != nil {
		t.Fatal(err)
	}
	if sent.ThreadID != parent.ThreadID || sent.References != referencesOnly.References {
		t.Errorf("References-only draft lost thread = %q, References = %q", sent.ThreadID, sent.References)
	}
}

func TestThreadRootOutboundReferencesFormat(t *testing.T) {
	var references []string
	for index := range 30 {
		references = append(references, fmt.Sprintf("turn-%d@thread-root.example", index))
	}
	for _, parent := range []string{"parent@example.test", "<parent@example.test>"} {
		raw, err := buildOutboundRaw(outboundMessage{
			From: "owner@example.test", To: []string{"other@example.test"},
			MessageID: "<reply@example.test>", InReplyTo: parent,
			References: strings.Join(references, " "), Date: time.Now(), BodyText: "reply",
		})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := rmime.Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Headers.InReplyTo != "<parent@example.test>" || len(parsed.Headers.References) != len(references) {
			t.Fatalf("reply headers = %#v", parsed.Headers)
		}
		for index, reference := range parsed.Headers.References {
			if reference != "<"+references[index]+">" {
				t.Errorf("reference %d = %q", index, reference)
			}
		}
		for _, line := range strings.Split(raw, "\r\n") {
			if len(line) > 998 {
				t.Errorf("outbound header line is %d octets", len(line))
			}
		}
	}
}
