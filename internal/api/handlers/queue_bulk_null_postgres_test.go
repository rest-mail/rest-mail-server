package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestBulkQueueNullFilterPreservesPostgresRows(t *testing.T) {
	db := queueMutationPostgres(t)
	for _, action := range []string{"retry", "bounce", "delete"} {
		t.Run(action, func(t *testing.T) {
			transaction := db.Begin()
			if transaction.Error != nil {
				t.Fatal(transaction.Error)
			}
			t.Cleanup(func() { _ = transaction.Rollback().Error })
			entries := []models.OutboundQueue{
				{Sender: "sender@null-filter.test", Recipient: "pending@null-filter.test", Domain: "null-filter.test", Status: "pending"},
				{Sender: "sender@null-filter.test", Recipient: "deferred@null-filter.test", Domain: "null-filter.test", Status: "deferred"},
				{Sender: "sender@null-filter.test", Recipient: "delivered@null-filter.test", Domain: "null-filter.test", Status: "delivered"},
			}
			if err := transaction.Create(&entries).Error; err != nil {
				t.Fatal(err)
			}
			var before []models.OutboundQueue
			if err := transaction.Order("id").Find(&before).Error; err != nil {
				t.Fatal(err)
			}
			for _, body := range []string{
				`{"filter":null}`,
				`{"ids":[],"filter":null}`,
				fmt.Sprintf(`{"ids":[%d],"filter":null}`, entries[0].ID),
			} {
				method := http.MethodPost
				if action == "delete" {
					method = http.MethodDelete
				}
				response := httptest.NewRecorder()
				queueRoutes(NewQueueHandler(transaction)).ServeHTTP(response, httptest.NewRequest(method, "/api/v1/admin/queue/bulk-"+action, strings.NewReader(body)))
				if response.Code != http.StatusUnprocessableEntity {
					t.Fatalf("%s: status %d, want 422: %s", body, response.Code, response.Body)
				}
				var refusal struct {
					Error struct {
						Code    string `json:"code"`
						Details struct {
							Fields map[string]string `json:"fields"`
						} `json:"details"`
					} `json:"error"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &refusal); err != nil {
					t.Fatal(err)
				}
				if refusal.Error.Code != "validation_failed" || !reflect.DeepEqual(refusal.Error.Details.Fields, map[string]string{"filter": "must not be null"}) {
					t.Fatalf("%s: unexpected refusal: %s", body, response.Body)
				}
				var after []models.OutboundQueue
				if err := transaction.Order("id").Find(&after).Error; err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("%s: refused bulk %s changed queue rows: before=%+v after=%+v", body, action, before, after)
				}
			}
		})
	}
}
