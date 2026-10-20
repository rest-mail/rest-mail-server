package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

func TestListQueue_RejectInvalidStatus(t *testing.T) {
	for _, status := range []string{"", "vertrag-not-in-enum", "PENDING", "pending ", "12345"} {
		t.Run("status="+status, func(t *testing.T) {
			query := url.Values{"status": {status}}
			rr := httptest.NewRecorder()
			// A nil DB proves the refusal happens before any database access.
			NewQueueHandler(nil).ListQueue(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/queue?"+query.Encode(), nil))
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body)
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Details struct {
						Fields map[string]string `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "validation_failed" || body.Error.Details.Fields["status"] == "" {
				t.Fatalf("missing status validation error: %s", rr.Body)
			}
		})
	}
}

func TestListQueue_ContractStatusFilters(t *testing.T) {
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("queue list DB test requires PostgreSQL: %v", err)
	}
	if err := db.AutoMigrate(&models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Where("1 = 1").Delete(&models.OutboundQueue{}).Error; err != nil {
		t.Fatal(err)
	}

	op := listQueryOperations(t)["listQueue"]
	var statuses []string
	for _, param := range op.Parameters {
		if param.Name == "status" {
			statuses = param.Schema.Enum
		}
	}
	if !slices.Equal(statuses, []string{"pending", "delivering", "deferred", "delivered", "bounced", "expired"}) {
		t.Fatalf("unexpected status schema: %v", statuses)
	}
	if _, ok := op.Responses["422"]; !ok {
		t.Fatal("status validation response must be documented")
	}

	var entries []models.OutboundQueue
	for i, status := range statuses {
		for j := range 2 {
			entries = append(entries, models.OutboundQueue{
				Sender: "sender@restmail.test", Recipient: "recipient@example.test",
				Domain: "example.test", Status: status,
				CreatedAt: time.Now().Add(time.Duration(i*2+j) * time.Minute),
			})
		}
	}
	if err := tx.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
	for _, status := range append([]string{""}, statuses...) {
		for _, paginated := range []bool{false, true} {
			name := status
			if paginated {
				name += "/paginated"
			}
			t.Run(name, func(t *testing.T) {
				query := url.Values{}
				if status != "" {
					query.Set("status", status)
				}
				limit, offset := 50, 0
				if paginated {
					limit, offset = 1, 1
					query.Set("limit", "1")
					query.Set("offset", "1")
				}
				rr := httptest.NewRecorder()
				NewQueueHandler(tx).ListQueue(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/queue?"+query.Encode(), nil))
				if rr.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body)
				}
				var body struct {
					Data struct {
						Items  []models.OutboundQueue `json:"items"`
						Total  int                    `json:"total"`
						Limit  int                    `json:"limit"`
						Offset int                    `json:"offset"`
					} `json:"data"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				var want []uint
				for i := len(entries) - 1; i >= 0; i-- {
					if status == "" || entries[i].Status == status {
						want = append(want, entries[i].ID)
					}
				}
				if body.Data.Total != len(want) || body.Data.Limit != limit || body.Data.Offset != offset {
					t.Fatalf("pagination = total %d, limit %d, offset %d; want %d, %d, %d", body.Data.Total, body.Data.Limit, body.Data.Offset, len(want), limit, offset)
				}
				want = want[offset:min(offset+limit, len(want))]
				var got []uint
				for _, entry := range body.Data.Items {
					got = append(got, entry.ID)
				}
				if !slices.Equal(got, want) {
					t.Fatalf("IDs = %v, want %v", got, want)
				}
			})
		}
	}
}
