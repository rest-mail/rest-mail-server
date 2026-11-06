package handlers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
)

// receivedMessage is a message carrying n Received: fields.
func receivedMessage(n int, to string) string {
	return strings.Repeat("Received: from relay.test by relay.test; Tue, 3 Nov 2026 10:00:00 +0000\r\n", n) +
		"From: sender@external.test\r\nTo: " + to + "\r\nDate: Tue, 3 Nov 2026 10:00:00 +0000\r\nSubject: hops\r\n\r\nhello\r\n"
}

// nestedRaw is a message whose multiparts nest depth deep.
func nestedRaw(depth int, to string) string {
	var head, tail strings.Builder
	head.WriteString("From: sender@external.test\r\nTo: " + to + "\r\nDate: Tue, 3 Nov 2026 10:00:00 +0000\r\nSubject: nested\r\nMIME-Version: 1.0\r\n")
	for i := 0; i < depth; i++ {
		fmt.Fprintf(&head, "Content-Type: multipart/mixed; boundary=b%d\r\n\r\n--b%d\r\n", i, i)
	}
	head.WriteString("Content-Type: text/plain\r\n\r\nbottom\r\n")
	for i := depth - 1; i >= 0; i-- {
		fmt.Fprintf(&tail, "--b%d--\r\n", i)
	}
	return head.String() + tail.String()
}

func deliverRaw(h *MessageHandler, mailboxID uint, raw string, arrival bool) *httptest.ResponseRecorder {
	body := map[string]any{"mailbox_id": mailboxID, "sender": "sender@external.test", "raw_message": base64.StdEncoding.EncodeToString([]byte(raw))}
	if arrival {
		body["received_tls"] = false
	}
	encoded, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	deliveryRoute(h)(rr, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", bytes.NewReader(encoded)))
	return rr
}

// An arrival with more Received: fields than the hop limit is a mail loop, refused 550 before
// the database is touched (#1647).
func TestDeliveryRefusesMailLoopBeforeDatabase(t *testing.T) {
	rr := deliverRaw(&MessageHandler{}, 1, receivedMessage(rmime.CurrentLimits().MaxReceived+1, "eve@limits.test"), true)
	if rr.Code != 550 || !strings.Contains(rr.Body.String(), `"rejected"`) {
		t.Fatalf("got %d %s, want 550 rejected", rr.Code, rr.Body)
	}
}

// At the hop limit an arrival is delivered; an IMAP APPEND (no received_tls) is filed whatever
// its Received fields, since filing is not a hop; and a message nested past the MIME depth limit
// is refused 403 with nothing stored (#1647, #1641).
func TestDeliveryInboundLimits(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := db.AutoMigrate(&models.QuotaUsage{}); err != nil {
		t.Skipf("deliver DB test skipped: migrate quota_usage failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	h := NewMessageHandler(tx, nil, nil, "", nil)
	limits := rmime.CurrentLimits()

	if rr := deliverRaw(h, fx.mailbox.ID, receivedMessage(limits.MaxReceived, fx.mailbox.Address), true); rr.Code != http.StatusCreated {
		t.Fatalf("arrival at the hop limit: got %d %s, want 201", rr.Code, rr.Body)
	}
	if rr := deliverRaw(h, fx.mailbox.ID, receivedMessage(limits.MaxReceived+10, fx.mailbox.Address), false); rr.Code != http.StatusCreated {
		t.Fatalf("APPEND past the hop limit: got %d %s, want 201", rr.Code, rr.Body)
	}
	before := countMessages(t, tx, fx.mailbox.ID)
	rr := deliverRaw(h, fx.mailbox.ID, nestedRaw(limits.MaxNestingDepth+1, fx.mailbox.Address), true)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), `"rejected"`) {
		t.Fatalf("nested past the limit: got %d %s, want 403 rejected", rr.Code, rr.Body)
	}
	if after := countMessages(t, tx, fx.mailbox.ID); after != before {
		t.Fatalf("refused delivery stored %d messages", after-before)
	}
}

// RESTMAIL delivery from another server refuses a looping or over-nested message 403 rejected
// before any lookup (#1647, #1641).
func TestRestmailDeliverInboundLimits(t *testing.T) {
	h := NewRestmailHandler(nil, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
	limits := rmime.CurrentLimits()
	for name, raw := range map[string]string{
		"loop":   receivedMessage(limits.MaxReceived+1, "eve@limits.test"),
		"nested": nestedRaw(limits.MaxNestingDepth+1, "eve@limits.test"),
	} {
		t.Run(name, func(t *testing.T) {
			encoded, _ := json.Marshal(map[string]any{"from": "sender@external.test", "raw_message": raw})
			rr := httptest.NewRecorder()
			restmailRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, restmailMessagesPath, bytes.NewReader(encoded)))
			if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), `"rejected"`) {
				t.Fatalf("got %d %s, want 403 rejected", rr.Code, rr.Body)
			}
		})
	}
}

// RESTMAIL delivery at the hop limit is delivered.
func TestRestmailDeliverAtHopLimit(t *testing.T) {
	db := openPlusAddrTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: "hop-limit.test", Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "eve", Address: "eve@hop-limit.test", Active: true}
	if err := tx.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(t, tx, mailbox)
	h := NewRestmailHandler(tx, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"203.0.113.0/24"}})
	raw := receivedMessage(rmime.CurrentLimits().MaxReceived, mailbox.Address)
	encoded, _ := json.Marshal(map[string]any{"from": "sender@external.test", "raw_message": raw})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, restmailMessagesPath, bytes.NewReader(encoded))
	req.RemoteAddr = "203.0.113.9:5000"
	restmailRoutes(h).ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("at the hop limit: got %d %s, want 201", rr.Code, rr.Body)
	}
}

// Import holds a message to the MIME limits, refusing one nested too deep 422 on raw_message,
// and stores old mail whatever its Received fields: storing it is not a hop (#1641, #1647).
func TestMessageImportInboundLimits(t *testing.T) {
	db := importProofDB(t)
	fx := seedAccountsFixture(t, db)
	if err := db.Create(&models.QuotaUsage{MailboxID: fx.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	h := NewMessageImportHandler(db, 1<<20, nil, nil)
	h.directory = t.TempDir()
	router := importMessageRoutes(h)
	limits := rmime.CurrentLimits()
	call := func(raw string) *httptest.ResponseRecorder {
		encoded, err := json.Marshal(apitypes.ImportMessageBody{RawMessage: []byte(raw), Folder: "Archive", InternalDate: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		request := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.admin.ID, nil, string(encoded))
		request.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		return w
	}
	if w := call(nestedRaw(limits.MaxNestingDepth+1, fx.user.Address)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"raw_message"`) {
		t.Fatalf("nested past the limit: %d %s, want 422 naming raw_message", w.Code, w.Body)
	}
	if w := call("Message-ID: <hops@example.test>\r\n" + receivedMessage(limits.MaxReceived+10, fx.user.Address)); w.Code != http.StatusOK {
		t.Fatalf("old mail past the hop limit: %d %s, want 200", w.Code, w.Body)
	}
}
