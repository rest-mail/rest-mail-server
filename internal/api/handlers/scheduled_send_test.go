package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// openScheduledSendTestDB is openMsgDeleteTestDB plus the rows the send path
// writes: contacts, the outbound queue, and the scheduled messages themselves.
func openScheduledSendTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb := openMsgDeleteTestDB(t)
	if err := gdb.AutoMigrate(
		&models.Contact{},
		&models.OutboundQueue{},
		&models.ScheduledSend{},
	); err != nil {
		t.Skipf("scheduled-send DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

func TestScheduleMessage_StoresListsAndCancels(t *testing.T) {
	db := openScheduledSendTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedMsgDeleteFixture(t, tx, 0)
	h := NewMessageHandler(tx, nil, nil, "", nil)

	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	body := fmt.Sprintf(
		`{"from":%q,"to":["rcpt@remote.test"],"subject":"later","body_text":"body","scheduled_at":%q}`,
		fx.mailbox.Address, at.Format(time.RFC3339),
	)
	rr := httptest.NewRecorder()
	h.SendMessage(rr, authzRequest(
		http.MethodPost, "/api/v1/messages/send", fx.mailbox.ID, nil, body,
	))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("schedule: expected 202, got %d (%s)", rr.Code, rr.Body.String())
	}
	var created struct {
		Data models.ScheduledSend `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode scheduled message: %v", err)
	}
	if created.Data.Status != "pending" {
		t.Errorf("status = %q, want pending", created.Data.Status)
	}
	if !created.Data.SendAt.Equal(at) {
		t.Errorf("scheduled_at = %v, want %v", created.Data.SendAt, at)
	}
	if n := countMessages(t, tx, fx.mailbox.ID); n != 0 {
		t.Errorf("scheduling created %d messages in Sent; it must not send yet", n)
	}

	rr = httptest.NewRecorder()
	h.ListScheduledSends(rr, authzRequest(
		http.MethodGet, "/api/v1/messages/scheduled", fx.mailbox.ID, nil, "",
	))
	if rr.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var listed struct {
		Data []models.ScheduledSend `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Data) != 1 || listed.Data[0].ID != created.Data.ID {
		t.Fatalf("list = %+v, want the one scheduled message", listed.Data)
	}

	id := uintToStr(created.Data.ID)
	rr = httptest.NewRecorder()
	h.CancelScheduledSend(rr, authzRequest(
		http.MethodDelete, "/api/v1/messages/scheduled/"+id, fx.mailbox.ID,
		map[string]string{"id": id}, "",
	))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("cancel: expected 204, got %d (%s)", rr.Code, rr.Body.String())
	}
	var reloaded models.ScheduledSend
	if err := tx.First(&reloaded, created.Data.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Status != "cancelled" {
		t.Errorf("status after cancel = %q, want cancelled", reloaded.Status)
	}

	rr = httptest.NewRecorder()
	h.ListScheduledSends(rr, authzRequest(
		http.MethodGet, "/api/v1/messages/scheduled", fx.mailbox.ID, nil, "",
	))
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list after cancel: %v", err)
	}
	if len(listed.Data) != 0 {
		t.Errorf("list after cancel = %+v, want empty", listed.Data)
	}
}

func TestScheduleMessage_RefusesAPastTime(t *testing.T) {
	db := openScheduledSendTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedMsgDeleteFixture(t, tx, 0)
	h := NewMessageHandler(tx, nil, nil, "", nil)
	body := fmt.Sprintf(
		`{"from":%q,"to":["rcpt@remote.test"],"subject":"late","body_text":"body","scheduled_at":%q}`,
		fx.mailbox.Address, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	)
	rr := httptest.NewRecorder()
	h.SendMessage(rr, authzRequest(
		http.MethodPost, "/api/v1/messages/send", fx.mailbox.ID, nil, body,
	))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("past scheduled_at: expected 422, got %d (%s)", rr.Code, rr.Body.String())
	}
	var count int64
	tx.Model(&models.ScheduledSend{}).Count(&count)
	if count != 0 {
		t.Errorf("refused schedule stored %d rows, want 0", count)
	}
}

func TestRunScheduledSends_SendsWhenDue(t *testing.T) {
	db := openScheduledSendTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedMsgDeleteFixture(t, tx, 0)
	h := NewMessageHandler(tx, nil, nil, "", nil)

	at := time.Now().Add(-time.Minute)
	request, _ := json.Marshal(map[string]any{
		"from":      fx.mailbox.Address,
		"to":        []string{"rcpt@remote.test"},
		"subject":   "due now",
		"body_text": "body",
	})
	row := models.ScheduledSend{
		AccountID: fx.mailbox.ID,
		From:      fx.mailbox.Address,
		To:        models.JSONB(`["rcpt@remote.test"]`),
		Subject:   "due now",
		SendAt:    at,
		Status:    "pending",
		Request:   models.JSONB(request),
	}
	if err := tx.Create(&row).Error; err != nil {
		t.Fatalf("create scheduled send: %v", err)
	}

	h.runScheduledSends(context.Background())

	var reloaded models.ScheduledSend
	if err := tx.First(&reloaded, row.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Status != "sent" {
		t.Fatalf("status = %q (%s), want sent", reloaded.Status, reloaded.LastError)
	}
	if reloaded.SentMessageID == nil || *reloaded.SentMessageID == 0 {
		t.Error("sent_message_id was not recorded")
	}
	if n := countMessages(t, tx, fx.mailbox.ID); n != 1 {
		t.Errorf("Sent has %d messages, want 1", n)
	}
	var queued int64
	tx.Model(&models.OutboundQueue{}).Count(&queued)
	if queued != 1 {
		t.Errorf("outbound queue has %d rows, want 1", queued)
	}
}
