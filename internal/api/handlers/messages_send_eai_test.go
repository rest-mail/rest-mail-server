package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
)

// An API send to a hosted recipient spelled with a Unicode domain or another
// normalization form is delivered locally to the account it resolves to, not
// queued to the outside world. The message keeps the addresses as submitted.
func TestSendMessageLocalRecipientsEAI(t *testing.T) {
	db := openPlusAddrTestDB(t)
	if err := db.AutoMigrate(&models.OutboundQueue{}, &models.Contact{}, &models.CalendarEventVersion{}); err != nil {
		t.Skipf("API send EAI test skipped: migrate failed (%v)", err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	suffix := fmt.Sprintf("eai-send-%d.test", time.Now().UnixNano())
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
		return mb
	}
	sender := seed("absender")
	mailbox := seed("münchen")
	legacy := seed("café")
	inactive := seed("noël")
	seed("noël")
	if err := tx.Model(&inactive).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}

	to := "MÜNCHEN@" + unicodeDomain
	cc := "café@" + unicodeDomain
	h := NewMessageHandler(tx, nil, nil, "", nil)
	body := `{"from":"` + sender.Address + `","to":["` + to + `"],"cc":["` + cc + `"],"bcc":["` + inactive.Address + `"],"subject":"Grüße","body_text":"hallo"}`
	rr := sendAs(h, &auth.Claims{AccountID: sender.ID, CredentialKind: "app_password"}, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("send: %d %s", rr.Code, rr.Body.String())
	}

	for _, want := range []models.Mailbox{mailbox, legacy} {
		var msg models.Message
		if err := tx.Where("mailbox_id = ? AND folder = ?", want.ID, "INBOX").First(&msg).Error; err != nil {
			t.Fatalf("%s: no local delivery: %v", want.Address, err)
		}
		if msg.Sender != sender.Address || !strings.Contains(string(msg.RecipientsTo), to) || !strings.Contains(string(msg.RecipientsCc), cc) {
			t.Fatalf("%s: delivery rewrote message addresses: %+v", want.Address, msg)
		}
	}

	// Only the inactive exact identity leaves through the queue; it is never
	// redirected to its active NFC twin, and the hosted spellings stay local.
	var queued []models.OutboundQueue
	if err := tx.Where("sender = ?", sender.Address).Find(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].Recipient != inactive.Address {
		t.Fatalf("queued = %+v; want only %s", queued, inactive.Address)
	}
	var twinCount int64
	tx.Model(&models.Message{}).Where("mailbox_id <> ? AND mailbox_id IN (?)", sender.ID,
		tx.Model(&models.Mailbox{}).Select("id").Where("address = ?", "noël@"+asciiDomain)).Count(&twinCount)
	if twinCount != 0 {
		t.Fatal("inactive exact identity was redirected to its NFC twin")
	}

	// A calendar reply to a hosted organizer spelled another way is local too.
	events, err := json.Marshal([]pipeline.CalendarEvent{{Method: "REQUEST", UID: "eai-1", Summary: "Treffen", Organizer: pipeline.CalendarAddress{Address: to}}})
	if err != nil {
		t.Fatal(err)
	}
	invite := models.Message{MailboxID: sender.ID, Folder: "INBOX", Sender: to, Subject: "Treffen", CalendarEventsRaw: events, ReceivedAt: time.Now()}
	if err := tx.Create(&invite).Error; err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprint(invite.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/"+id+"/calendar-reply", strings.NewReader(`{"response":"ACCEPTED","from":"`+sender.Address+`"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	req = req.WithContext(context.WithValue(ctx, middleware.ClaimsKey, &auth.Claims{AccountID: sender.ID, CredentialKind: "app_password"}))
	rr = httptest.NewRecorder()
	composeRoutes(h).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
		t.Fatalf("calendar reply: %d %s", rr.Code, rr.Body.String())
	}
	var replies int64
	tx.Model(&models.Message{}).Where("mailbox_id = ? AND subject LIKE ?", mailbox.ID, "Accepted%").Count(&replies)
	if replies != 1 {
		t.Fatalf("calendar reply delivered locally %d times; want 1", replies)
	}
	if err := tx.Where("sender = ?", sender.Address).Find(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Fatalf("calendar reply to a hosted organizer was queued: %+v", queued)
	}
}
