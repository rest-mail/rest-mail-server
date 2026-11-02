package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestRetryFixtureReplenishedAfterEmptyFilterBulkBounce(t *testing.T) {
	database := fixtureDB(t)
	if err := database.Create(&models.Domain{Name: "restmail.test", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	id, err := prepareRetryFixture(database, "restmail.test")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	handlers.NewQueueHandler(database).Register(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}, handlers.QueueRouteChecks{
		At: func(string) endpoint.Middleware {
			return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
		},
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/queue/bulk-bounce", strings.NewReader(`{"filter":{}}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("empty-filter bounce: %d %s", response.Code, response.Body.String())
	}
	if status := retry(t, database, id); status != http.StatusBadRequest {
		t.Fatalf("retry after empty-filter bulk bounce: %d, want 400", status)
	}
	if err := prepareQueueFixtures(database, "restmail.test"); err != nil {
		t.Fatal(err)
	}
	var fresh models.OutboundQueue
	if err := database.Where("recipient = ? AND status = ?", "contract-retry@retry.invalid", "deferred").Order("id DESC").First(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.ID == id {
		t.Fatal("replenishment reused the bulk-bounced entry")
	}
	if status := retry(t, database, fresh.ID); status != http.StatusOK {
		t.Fatalf("retry replenished entry %d: %d, want 200", fresh.ID, status)
	}
	var original models.OutboundQueue
	if err := database.First(&original, id).Error; err != nil {
		t.Fatal(err)
	}
	if original.Status != "bounced" {
		t.Fatalf("replenishment undid the bulk action: original status %q", original.Status)
	}
}
