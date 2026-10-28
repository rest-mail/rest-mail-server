package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
)

// Recipient checks and both inbound delivery APIs must resolve the same hosted
// identity as account sign-in, while retaining the original message addresses.
func TestRecipientsEAI(t *testing.T) {
	db := openPlusAddrTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	suffix := fmt.Sprintf("eai-%d.test", time.Now().UnixNano())
	unicodeDomain, asciiDomain := "städte."+suffix, "xn--stdte-hra."+suffix
	domain := models.Domain{Name: asciiDomain, Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	seed := func(local string) models.Mailbox {
		t.Helper()
		mb := models.Mailbox{DomainID: domain.ID, LocalPart: local, Address: local + "@" + asciiDomain, Active: true}
		if err := tx.Create(&mb).Error; err != nil {
			t.Fatal(err)
		}
		seedReceiveFixture(t, tx, mb)
		return mb
	}
	mailbox := seed("münchen")
	legacy := seed("cafe\u0301")
	inactive := seed("noe\u0308l")
	twin := seed("noël")
	if err := tx.Model(&inactive).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	seed("a\u0301\u0323")
	seed("a\u0323\u0301")
	seed("ẹ\u0301")
	seed("e\u0301\u0323+tag")
	seed("e\u0323\u0301+tag")
	disabledTagged := seed("münchen+disabled")
	if err := tx.Model(&disabledTagged).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	rest := NewRestmailHandler(tx, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
	gateway := NewMailboxHandler(tx, nil, nil)

	gatewayCheck := func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/api/mailboxes"
		addressCheckRoute(gateway)(w, r)
	}
	for _, tc := range []struct {
		address string
		wantID  uint
	}{
		{"MU\u0308NCHEN@" + unicodeDomain, mailbox.ID},
		{mailbox.Address, mailbox.ID},
		{"café@" + unicodeDomain, legacy.ID},
		{legacy.Address, legacy.ID},
		{disabledTagged.Address, 0},
		{"ẹ\u0301+tag@" + unicodeDomain, 0},
		{inactive.Address, 0}, // Never redirect an inactive exact identity to its twin.
		{twin.Address, twin.ID},
		{"ạ\u0301@" + unicodeDomain, 0}, // Ambiguous legacy equivalence.
		{"munchen@" + unicodeDomain, 0},
		{"münchen@remote.test", 0},
		{"bad@@" + unicodeDomain, 0},
		{"münchen+tag@" + unicodeDomain + "\r\n", 0},
	} {
		t.Run(tc.address, func(t *testing.T) {
			for _, check := range []func(http.ResponseWriter, *http.Request){gatewayCheck, restmailRoute(rest, "/restmail/mailboxes")} {
				rr := httptest.NewRecorder()
				check(rr, httptest.NewRequest(http.MethodGet, "/?address="+url.QueryEscape(tc.address), nil))
				var response struct {
					Data struct {
						Exists  bool   `json:"exists"`
						Address string `json:"address"`
					} `json:"data"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if rr.Code != http.StatusOK || response.Data.Exists != (tc.wantID != 0) {
					t.Fatalf("check: %d %s", rr.Code, rr.Body.String())
				}
				if response.Data.Exists {
					var want models.Mailbox
					if err := tx.First(&want, tc.wantID).Error; err != nil {
						t.Fatal(err)
					}
					if response.Data.Address != want.Address {
						t.Fatalf("check rewrote stored identity: %q, want %q", response.Data.Address, want.Address)
					}
				}
			}
			mb, _, resolveErr := rest.resolveRecipientMailbox(t.Context(), tc.address)
			ok := resolveErr == nil
			if ok != (tc.wantID != 0) || (ok && mb.ID != tc.wantID) {
				t.Fatalf("resolve = %d, %v; want %d", mb.ID, ok, tc.wantID)
			}
		})
	}

	// An explicit canonical alias still wins when a peer uses a Unicode domain
	// and decomposed local part for the plus-addressed envelope recipient.
	claimed := "münchen+shop@" + asciiDomain
	alias := models.Alias{DomainID: domain.ID, SourceAddress: claimed, DestinationAddress: legacy.Address, Active: true}
	if err := tx.Create(&alias).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, resolveErr := rest.resolveRecipientMailbox(t.Context(), "mu\u0308nchen+shop@"+unicodeDomain); resolveErr == nil {
		t.Fatal("plus fallback bypassed the explicit alias")
	}

	for _, address := range []string{"MU\u0308NCHEN@" + unicodeDomain, "mu\u0308nchen+tag@" + unicodeDomain, "café@" + unicodeDomain} {
		raw := "From: 外部@例え.jp\r\nTo: " + address + "\r\nSubject: Grüße\r\n\r\nhello\r\n"
		delivered, failed := deliverRESTMAIL(t, rest, "外部@例え.jp", raw, address)
		if len(delivered) != 1 || delivered[0] != address || len(failed) != 0 {
			t.Fatalf("deliver %q: delivered=%v failed=%v", address, delivered, failed)
		}
		wantID := mailbox.ID
		if strings.HasPrefix(address, "café") {
			wantID = legacy.ID
		}
		var msg models.Message
		if err := tx.Where("mailbox_id = ?", wantID).Order("id DESC").First(&msg).Error; err != nil {
			t.Fatal(err)
		}
		if msg.Sender != "外部@例え.jp" || !strings.Contains(msg.RawMessage, raw) || !strings.Contains(string(msg.RecipientsTo), address) {
			t.Fatalf("delivery rewrote original message addresses: %+v", msg)
		}
		if strings.Contains(address, "+tag") && !strings.Contains(msg.RawMessage, "Delivered-To: "+address+"\r\n") {
			t.Fatal("plus delivery lost original recipient")
		}
	}

	address := "MU\u0308NCHEN@" + unicodeDomain
	raw := "From: 外部@例え.jp\r\nTo: " + address + "\r\nSubject: Grüße\r\n\r\nhello\r\n"
	body, _ := json.Marshal(map[string]interface{}{
		"address": address, "sender": "外部@例え.jp", "raw_message": []byte(raw), "recipients_to": []string{address},
	})
	rr := httptest.NewRecorder()
	deliveryRoute(NewMessageHandler(tx, nil, nil, "", nil))(rr, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("gateway delivery: %d %s", rr.Code, rr.Body.String())
	}
	var msg models.Message
	if err := tx.Where("mailbox_id = ?", mailbox.ID).Order("id DESC").First(&msg).Error; err != nil {
		t.Fatal(err)
	}
	if msg.RawMessage != raw || msg.Sender != "外部@例え.jp" {
		t.Fatal("gateway delivery changed raw message or remote sender")
	}
}

// A Unicode domain must select its hosted pipeline, including fail-closed
// behavior when a configured security filter is unavailable.
func TestRecipientsEAIInboundPipeline(t *testing.T) {
	db := openPlusAddrTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	suffix := fmt.Sprintf("eai-pipeline-%d.test", time.Now().UnixNano())
	domain := models.Domain{Name: "xn--stdte-hra." + suffix, Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mb := models.Mailbox{DomainID: domain.ID, LocalPart: "münchen", Address: "münchen@" + domain.Name, Active: true}
	if err := tx.Create(&mb).Error; err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(t, tx, mb)
	filters, err := json.Marshal([]pipeline.FilterConfig{{Name: "unavailable-security-filter", Type: pipeline.FilterTypeAction, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: filters, Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	engine := pipeline.NewEngine(pipeline.NewRegistry(), nil)
	h := NewRestmailHandler(tx, engine, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
	for _, domainName := range []string{domain.Name, "städte." + suffix, "STA\u0308DTE." + suffix, "städte." + suffix + " "} {
		body, _ := json.Marshal(map[string]interface{}{"from": "sender@remote.test", "to": []string{"münchen@" + domainName}})
		rr := httptest.NewRecorder()
		restmailRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/restmail/messages", bytes.NewReader(body)))
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("domain %q bypassed inbound pipeline: %d %s", domainName, rr.Code, rr.Body.String())
		}
	}
	var count int64
	if err := tx.Model(&models.Message{}).Where("mailbox_id = ?", mb.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stored %d messages despite deferred inbound pipeline", count)
	}
}
