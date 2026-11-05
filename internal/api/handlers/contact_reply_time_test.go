package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

func TestContactReplyTime(t *testing.T) {
	db, err := connectTestDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateModels(db, append(accountModels, &models.Contact{}, &models.Message{})...); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	ensureDefaultServerCeiling(t, tx)
	contact := models.Contact{MailboxID: fx.ownerMailbox.ID, Email: "friend@example.test", Name: "Friend", TrustLevel: "trusted", Source: "manual"}
	if err := tx.Create(&contact).Error; err != nil {
		t.Fatal(err)
	}
	routes := contactRoutes(NewContactHandler(tx))
	base := fmt.Sprintf("/api/v1/accounts/%d", fx.ownerMailbox.ID)
	send := func(method, suffix, body string, caller uint) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		req := authzRequest(method, base+suffix, caller, nil, body)
		req.Header.Set("Content-Type", "application/json")
		routes.ServeHTTP(rr, req)
		return rr
	}
	wantSetting := func(enabled bool) {
		t.Helper()
		rr := send(http.MethodGet, "/contact-reply-time", "", fx.ownerMailbox.ID)
		if rr.Code != 200 || rr.Body.String() != fmt.Sprintf("{\"data\":{\"enabled\":%t}}\n", enabled) {
			t.Fatalf("setting: %d %s", rr.Code, rr.Body)
		}
	}
	wantFigure := func(seconds *int64) {
		t.Helper()
		for _, suffix := range []string{"/contacts", "/contacts/suggest?q=friend"} {
			rr := send(http.MethodGet, suffix, "", fx.ownerMailbox.ID)
			var body struct {
				Data []map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || rr.Code != 200 || len(body.Data) != 1 {
				t.Fatalf("contacts: %d %s (%v)", rr.Code, rr.Body, err)
			}
			figure, found := body.Data[0]["usual_reply_seconds"]
			if seconds == nil {
				if found {
					t.Fatalf("unexpected reply figure: %s", rr.Body)
				}
			} else if string(figure) != fmt.Sprint(*seconds) {
				t.Fatalf("want %d seconds: %s", *seconds, rr.Body)
			}
		}
	}
	set := func(enabled bool) {
		t.Helper()
		rr := send(http.MethodPut, "/contact-reply-time", fmt.Sprintf(`{"enabled":%t}`, enabled), fx.ownerMailbox.ID)
		if rr.Code != 200 {
			t.Fatalf("save setting: %d %s", rr.Code, rr.Body)
		}
		wantSetting(enabled)
	}
	t.Run("off by default and no history query", func(t *testing.T) {
		wantSetting(false)
		const callback = "reply_time_history_guard"
		if err := tx.Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
			if query.Statement.Table == "messages" {
				_ = query.AddError(fmt.Errorf("history queried while off"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Callback().Query().Remove(callback) })
		wantFigure(nil)
	})
	t.Run("refuses unrelated caller", func(t *testing.T) {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			rr := send(method, "/contact-reply-time", `{"enabled":true}`, fx.attackerMailbox.ID)
			if rr.Code != 404 {
				t.Fatalf("unauthorized setting: %d %s", rr.Code, rr.Body)
			}
		}
		for _, suffix := range []string{"/contacts", "/contacts/suggest?q=friend"} {
			rr := send(http.MethodGet, suffix, "", fx.attackerMailbox.ID)
			if rr.Code != 404 {
				t.Fatalf("unauthorized history: %d %s", rr.Code, rr.Body)
			}
		}
		wantSetting(false)
	})
	set(true)
	wantFigure(nil)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exchange := func(account uint, index int, delay time.Duration, reference bool) {
		t.Helper()
		id := fmt.Sprintf("<sent-%d@example.test>", index)
		sent := models.Message{MailboxID: account, Folder: "Sent", MsgID: id, Sender: fx.ownerMailbox.Address,
			RecipientsTo: models.JSONB(`["friend@example.test"]`), ReceivedAt: start.Add(time.Duration(index) * 24 * time.Hour)}
		reply := models.Message{MailboxID: account, Sender: contact.Email, MsgID: fmt.Sprintf("<reply-%d@example.test>", index),
			InReplyTo: id, ReceivedAt: sent.ReceivedAt.Add(delay)}
		if reference {
			reply.InReplyTo = ""
			reply.References = "<root@example.test> " + id
		}
		if err := tx.Create(&sent).Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Create(&reply).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Run("too little history", func(t *testing.T) {
		exchange(fx.ownerMailbox.ID, 1, time.Hour, false)
		exchange(fx.ownerMailbox.ID, 2, 2*time.Hour, true)
		wantFigure(nil)
	})
	t.Run("median and first reply only", func(t *testing.T) {
		exchange(fx.ownerMailbox.ID, 3, 20*time.Hour, false)
		extra := models.Message{MailboxID: fx.ownerMailbox.ID, Sender: contact.Email, InReplyTo: "<sent-1@example.test>", ReceivedAt: start.Add(5 * 24 * time.Hour)}
		if err := tx.Create(&extra).Error; err != nil {
			t.Fatal(err)
		}
		seconds := int64(7200)
		wantFigure(&seconds)
	})
	t.Run("other accounts cannot affect result", func(t *testing.T) {
		for index := 1; index <= 6; index++ {
			exchange(fx.attackerMailbox.ID, index, time.Minute, false)
		}
		foreignReply := models.Message{MailboxID: fx.attackerMailbox.ID, Sender: contact.Email, InReplyTo: "<sent-3@example.test>", ReceivedAt: start.Add(73 * time.Hour)}
		if err := tx.Create(&foreignReply).Error; err != nil {
			t.Fatal(err)
		}
		orphan := models.Message{MailboxID: fx.ownerMailbox.ID, Sender: contact.Email, InReplyTo: "<sent-6@example.test>", ReceivedAt: start.Add(7 * 24 * time.Hour)}
		if err := tx.Create(&orphan).Error; err != nil {
			t.Fatal(err)
		}
		seconds := int64(7200)
		wantFigure(&seconds)
	})
	t.Run("unmatched and ineligible mail is ignored", func(t *testing.T) {
		for index, column := range []string{"is_deleted", "is_draft", "is_dsn"} {
			exchange(fx.ownerMailbox.ID, 10+index, time.Minute, false)
			if err := tx.Model(&models.Message{}).Where("mailbox_id = ? AND message_id = ?", fx.ownerMailbox.ID, fmt.Sprintf("<reply-%d@example.test>", 10+index)).Update(column, true).Error; err != nil {
				t.Fatal(err)
			}
		}
		exchange(fx.ownerMailbox.ID, 13, -time.Hour, false)
		exchange(fx.ownerMailbox.ID, 14, time.Minute, false)
		if err := tx.Model(&models.Message{}).Where("mailbox_id = ? AND message_id = ?", fx.ownerMailbox.ID, "<sent-14@example.test>").Update("recipients_to", models.JSONB(`["someone-else@example.test"]`)).Error; err != nil {
			t.Fatal(err)
		}
		exchange(fx.ownerMailbox.ID, 15, time.Minute, false)
		if err := tx.Model(&models.Message{}).Where("mailbox_id = ? AND message_id = ?", fx.ownerMailbox.ID, "<reply-15@example.test>").Updates(map[string]interface{}{"in_reply_to": "<unmatched@example.test>", "references": "<sent-15@example.test>"}).Error; err != nil {
			t.Fatal(err)
		}
		seconds := int64(7200)
		wantFigure(&seconds)
	})
	t.Run("even median uses the middle pair", func(t *testing.T) {
		exchange(fx.ownerMailbox.ID, 4, 4*time.Hour, false)
		seconds := int64(10800)
		wantFigure(&seconds)
	})
	t.Run("off hides known history and skips calculation", func(t *testing.T) {
		set(false)
		const callback = "reply_time_disabled_history_guard"
		if err := tx.Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
			if query.Statement.Table == "messages" {
				_ = query.AddError(fmt.Errorf("history queried after disabling"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Callback().Query().Remove(callback) })
		wantFigure(nil)
	})
}
