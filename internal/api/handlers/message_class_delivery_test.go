package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
)

// The class is read from what delivery really stores. deliverToLocal keeps the message's
// source and no decoded header map, so a test that seeds Message.Headers by hand would not
// notice list mail arriving by SMTP being called person. A Sieve rule's receipt keyword is
// stored with the message, and neither it nor a list header files or removes anything.
func TestDeliveredMailIsClassifiedFromWhatDeliveryStores(t *testing.T) {
	db := openRedirectTestDB(t)
	if err := db.AutoMigrate(&models.SieveScript{}); err != nil {
		t.Fatal(err)
	}
	engine := pipeline.NewEngine(withQuietVacation(pipeline.DefaultRegistry), slog.Default())
	const header = "From: alice@remote.test\r\nSubject: Hi\r\n"
	const receiptRule = `require ["imap4flags"]; addflag "$Receipt";`

	for index, test := range []struct {
		name, script, headers, want string
	}{
		{name: "no signals", want: "person"},
		{name: "list id", headers: "List-Id: News <news.example.test>\r\n", want: "newsletter"},
		{name: "folded list id in another case", headers: "list-id: News\r\n <news.example.test>\r\n", want: "newsletter"},
		{name: "list unsubscribe", headers: "List-Unsubscribe: <https://example.test/leave>\r\n", want: "newsletter"},
		{name: "precedence", headers: "Precedence: bulk\r\n", want: "notification"},
		{name: "a list header forged on a person's mail", headers: "List-Id: <forged.example.test>\r\n", want: "newsletter"},
		{name: "a sender cannot claim a rule's keyword", headers: "Keywords: $Receipt\r\nX-Keywords: $Receipt\r\n", want: "person"},
		{name: "receipt rule", script: receiptRule, want: "receipt"},
		{name: "receipt rule beats list headers", script: receiptRule, headers: "List-Id: <shop.example.test>\r\n", want: "receipt"},
		{name: "keyword removed again", script: `require ["imap4flags"]; addflag "$receipt"; removeflag "$receipt";`, want: "person"},
		{name: "another keyword", script: `require ["imap4flags"]; addflag "$newsletter";`, want: "person"},
		{name: "rule that does not match", script: `require ["imap4flags"]; if header :contains "Subject" "invoice" { addflag "$receipt"; }`, want: "person"},
	} {
		t.Run(test.name, func(t *testing.T) {
			domain := models.Domain{Name: fmt.Sprintf("class-%d-%d.test", index, time.Now().UnixNano()), Active: true}
			if err := db.Create(&domain).Error; err != nil {
				t.Fatal(err)
			}
			mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "owner", Address: "owner@" + domain.Name, Active: true, QuotaBytes: 1 << 30}
			if err := db.Create(&mailbox).Error; err != nil {
				t.Fatal(err)
			}
			seedReceiveFixture(t, db, mailbox)
			steps, _ := json.Marshal([]pipeline.FilterConfig{sieveStep("")})
			line := models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: steps, Active: true}
			if err := db.Create(&line).Error; err != nil {
				t.Fatal(err)
			}
			if test.script != "" {
				if err := db.Create(&models.SieveScript{MailboxID: mailbox.ID, Script: test.script, ScriptHash: filters.HashSieveScript(test.script), Active: true}).Error; err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				db.Where("mailbox_id = ?", mailbox.ID).Delete(&models.SieveScript{})
				db.Where("mailbox_id = ?", mailbox.ID).Delete(&models.Message{})
				db.Where("id = ?", line.ID).Delete(&models.Pipeline{})
				db.Where("id = ?", mailbox.ID).Delete(&models.Mailbox{})
				db.Where("id = ?", domain.ID).Delete(&models.Domain{})
			})
			raw := header + test.headers + "\r\nhello body\r\n"
			body, _ := json.Marshal(map[string]any{"address": mailbox.Address, "sender": "alice@remote.test", "subject": "Hi", "body_text": "hello body", "raw_message": []byte(raw)})
			recorder := httptest.NewRecorder()
			deliveryRoute(NewMessageHandler(db, nil, engine, "", nil))(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", bytes.NewReader(body)))
			if recorder.Code != http.StatusCreated {
				t.Fatalf("deliver = %d: %s", recorder.Code, recorder.Body)
			}
			var stored models.Message
			if err := db.Where("mailbox_id = ?", mailbox.ID).First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if got := messageOf(stored).Class; got != test.want {
				t.Fatalf("class = %q, want %q", got, test.want)
			}
			if stored.Folder != "INBOX" || stored.IsDeleted {
				t.Fatalf("classifying moved or removed the message: folder %q, deleted %v", stored.Folder, stored.IsDeleted)
			}

			// The rule's answer was recorded at acceptance: the same message keeps it when
			// the script later changes or goes away.
			db.Where("mailbox_id = ?", mailbox.ID).Delete(&models.SieveScript{})
			var again models.Message
			if err := db.First(&again, stored.ID).Error; err != nil {
				t.Fatal(err)
			}
			if got := messageOf(again).Class; got != test.want {
				t.Fatalf("class after the script went = %q, want %q", got, test.want)
			}
		})
	}
}

// A RESTMAIL recipient's own rule decides that recipient's copy alone.
func TestRESTMAILDeliver_SieveReceiptRuleAppliesToItsRecipient(t *testing.T) {
	db := openRedirectTestDB(t)
	if err := db.AutoMigrate(&models.SieveScript{}, &models.Quarantine{}, &models.VacationConfig{}, &models.VacationResponse{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_vacation_responses_handle ON vacation_responses(mailbox_id, sender, handle)`).Error; err != nil {
		t.Fatal(err)
	}
	const script = `require ["imap4flags"]; addflag "$receipt";`
	domain := models.Domain{Name: fmt.Sprintf("class-rm-%d.test", time.Now().UnixNano()), Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	steps, _ := json.Marshal([]pipeline.FilterConfig{sieveStep("")})
	line := models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: steps, Active: true}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	var addresses []string
	boxes := map[string]uint{}
	for _, local := range []string{"ruled", "plain"} {
		mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: local, Address: local + "@" + domain.Name, Active: true, QuotaBytes: 1 << 30}
		if err := db.Create(&mailbox).Error; err != nil {
			t.Fatal(err)
		}
		seedReceiveFixture(t, db, mailbox)
		boxes[local] = mailbox.ID
		addresses = append(addresses, mailbox.Address)
	}
	if err := db.Create(&models.SieveScript{MailboxID: boxes["ruled"], Script: script, ScriptHash: filters.HashSieveScript(script), Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range boxes {
			db.Where("mailbox_id = ?", id).Delete(&models.SieveScript{})
			db.Where("mailbox_id = ?", id).Delete(&models.Message{})
			db.Where("id = ?", id).Delete(&models.Mailbox{})
		}
		db.Where("id = ?", line.ID).Delete(&models.Pipeline{})
		db.Where("id = ?", domain.ID).Delete(&models.Domain{})
	})
	handler := NewRestmailHandler(db, pipeline.NewEngine(withQuietVacation(pipeline.DefaultRegistry), slog.Default()), nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
	delivered, failed := deliverRESTMAIL(t, handler, "alice@remote.test", "From: alice@remote.test\r\nSubject: Hi\r\nList-Id: <shop.example.test>\r\n\r\nhello\r\n", addresses...)
	if !slices.Equal(delivered, addresses) || len(failed) != 0 {
		t.Fatalf("delivered %v failed %v", delivered, failed)
	}
	for local, want := range map[string]string{"ruled": "receipt", "plain": "newsletter"} {
		var stored models.Message
		if err := db.Where("mailbox_id = ?", boxes[local]).First(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if got := messageOf(stored).Class; got != want {
			t.Errorf("%s: class = %q, want %q", local, got, want)
		}
	}
}
