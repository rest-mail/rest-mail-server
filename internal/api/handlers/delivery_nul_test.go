package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestDeliveryRejectsNULBeforeStorage(t *testing.T) {
	database := openMsgDeleteTestDB(t)
	fields := []string{"address", "sender", "sender_name", "subject", "body_text", "body_html", "message_id", "in_reply_to", "references", "client_ip", "helo_name", "tls_version", "raw_message", "recipients_to", "recipients_cc"}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			transaction := database.Begin()
			t.Cleanup(func() { transaction.Rollback() })
			fixture := seedMsgDeleteFixture(t, transaction, 0)
			body := map[string]any{"address": fixture.mailbox.Address}
			body[field] = "before\x00after"
			if field == "raw_message" {
				body[field] = base64.StdEncoding.EncodeToString([]byte("Subject: Hello\r\n\r\nbefore\x00after"))
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			deliveryRoute(NewMessageHandler(transaction, nil, nil, "", nil))(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(string(encoded))))
			var response struct {
				Error struct {
					Code    string `json:"code"`
					Details struct {
						Fields map[string]string `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != 422 || response.Error.Code != "validation_failed" || response.Error.Details.Fields[field] == "" {
				t.Fatalf("want 422 validation_failed naming %s, got %d %s", field, recorder.Code, recorder.Body)
			}
			if count := countMessages(t, transaction, fixture.mailbox.ID); count != 0 {
				t.Fatalf("refused delivery stored %d messages", count)
			}
			if used := quotaUsedBytes(t, transaction, fixture.mailbox.ID); used != 0 {
				t.Fatalf("refused delivery changed quota to %d", used)
			}
		})
	}
}

func TestDeliveryRejectsNestedJSONNULBeforeDatabase(t *testing.T) {
	for _, field := range []string{"recipients_to", "recipients_cc"} {
		for _, value := range []string{`"\u0000"`, `["safe",{"name":"bad\u0000"}]`, `{"bad\u0000":"safe"}`, `{"nested":["\u0000"]}`, `[1e10000,"\u0000"]`} {
			t.Run(field+value, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				body := fmt.Sprintf(`{"address":"one@example.test",%q:%s}`, field, value)
				deliveryRoute(&MessageHandler{})(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(body)))
				if recorder.Code != 422 || !strings.Contains(recorder.Body.String(), `"`+field+`":`) {
					t.Fatalf("want 422 naming %s, got %d %s", field, recorder.Code, recorder.Body)
				}
			})
		}
	}
}

func TestMessageImportFolderRuleBeforeDatabase(t *testing.T) {
	for _, folder := range []string{" ", "\t", " Archive", "Archive ", "Archive/../Other", "bad\x00folder", "bad\x7ffolder", "bad\u0085folder", "\u00a0Archive", "Archive\u3000", strings.Repeat("a", 256), strings.Repeat("雪", 86)} {
		t.Run(fmt.Sprintf("%q", folder), func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"raw_message": []byte("Subject: old\r\n\r\nbody"), "internal_date": "2021-01-02T03:04:05Z", "folder": folder})
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			importMessageRoutes(NewMessageImportHandler(nil, 1024, nil, nil)).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/accounts/1/messages/import", strings.NewReader(string(body))))
			if recorder.Code != 422 || !strings.Contains(recorder.Body.String(), `"folder":`) {
				t.Fatalf("want 422 naming folder, got %d %s", recorder.Code, recorder.Body)
			}
		})
	}
}

func TestDeliveryPreservesNonNULContent(t *testing.T) {
	database := openMsgDeleteTestDB(t)
	transaction := database.Begin()
	t.Cleanup(func() { transaction.Rollback() })
	fixture := seedMsgDeleteFixture(t, transaction, 0)
	bodyText := "line one\nline two\t雪\\u0000"
	raw := "Subject: Hello\r\n\r\n" + bodyText
	body, err := json.Marshal(map[string]any{"address": fixture.mailbox.Address, "body_text": bodyText, "raw_message": []byte(raw), "recipients_to": []string{`literal\u0000`}})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	deliveryRoute(NewMessageHandler(transaction, nil, nil, "", nil))(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(string(body))))
	if recorder.Code != 201 {
		t.Fatalf("want 201, got %d %s", recorder.Code, recorder.Body)
	}
	var message models.Message
	if err := transaction.Where("mailbox_id = ?", fixture.mailbox.ID).First(&message).Error; err != nil {
		t.Fatal(err)
	}
	if message.BodyText != bodyText || !strings.Contains(message.RawMessage, bodyText) {
		t.Fatalf("delivery changed content: body %q, raw %q", message.BodyText, message.RawMessage)
	}
}
