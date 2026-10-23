package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
)

// queueRoutes exercises the production registration with capability checks that pass.
func queueRoutes(h *QueueHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, QueueRouteChecks{
		At: func(string) endpoint.Middleware {
			return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
		},
	})
	return r
}

// Path constraints are enforced by registration before any database access.
func TestQueueRoutesRejectInvalidIDs(t *testing.T) {
	for _, operation := range []struct{ method, suffix string }{
		{http.MethodGet, ""}, {http.MethodDelete, ""},
		{http.MethodPost, "/retry"}, {http.MethodPost, "/bounce"},
	} {
		for _, id := range []string{"0", "-1", "abc", "1.5", "4294967296"} {
			t.Run(operation.method+id+operation.suffix, func(t *testing.T) {
				rr := httptest.NewRecorder()
				queueRoutes(NewQueueHandler(nil)).ServeHTTP(rr, httptest.NewRequest(operation.method, "/api/v1/admin/queue/"+id+operation.suffix, nil))
				if rr.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status %d, want 422: %s", rr.Code, rr.Body)
				}
				var got respond.ValidationErrorResponse
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.Error.Code != "validation_failed" || got.Error.Details.Fields["id"] == "" {
					t.Fatalf("missing id violation: %s", rr.Body)
				}
			})
		}
	}
}

func TestQueueEntryPreservesModelJSON(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	id := uint(42)
	for _, item := range []models.OutboundQueue{
		{},
		{ID: 10, MessageID: &id, Sender: "sender@example.test", Recipient: "recipient@example.test", Domain: "example.test", RawMessage: "secret", BodyType: "8BITMIME", Status: "deferred", Attempts: 2, LastAttempt: &now, NextAttempt: now, LastError: "retry later", LastErrorCode: 451, FailureCategory: models.FailureCategory("greylisted"), MaxRetries: 30, CreatedAt: now, ExpiresAt: now},
	} {
		want, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(queueEntryOf(item))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("API JSON %s differs from model JSON %s", got, want)
		}
	}
}

func TestQueueDeleteMissingAndRetryState(t *testing.T) {
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	item := models.OutboundQueue{Sender: "sender@example.test", Recipient: "recipient@example.test", Domain: "example.test", Status: "delivered"}
	if err := tx.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	handler := queueRoutes(NewQueueHandler(tx))
	path := fmt.Sprintf("/api/v1/admin/queue/%d", item.ID)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path+"/retry", nil))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "Only pending or deferred entries can be retried") {
		t.Fatalf("retry state refusal: %d %s", rr.Code, rr.Body)
	}
	for _, status := range []int{http.StatusNoContent, http.StatusNotFound} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, path, nil))
		if rr.Code != status {
			t.Fatalf("delete: %d, want %d: %s", rr.Code, status, rr.Body)
		}
		if status == http.StatusNotFound && (errorCodeOf(rr) != "not_found" || !strings.Contains(rr.Body.String(), "Queue entry not found")) {
			t.Fatalf("missing-entry refusal: %s", rr.Body)
		}
	}
}

// Every documented bulk validation pair is refused through the registered route.
func TestBulkQueueValidationExamples(t *testing.T) {
	for _, action := range []string{"retry", "bounce", "delete"} {
		method := http.MethodPost
		path := "/api/v1/admin/queue/bulk-" + action
		spec := readBulkDeleteSpec(t)
		if action != "delete" {
			spec = readBulkPostSpec(t, path)
		} else {
			method = http.MethodDelete
		}
		requests, answers := spec.requests(t), spec.responses(t)["422"]
		for name, want := range answers {
			body, err := json.Marshal(requests[name])
			if err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			queueRoutes(NewQueueHandler(nil)).ServeHTTP(rr, httptest.NewRequest(method, path, bytes.NewReader(body)))
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s/%s: %d %s", action, name, rr.Code, rr.Body)
			}
			assertDocumentedBody(t, requests[name], rr, want)
		}
	}
}

func TestQueueStatsPreservesStatusCounters(t *testing.T) {
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
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
	handler := queueRoutes(NewQueueHandler(tx))
	check := func(want map[string]int64) {
		t.Helper()
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/queue/stats", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("stats: %d %s", rr.Code, rr.Body)
		}
		var got struct{ Data map[string]int64 }
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Data, want) {
			t.Fatalf("stats %v, want %v", got.Data, want)
		}
	}
	check(map[string]int64{"pending": 0, "deferred": 0, "delivered": 0, "bounced": 0, "total": 0})
	for _, status := range []string{"pending", "deferred", "delivered", "bounced", "delivering", "expired"} {
		if err := tx.Create(&models.OutboundQueue{Sender: "sender@example.test", Recipient: "recipient@example.test", Domain: "example.test", Status: status}).Error; err != nil {
			t.Fatal(err)
		}
	}
	check(map[string]int64{"pending": 1, "deferred": 1, "delivered": 1, "bounced": 1, "delivering": 1, "expired": 1, "total": 6})
}
