package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/mail"
	"gorm.io/gorm"
)

// restmailMsgIDDelivery sends body through the registered RESTMAIL route from a trusted peer.
func restmailMsgIDDelivery(t *testing.T, db *gorm.DB, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, restmailMessagesPath, strings.NewReader(string(data)))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "192.0.2.10:1234"
	h := NewRestmailHandler(db, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"192.0.2.10/32"}})
	rr := httptest.NewRecorder()
	restmailRoutes(h).ServeHTTP(rr, r)
	return rr
}

func restmailMsgIDStored(t *testing.T, db *gorm.DB, mailbox models.Mailbox) []models.Message {
	t.Helper()
	var messages []models.Message
	if err := db.Where("mailbox_id = ?", mailbox.ID).Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	return messages
}

// A JSON message_id, in_reply_to or references that is not RFC 5322 §3.6.4 msg-id syntax is
// refused 422 validation_failed, naming the field, and nothing is stored: a line break in one
// would otherwise reach every header written from it (#1608). Valid ones are stored as sent.
func TestRESTMAILDeliveryRefusesInvalidMsgIDs(t *testing.T) {
	db, mailbox := restmailStorageFixture(t)
	base := func() map[string]any {
		return map[string]any{
			"from": "sender@external.test", "to": []string{mailbox.Address}, "subject": "ids",
			"body_text": "hello", "date": "Sat, 17 Oct 2026 12:00:00 +0000",
		}
	}

	for _, tc := range []struct {
		name, field, value, reason string
	}{
		{"message_id line break", "message_id", "<a@external.test>\r\nBcc: victim@external.test", "must not contain line breaks or other control characters"},
		{"message_id bare LF", "message_id", "<a@external.test>\nX-Injected: yes", "must not contain line breaks or other control characters"},
		{"message_id without brackets", "message_id", "a@external.test", "must be an RFC 5322 msg-id: <id-left@id-right>"},
		{"message_id two ids", "message_id", "<a@external.test> <b@external.test>", "must be an RFC 5322 msg-id: <id-left@id-right>"},
		{"in_reply_to line break", "in_reply_to", "<p@external.test>\r\nX-Injected: yes", "must not contain line breaks or other control characters"},
		{"references line break", "references", "<r@external.test>\r\n <p@external.test>", "must not contain line breaks or other control characters"},
		{"references comma separated", "references", "<r@external.test>, <p@external.test>", "must be one or more RFC 5322 msg-ids (<id-left@id-right>), separated by spaces"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := base()
			body[tc.field] = tc.value
			rr := restmailMsgIDDelivery(t, db, body)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body)
			}
			var got struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
					Details struct {
						Fields map[string]string `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("%v: %s", err, rr.Body)
			}
			want := map[string]string{tc.field: tc.reason}
			if got.Error.Code != "validation_failed" || got.Error.Message != "Request body failed validation" ||
				len(got.Error.Details.Fields) != 1 || got.Error.Details.Fields[tc.field] != tc.reason {
				t.Errorf("refusal = %s, want validation_failed with fields %v", rr.Body, want)
			}
			if stored := restmailMsgIDStored(t, db, mailbox); len(stored) != 0 {
				t.Errorf("a refused delivery stored %d messages", len(stored))
			}
		})
	}

	t.Run("valid ids are stored unchanged", func(t *testing.T) {
		body := base()
		body["message_id"] = "<valid-1608@external.test>"
		body["in_reply_to"] = "<parent@external.test>"
		body["references"] = "<root@external.test> <parent@external.test>"
		rr := restmailMsgIDDelivery(t, db, body)
		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body)
		}
		stored := restmailMsgIDStored(t, db, mailbox)
		if len(stored) != 1 {
			t.Fatalf("stored %d messages, want 1", len(stored))
		}
		m := stored[0]
		if m.MsgID != "<valid-1608@external.test>" || m.InReplyTo != "<parent@external.test>" ||
			m.References != "<root@external.test> <parent@external.test>" {
			t.Errorf("stored ids = %q, %q, %q; want them as sent", m.MsgID, m.InReplyTo, m.References)
		}
	})

	// An empty identifier is one not sent: message_id is generated, and the others stay empty.
	t.Run("empty ids are not refused", func(t *testing.T) {
		body := base()
		body["subject"] = "empty ids"
		body["message_id"], body["in_reply_to"], body["references"] = "", "", ""
		rr := restmailMsgIDDelivery(t, db, body)
		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body)
		}
		var m models.Message
		if err := db.Where("mailbox_id = ? AND subject = ?", mailbox.ID, "empty ids").First(&m).Error; err != nil {
			t.Fatal(err)
		}
		if mail.ValidateMsgID(m.MsgID) != nil || m.InReplyTo != "" || m.References != "" {
			t.Errorf("stored ids = %q, %q, %q; want a generated Message-ID and nothing else", m.MsgID, m.InReplyTo, m.References)
		}
	})

	// Identifiers read from raw_message keep the MIME parser's path: one that is not msg-id
	// syntax there is not refused by the JSON check.
	t.Run("raw_message identifiers keep the parser path", func(t *testing.T) {
		body := map[string]any{
			"from": "sender@external.test", "to": []string{mailbox.Address},
			"raw_message": "From: sender@external.test\r\nTo: " + mailbox.Address + "\r\nSubject: raw\r\n" +
				"Date: Sat, 17 Oct 2026 12:00:00 +0000\r\nMessage-ID: raw-not-bracketed@external.test\r\n\r\nhello",
		}
		rr := restmailMsgIDDelivery(t, db, body)
		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body)
		}
		var m models.Message
		if err := db.Where("mailbox_id = ? AND subject = ?", mailbox.ID, "raw").First(&m).Error; err != nil {
			t.Fatal(err)
		}
		if m.MsgID != "raw-not-bracketed@external.test" {
			t.Errorf("stored Message-ID = %q, want the parsed header's", m.MsgID)
		}
	})
}

// The patterns restmailDeliverBody states for message_id, in_reply_to and references, which the
// binder enforces and the description publishes, say what mail.ValidateMsgID and
// ValidateMsgIDList say of every nonempty value short of the length cap.
func TestRESTMAILMsgIDPatternsMatchTheValidator(t *testing.T) {
	body := reflect.TypeOf(restmailDeliverBody{})
	patterns := func(name string) []*regexp.Regexp {
		f, ok := body.FieldByName(name)
		if !ok {
			t.Fatalf("restmailDeliverBody has no %s", name)
		}
		var res []*regexp.Regexp
		for _, key := range []string{"pattern", "pattern2"} {
			re, err := regexp.Compile(f.Tag.Get(key))
			if err != nil || f.Tag.Get(key) == "" {
				t.Fatalf("%s's %s does not compile: %v", name, key, err)
			}
			res = append(res, re)
		}
		return res
	}
	matches := func(res []*regexp.Regexp, s string) bool {
		for _, re := range res {
			if !re.MatchString(s) {
				return false
			}
		}
		return true
	}
	values := []string{
		"<a@b>", " <a@b>\t", "<a.b-c+d@e.f>", `<"q \" s"@b>`, "<a@[192.0.2.1]>", "<ü@例え.jp>",
		"<a@b> <c@d>", "<a@b><c@d>", "<a@b>\t <c@d> ",
		"", " ", "a@b", "<a@b", "<ab>", "<@b>", "<a@>", "<a@b@c>", "<a..b@c>", "<.a@c>", "<a.@c>",
		"<a b@c>", "<a@b c>", "<a@[b[c]>", `<"open@b>`, "<a@b>, <c@d>", "<a@b> c@d", "<a(b)@c>",
		"<a@b>\r\nX: y", "<a@b>\n", "<a\x00@b>", "<a@b\x7f>", "<a@b>\u0085", "<a@b> ",
	}
	one, list := patterns("MessageID"), patterns("References")
	if inReplyTo := patterns("InReplyTo"); inReplyTo[0].String() != list[0].String() || inReplyTo[1].String() != list[1].String() {
		t.Errorf("in_reply_to and references state different patterns")
	}
	for _, v := range values {
		// Empty is the field not sent, which the patterns let through and the handler never
		// hands to the validator.
		if got, want := matches(one, v), v == "" || mail.ValidateMsgID(v) == nil; got != want {
			t.Errorf("message_id %q: pattern says %v, ValidateMsgID %v", v, got, want)
		}
		if got, want := matches(list, v), v == "" || mail.ValidateMsgIDList(v) == nil; got != want {
			t.Errorf("references %q: pattern says %v, ValidateMsgIDList %v", v, got, want)
		}
	}
}
