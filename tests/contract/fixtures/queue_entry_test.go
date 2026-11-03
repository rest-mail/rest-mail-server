package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestQueueEntryTargetsSurviveRepeatedCoverageWithoutOtherFixtures(t *testing.T) {
	database := fixtureDB(t)
	if err := database.Create(&models.Domain{Name: "restmail.test", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := prepareQueueFixtures(database, "restmail.test"); err != nil {
		t.Fatal(err)
	}
	var originals []models.OutboundQueue
	if err := database.Order("id").Find(&originals).Error; err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	handlers.NewQueueHandler(database).Register(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}, handlers.QueueRouteChecks{
		At: func(string) endpoint.Middleware {
			return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
		},
	})
	seen := map[uint]bool{}
	for range 2 {
		for _, operation := range []struct {
			method string
			suffix string
			status int
		}{{http.MethodDelete, "", 204}, {http.MethodGet, "", 200}, {http.MethodPost, "/retry", 200}, {http.MethodPost, "/bounce", 200}} {
			id, err := prepareRetryFixture(database, "restmail.test")
			if err != nil {
				t.Fatal(err)
			}
			if seen[id] {
				t.Fatal("coverage reused a target:", id)
			}
			seen[id] = true
			var item models.OutboundQueue
			if err := database.First(&item, id).Error; err != nil {
				t.Fatal(err)
			}
			if item.Status != "deferred" || !item.NextAttempt.After(time.Now()) || !item.ExpiresAt.After(item.NextAttempt) || item.Domain != "retry.invalid" {
				t.Fatalf("target is not held safely: %+v", item)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(operation.method, fmt.Sprintf("/api/v1/admin/queue/%d%s", id, operation.suffix), nil))
			if response.Code != operation.status {
				t.Fatalf("%s %s answered %d, want %d: %s", operation.method, operation.suffix, response.Code, operation.status, response.Body)
			}
		}
	}
	for _, original := range originals {
		var preserved models.OutboundQueue
		if err := database.First(&preserved, original.ID).Error; err != nil {
			t.Fatal(err)
		}
		if preserved.Status != original.Status || !preserved.NextAttempt.Equal(original.NextAttempt) || preserved.RawMessage != original.RawMessage {
			t.Fatal("coverage changed a preexisting queue fixture:", original.ID)
		}
	}
	for _, model := range []interface{}{&models.Quarantine{}, &models.Suppression{}, &models.Mailbox{}, &models.Message{}} {
		var count int64
		if err := database.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("queue target setup changed %T: count %d, error %v", model, count, err)
		}
	}
}
