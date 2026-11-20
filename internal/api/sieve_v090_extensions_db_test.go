package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// The extensions go-sieve v0.9.0 runs and this server now enables (#2108): each script
// is uploaded through the production upload route, which parses it, checks every
// require against the enabled list and dry-runs it, and then runs on mail delivered as
// the SMTP gateway delivers it, through the inbound pipeline's sieve step. Each script
// is written with Unix line endings, as a JSON body usually carries it.
func TestSieveV090ExtensionsUploadAndRunOnDelivery(t *testing.T) {
	f := newCredentialFixture(t)
	encoded, err := json.Marshal([]pipeline.FilterConfig{{Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true, Config: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&models.Pipeline{DomainID: f.mustDomainID(), Direction: "inbound", Filters: encoded, Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&models.QuotaUsage{MailboxID: f.mailboxID}).Error; err != nil {
		t.Fatal(err)
	}
	sievePath := "/api/v1/accounts/" + strconv.FormatUint(uint64(f.accountID), 10) + "/sieve"

	upload := func(t *testing.T, script string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"script": script})
		rr := f.do(http.MethodPut, sievePath, f.session, string(body))
		if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
			t.Fatalf("upload = %d %s", rr.Code, rr.Body.String())
		}
		read := f.do(http.MethodGet, sievePath, f.session, "")
		var stored struct {
			Data struct {
				Script string `json:"script"`
			} `json:"data"`
		}
		if err := json.Unmarshal(read.Body.Bytes(), &stored); err != nil || stored.Data.Script != script {
			t.Fatalf("stored script is not the one uploaded: %d %s", read.Code, read.Body.String())
		}
	}
	// deliver hands the gateway's delivery of raw to rcpt to the API and returns the
	// folder the stored message is in.
	deliver := func(t *testing.T, rcpt, subject, headers string) string {
		t.Helper()
		raw := "From: alice@remote.test\r\nTo: " + f.address + "\r\n" + headers +
			"Subject: " + subject + "\r\nMessage-ID: <" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@remote.test>\r\nDate: Thu, 19 Nov 2026 12:00:00 +0000\r\n\r\nhello body\r\n"
		body, _ := json.Marshal(map[string]any{
			"address": rcpt, "sender": "alice@remote.test", "subject": subject, "body_text": "hello body",
			"raw_message": []byte(raw),
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.0.2.10:50000"
		rr := httptest.NewRecorder()
		asGateway(deliveryCN, f.gatewayRouter()).ServeHTTP(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("deliver = %d %s", rr.Code, rr.Body.String())
		}
		var delivered struct {
			Data struct {
				ID uint `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &delivered); err != nil || delivered.Data.ID == 0 {
			t.Fatalf("no message in the answer: %s", rr.Body.String())
		}
		var message models.Message
		if err := f.db.Where("mailbox_id = ?", f.mailboxID).First(&message, delivered.Data.ID).Error; err != nil {
			t.Fatalf("the delivered message is not stored: %v", err)
		}
		return message.Folder
	}
	check := func(t *testing.T, got, want, what string) {
		t.Helper()
		if got != want {
			t.Errorf("%s: delivered to %q, want %q", what, got, want)
		}
	}

	t.Run("variables with a comparator built from a variable", func(t *testing.T) {
		upload(t, "require [\"fileinto\", \"variables\", \"comparator-i;ascii-numeric\"];\n"+
			"set \"numeric\" \"i;ascii-numeric\";\n"+
			"if header :matches \"Subject\" \"Order *\" {\n"+
			"  set \"order\" \"${1}\";\n"+
			"}\n"+
			"if string :is :comparator \"${numeric}\" \"${order}\" \"0042\" {\n"+
			"  fileinto \"Order-${order}\";\n"+
			"}\n")
		check(t, deliver(t, f.address, "Order 42", ""), "Order-42", "order 42, numerically equal to 0042")
		check(t, deliver(t, f.address, "Order 41", ""), "INBOX", "order 41")
		check(t, deliver(t, f.address, "Lunch", ""), "INBOX", "no order number")
	})

	// :count here reads fields only the raw header section carries. go-sieve v0.9.0 reads
	// From, To, Cc, Bcc, Subject, Message-ID, In-Reply-To, Date and References from both
	// the structured fields and the raw section the server passes, so :count on those
	// counts each value twice on this server, so this test does not
	// claim them.
	t.Run("relational :value and :count", func(t *testing.T) {
		upload(t, "require [\"fileinto\", \"relational\", \"comparator-i;ascii-numeric\"];\n"+
			"if header :value \"gt\" :comparator \"i;ascii-numeric\" \"X-Score\" \"5\" {\n"+
			"  fileinto \"High\";\n"+
			"} elsif address :count \"ge\" :comparator \"i;ascii-numeric\" \"reply-to\" \"3\" {\n"+
			"  fileinto \"Crowd\";\n"+
			"} elsif header :count \"eq\" :comparator \"i;ascii-numeric\" \"X-Tag\" \"2\" {\n"+
			"  fileinto \"Tagged\";\n"+
			"}\n")
		check(t, deliver(t, f.address, "Score", "X-Score: 9\r\n"), "High", "X-Score 9 :value gt 5")
		check(t, deliver(t, f.address, "Score", "X-Score: 10\r\n"), "High", "X-Score 10 compares as a number, not text")
		check(t, deliver(t, f.address, "Score", "X-Score: 5\r\n"), "INBOX", "X-Score 5 is not gt 5")
		check(t, deliver(t, f.address, "Many", "Reply-To: a@remote.test, b@remote.test, c@remote.test\r\n"), "Crowd", "three Reply-To addresses :count ge 3")
		check(t, deliver(t, f.address, "Few", "Reply-To: a@remote.test, b@remote.test\r\n"), "INBOX", "two Reply-To addresses")
		check(t, deliver(t, f.address, "Tags", "X-Tag: one\r\nX-Tag: two\r\n"), "Tagged", "two X-Tag fields :count eq 2")
		check(t, deliver(t, f.address, "Tag", "X-Tag: one\r\n"), "INBOX", "one X-Tag field")
	})

	t.Run("subaddress :user and :detail", func(t *testing.T) {
		upload(t, "require [\"fileinto\", \"envelope\", \"subaddress\"];\n"+
			"if allof (envelope :user \"to\" \"chris\", envelope :detail \"to\" \"work\") {\n"+
			"  fileinto \"Work\";\n"+
			"} elsif address :detail \"cc\" \"news\" {\n"+
			"  fileinto \"News\";\n"+
			"}\n")
		check(t, deliver(t, "chris+work@example.test", "Plan", ""), "Work", "chris+work")
		check(t, deliver(t, "chris+home@example.test", "Plan", ""), "INBOX", "chris+home")
		check(t, deliver(t, f.address, "Digest", "Cc: chris+news@example.test\r\n"), "News", "Cc chris+news")
		check(t, deliver(t, f.address, "Plan", ""), "INBOX", "no detail")
	})
}
