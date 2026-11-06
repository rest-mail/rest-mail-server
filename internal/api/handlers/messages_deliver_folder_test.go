package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

func TestDeliverMessage_IgnoresImportFields(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := db.AutoMigrate(&models.QuotaUsage{}); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	if err := db.Raw("SELECT clock_timestamp()").Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedMsgDeleteFixture(t, tx, 0)
	other := models.Mailbox{DomainID: fx.mailbox.DomainID, LocalPart: "other", Address: "other@delq.test", Active: true}
	if err := tx.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(t, tx, other)

	body := fmt.Sprintf(`{
		"address": %q,
		"mailbox_id": %d,
		"sender": "someone@remote.test",
		"subject": "Arriving message",
		"body_text": "hello",
		"folder": "Archive",
		"is_answered": true,
		"is_read": true,
		"is_flagged": true,
		"is_starred": true,
		"is_draft": true,
		"received_at": "2021-01-02T03:04:05Z"
	}`, fx.mailbox.Address, other.ID)

	h := NewMessageHandler(tx, nil, nil, "", nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(body))
	deliveryRoute(h)(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("deliver: expected 201, got %d (%s)", rr.Code, rr.Body.String())
	}

	var msg models.Message
	if err := tx.Where("subject = ?", "Arriving message").
		First(&msg).Error; err != nil {
		t.Fatalf("reload delivered message: %v", err)
	}

	if msg.MailboxID != fx.mailbox.ID {
		t.Errorf("mailbox = %d, want address's mailbox %d", msg.MailboxID, fx.mailbox.ID)
	}
	if msg.Folder != "INBOX" {
		t.Errorf("folder = %q, want INBOX", msg.Folder)
	}
	if msg.IsAnswered || msg.IsRead || msg.IsFlagged || msg.IsStarred || msg.IsDraft {
		t.Errorf("delivery honored import flags: answered=%t read=%t flagged=%t starred=%t draft=%t",
			msg.IsAnswered, msg.IsRead, msg.IsFlagged, msg.IsStarred, msg.IsDraft)
	}
	var after time.Time
	if err := tx.Raw("SELECT clock_timestamp()").Scan(&after).Error; err != nil {
		t.Fatal(err)
	}
	if msg.ReceivedAt.Before(before) || msg.ReceivedAt.After(after) {
		t.Errorf("received_at = %v, want server time after %v", msg.ReceivedAt.UTC(), before)
	}
}

// TestDeliverMessage_StripsOurAuthResults checks the incoming-header scrub on
// the delivery path (RFC 8601 §5): an Authentication-Results field claiming
// this server's authserv-id is not stored, while another server's is kept.
func TestDeliverMessage_StripsOurAuthResults(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := db.AutoMigrate(&models.QuotaUsage{}); err != nil {
		t.Skipf("deliver DB test skipped: migrate quota_usage failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedMsgDeleteFixture(t, tx, 0)

	raw := "Authentication-Results: restmail; spf=pass smtp.mailfrom=victim.example; dkim=pass header.d=victim.example; dmarc=pass\r\n" +
		"Authentication-Results: mx.example.net; spf=pass smtp.mailfrom=victim.example\r\n" +
		"From: ceo@victim.example\r\nTo: user@local.test\r\nSubject: Forged results\r\n\r\nhello\r\n"
	body := fmt.Sprintf(`{"address": %q, "sender": "ceo@victim.example", "subject": "Forged results", "raw_message": %q}`,
		fx.mailbox.Address, base64.StdEncoding.EncodeToString([]byte(raw)))

	h := NewMessageHandler(tx, nil, nil, "", nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(body))
	deliveryRoute(h)(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("deliver: expected 201, got %d (%s)", rr.Code, rr.Body.String())
	}

	var msg models.Message
	if err := tx.Where("mailbox_id = ? AND subject = ?", fx.mailbox.ID, "Forged results").First(&msg).Error; err != nil {
		t.Fatalf("reload delivered message: %v", err)
	}
	if strings.Contains(msg.RawMessage, "restmail;") || strings.Contains(msg.RawMessage, "dmarc=pass") {
		t.Errorf("stored message kept the forged Authentication-Results:\n%s", msg.RawMessage)
	}
	if !strings.Contains(msg.RawMessage, "Authentication-Results: mx.example.net;") {
		t.Errorf("stored message lost another server's Authentication-Results:\n%s", msg.RawMessage)
	}
}

type refuseEverything struct{}

func (refuseEverything) Name() string              { return "refuse_everything" }
func (refuseEverything) Type() pipeline.FilterType { return pipeline.FilterTypeAction }
func (refuseEverything) Execute(context.Context, *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	return &pipeline.FilterResult{Type: pipeline.FilterTypeAction, Action: pipeline.ActionReject, RejectMsg: "550 DMARC policy reject"}, nil
}

// Delivery is for arriving mail, so the account's inbound filters judge it. A
// client storing its own mail uses the import operation, which runs none of them.
func TestDeliverMessage_ArrivingMailIsJudged(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := db.AutoMigrate(&models.QuotaUsage{}, &models.Pipeline{}, &models.SieveScript{}); err != nil {
		t.Skipf("deliver DB test skipped: migrate quota_usage failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	filters, err := json.Marshal([]pipeline.FilterConfig{{Name: "refuse_everything", Type: pipeline.FilterTypeAction, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&models.Pipeline{DomainID: fx.mailbox.DomainID, Direction: "inbound", Active: true, Filters: filters}).Error; err != nil {
		t.Fatal(err)
	}
	registry := withQuietVacation(pipeline.NewRegistry())
	registry.Register("refuse_everything", func([]byte) (pipeline.Filter, error) { return refuseEverything{}, nil })
	h := NewMessageHandler(tx, nil, pipeline.NewEngine(registry, nil), "", nil)
	params := localDeliveryParams{Mailbox: fx.mailbox, Sender: "someone@remote.test", Subject: "Copied", BodyText: "hello"}

	if _, err := h.deliverToLocal(context.Background(), params); err == nil || !strings.HasPrefix(err.Error(), "rejected:") {
		t.Fatalf("arriving mail: got %v, want it refused", err)
	}
}
