package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestRESTMAILDeliveryPublishesCommittedArrivals(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "committed"
		if failed {
			name = "rolled-back"
		}
		t.Run(name, func(t *testing.T) {
			database, mailbox := restmailStorageFixture(t)
			broker := NewSSEBroker()
			stream := broker.Subscribe(mailbox.ID)
			defer broker.Unsubscribe(mailbox.ID, stream)
			messages := NewMessageHandler(database, broker, nil, "", nil)
			notifier := &recordingNotifier{}
			messages.SetNewMailNotifier(notifier)
			handler := NewRestmailHandler(database, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"192.0.2.10/32"}})
			handler.SetLocalDelivery(messages)
			if failed {
				if err := database.Callback().Create().Before("gorm:create").Register("restmail_events_failure", func(transaction *gorm.DB) {
					if transaction.Statement.Table == "messages" {
						_ = transaction.AddError(errors.New("injected storage failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			body, err := json.Marshal(map[string]any{"from": "sender@external.test", "to": []string{mailbox.Address}, "subject": "Arrival", "body_text": "PRIVATE BODY"})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, restmailMessagesPath, strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			request.RemoteAddr = "192.0.2.10:1234"
			response := httptest.NewRecorder()
			restmailRoutes(handler).ServeHTTP(response, request)
			if failed {
				if response.Code != http.StatusUnprocessableEntity || len(stream) != 0 || len(notifier.mailboxes) != 0 {
					t.Fatalf("failed delivery published an arrival: status=%d events=%d notifications=%v", response.Code, len(stream), notifier.mailboxes)
				}
				return
			}
			if response.Code != http.StatusCreated {
				t.Fatalf("delivery: %d %s", response.Code, response.Body.String())
			}
			if len(stream) != 2 {
				t.Fatalf("committed delivery published %d events, want new_message and folder_update", len(stream))
			}
			arrival, folders := (<-stream).Event, (<-stream).Event
			encoded, err := json.Marshal(arrival.Data)
			if err != nil {
				t.Fatal(err)
			}
			if arrival.Type != "new_message" || !strings.Contains(string(encoded), `"subject":"Arrival"`) || strings.Contains(string(encoded), "PRIVATE BODY") {
				t.Fatalf("arrival metadata: %s %s", arrival.Type, encoded)
			}
			counts, ok := folders.Data.(map[string]interface{})
			if folders.Type != "folder_update" || !ok || counts["folder"] != "INBOX" || counts["unread_count"] != int64(1) {
				t.Fatalf("folder counts: %+v", folders)
			}
			if len(notifier.mailboxes) != 1 || notifier.mailboxes[0] != mailbox.ID {
				t.Fatalf("push notifications: %v", notifier.mailboxes)
			}
		})
	}
}
