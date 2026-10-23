package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

func TestBulkQueue_RejectInvalidAddressFilters(t *testing.T) {
	h := NewQueueHandler(nil)
	for _, name := range []string{"retry", "bounce", "delete"} {
		for _, field := range []string{"sender", "recipient"} {
			for _, value := range []string{`123`, `true`, `[]`, `{}`} {
				t.Run(name+"/"+field+"/"+value, func(t *testing.T) {
					body := fmt.Sprintf(`{"filter":{"%s":%s}}`, field, value)
					rr := httptest.NewRecorder()
					// A nil DB proves malformed filters are refused before mutation.
					method := http.MethodPost
					if name == "delete" {
						method = http.MethodDelete
					}
					queueRoutes(h).ServeHTTP(rr, httptest.NewRequest(method, "/api/v1/admin/queue/bulk-"+name, strings.NewReader(body)))
					if rr.Code != http.StatusUnprocessableEntity {
						t.Fatalf("status %d, want 422: %s", rr.Code, rr.Body)
					}
					var got struct {
						Error struct {
							Code, Message string
							Details       struct{ Fields map[string]string }
						}
					}
					if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if got.Error.Code != "validation_failed" || got.Error.Details.Fields["filter."+field] != "must be a string" {
						t.Fatalf("unexpected refusal: %s", rr.Body)
					}
				})
			}
		}
	}
}

func TestBulkQueue_AddressFiltersSelectOnlyMatchingEntries(t *testing.T) {
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("bulk queue filters require PostgreSQL: %v", err)
	}
	if err := db.AutoMigrate(&models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"retry", "bounce", "delete"} {
		for _, tc := range []struct {
			name   string
			filter map[string]string
			ids    bool
			match  func(models.OutboundQueue) bool
		}{
			{"sender", map[string]string{"sender": `sender%_\`}, false,
				func(q models.OutboundQueue) bool { return strings.Contains(strings.ToLower(q.Sender), `sender%_\`) }},
			{"recipient", map[string]string{"recipient": `recipient%_\`}, false,
				func(q models.OutboundQueue) bool {
					return strings.Contains(strings.ToLower(q.Recipient), `recipient%_\`)
				}},
			{"both", map[string]string{"sender": `sender%_\`, "recipient": `recipient%_\`}, false,
				func(q models.OutboundQueue) bool {
					return q.Sender == `Sender%_\@example.test` && q.Recipient == `Recipient%_\@example.test`
				}},
			{"combined_with_status_and_domain", map[string]string{"sender": `sender%_\`, "recipient": `recipient%_\`, "status": "deferred", "domain": "example.test"}, false,
				func(q models.OutboundQueue) bool {
					return q.Sender == `Sender%_\@example.test` && q.Recipient == `Recipient%_\@example.test` && q.Status == "deferred" && q.Domain == "example.test"
				}},
			{"no_match", map[string]string{"sender": "absent"}, false, func(models.OutboundQueue) bool { return false }},
			{"empty_address_fields", map[string]string{"sender": "", "recipient": "", "domain": "example.test"}, false,
				func(q models.OutboundQueue) bool { return q.Domain == "example.test" }},
			{"ids_take_precedence", map[string]string{"sender": "absent", "recipient": "absent"}, true, nil},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				tx := db.Begin()
				if tx.Error != nil {
					t.Fatal(tx.Error)
				}
				t.Cleanup(func() { tx.Rollback() })
				if err := tx.Exec("LOCK TABLE outbound_queue IN EXCLUSIVE MODE").Error; err != nil {
					t.Fatal(err)
				}
				if err := tx.Where("1 = 1").Delete(&models.OutboundQueue{}).Error; err != nil {
					t.Fatal(err)
				}
				var entries []models.OutboundQueue
				for _, status := range []string{"deferred", "pending", "delivering", "delivered", "bounced", "expired"} {
					entries = append(entries, models.OutboundQueue{
						Sender: `Sender%_\@example.test`, Recipient: `Recipient%_\@example.test`,
						Domain: "example.test", Status: status, NextAttempt: time.Now().Add(24 * time.Hour),
					})
				}
				entries = append(entries,
					models.OutboundQueue{Sender: "other@example.test", Recipient: `Recipient%_\@example.test`, Domain: "example.test", Status: "deferred"},
					models.OutboundQueue{Sender: `Sender%_\@example.test`, Recipient: "other@example.test", Domain: "example.test", Status: "deferred"},
					models.OutboundQueue{Sender: `Sender%_\@example.test`, Recipient: `Recipient%_\@example.test`, Domain: "other.test", Status: "deferred"},
					// These must not match literal LIKE metacharacters in the filters.
					models.OutboundQueue{Sender: "SenderXX@example.test", Recipient: "RecipientXX@example.test", Domain: "example.test", Status: "deferred"},
				)
				if err := tx.Create(&entries).Error; err != nil {
					t.Fatal(err)
				}
				// Re-read to compare database timestamp precision consistently.
				if err := tx.Order("id").Find(&entries).Error; err != nil {
					t.Fatal(err)
				}
				body := map[string]interface{}{"filter": tc.filter}
				if tc.ids {
					body["ids"] = []uint{entries[0].ID}
				}
				encoded, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				h := NewQueueHandler(tx)
				handle := queueRoutes(h).ServeHTTP
				method := http.MethodPost
				if action == "delete" {
					method = http.MethodDelete
				}
				rr := httptest.NewRecorder()
				handle(rr, httptest.NewRequest(method, "/api/v1/admin/queue/bulk-"+action, strings.NewReader(string(encoded))))
				if rr.Code != http.StatusOK {
					t.Fatalf("status %d, want 200: %s", rr.Code, rr.Body)
				}
				var got struct{ Data struct{ Affected int } }
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				var remaining []models.OutboundQueue
				if err := tx.Find(&remaining).Error; err != nil {
					t.Fatal(err)
				}
				byID := map[uint]models.OutboundQueue{}
				for _, entry := range remaining {
					byID[entry.ID] = entry
				}
				wantAffected := 0
				for _, before := range entries {
					selected := before.ID == entries[0].ID
					if !tc.ids {
						selected = tc.match(before)
					}
					if action == "retry" {
						selected = selected && (before.Status == "deferred" || before.Status == "pending")
					} else if action == "bounce" {
						selected = selected && (before.Status == "deferred" || before.Status == "pending" || before.Status == "delivering")
					}
					after, exists := byID[before.ID]
					if !selected {
						if !exists || !reflect.DeepEqual(before, after) {
							t.Errorf("unselected entry %d changed: before %+v, after %+v", before.ID, before, after)
						}
						continue
					}
					wantAffected++
					switch action {
					case "delete":
						if exists {
							t.Errorf("selected entry %d was not deleted", before.ID)
						}
					case "retry":
						if !exists || after.Status != "pending" || after.NextAttempt.After(time.Now()) {
							t.Errorf("selected entry %d was not retried: %+v", before.ID, after)
						}
					case "bounce":
						if !exists || after.Status != "bounced" || after.LastError != "manually bounced by admin (bulk)" {
							t.Errorf("selected entry %d was not bounced: %+v", before.ID, after)
						}
					}
				}
				if got.Data.Affected != wantAffected {
					t.Errorf("affected %d, want %d", got.Data.Affected, wantAffected)
				}
			})
		}
	}
}
