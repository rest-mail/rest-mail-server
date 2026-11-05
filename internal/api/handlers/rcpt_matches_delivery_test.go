package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
)

// The RCPT-time recipient checks answer what delivery will do (#1845, RFC 5321 §3.3). A
// mailbox on a domain that is not live is refused by delivery as unknown, so both
// checks (GET /api/mailboxes for the SMTP gateway, GET /restmail/mailboxes for RESTMAIL
// peers) report it does not exist, and the SMTP gateway refuses it 550 5.1.1 at RCPT
// before the message, and so the greylist, is ever reached. A live domain's mailbox is
// accepted by all of them.
func TestRecipientCheckMatchesDeliveryOnDomainLive(t *testing.T) {
	db := openAccountsTestDB(t)
	if err := migrateModels(db, &models.Message{}, &models.Pipeline{}, &models.Alias{}, &models.GreylistEntry{}); err != nil {
		t.Fatal(err)
	}
	dormant := models.Domain{Name: fmt.Sprintf("dormant-%d.test", time.Now().UnixNano()), Active: false}
	live := models.Domain{Name: fmt.Sprintf("live-%d.test", time.Now().UnixNano()), Active: true}
	for _, d := range []*models.Domain{&dormant, &live} {
		if err := db.Create(d).Error; err != nil {
			t.Fatal(err)
		}
	}
	mailbox := func(d models.Domain) models.Mailbox {
		mb := models.Mailbox{DomainID: d.ID, LocalPart: "user", Address: "user@" + d.Name, Active: true}
		if err := db.Create(&mb).Error; err != nil {
			t.Fatal(err)
		}
		if err := grants.EnsureReceive(db, &mb); err != nil {
			t.Fatal(err)
		}
		return mb
	}
	dormantBox, liveBox := mailbox(dormant), mailbox(live)
	t.Cleanup(func() {
		for _, mb := range []models.Mailbox{dormantBox, liveBox} {
			db.Where("recipient = ?", mb.Address).Delete(&models.GreylistEntry{})
			db.Where("mailbox_id = ?", mb.ID).Delete(&models.Message{})
		}
	})

	mailboxes := &MailboxHandler{db: db}
	rest := NewRestmailHandler(db, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
	recipientCheck, err := filters.NewRecipientCheck(db)(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		address string
		accepts bool
	}{
		{"a mailbox on a domain that is not live is refused", dormantBox.Address, false},
		{"a mailbox on a live domain is accepted", liveBox.Address, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := fmt.Sprintf(`"exists":%t`, tc.accepts)

			w := httptest.NewRecorder()
			addressCheckRoute(mailboxes)(w, httptest.NewRequest(http.MethodGet, "/api/mailboxes?address="+url.QueryEscape(tc.address), nil))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
				t.Fatalf("SMTP RCPT check: %d %s, want %s", w.Code, w.Body, want)
			}

			w = httptest.NewRecorder()
			restmailRoutes(rest).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/restmail/mailboxes?address="+url.QueryEscape(tc.address), nil))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
				t.Fatalf("RESTMAIL recipient check: %d %s, want %s", w.Code, w.Body, want)
			}

			// Delivery: the recipient_check filter and RESTMAIL's recipient resolution give
			// the same answer the RCPT checks gave.
			result, err := recipientCheck.Execute(t.Context(), &pipeline.EmailJSON{Envelope: pipeline.Envelope{RcptTo: []string{tc.address}}})
			if err != nil {
				t.Fatal(err)
			}
			if tc.accepts {
				if result.Action != pipeline.ActionContinue {
					t.Fatalf("recipient_check: %+v", result)
				}
			} else if result.Action != pipeline.ActionReject || result.RejectMsg != fmt.Sprintf("550 5.1.1 <%s>: Recipient not found", tc.address) {
				t.Fatalf("recipient_check: %+v", result)
			}
			if _, _, err := rest.resolveRecipientMailbox(t.Context(), tc.address); (err == nil) != tc.accepts {
				t.Fatalf("RESTMAIL delivery resolution: %v", err)
			}

			// The RCPT-time checks never consult the greylist.
			var greylisted int64
			if err := db.Model(&models.GreylistEntry{}).Where("recipient = ?", tc.address).Count(&greylisted).Error; err != nil {
				t.Fatal(err)
			}
			if greylisted != 0 {
				t.Fatalf("greylist entries for %s after the RCPT check: %d", tc.address, greylisted)
			}
		})
	}

	// RESTMAIL delivery itself refuses the not-live recipient as undeliverable, as it
	// refuses an unknown one, and delivers to the live one.
	for _, tc := range []struct {
		address string
		status  int
	}{{dormantBox.Address, http.StatusUnprocessableEntity}, {liveBox.Address, http.StatusCreated}} {
		w := httptest.NewRecorder()
		restmailRoutes(rest).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/restmail/messages", strings.NewReader(fmt.Sprintf(`{"from":"sender@remote.test","to":[%q],"subject":"Hi"}`, tc.address))))
		if w.Code != tc.status {
			t.Fatalf("RESTMAIL delivery to %s: %d %s, want %d", tc.address, w.Code, w.Body, tc.status)
		}
	}
}
