package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// The typed answers write what the model and the local types they replaced wrote.
func TestComposeResponseJSONParity(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	sent := uint(9)
	scheduled := models.ScheduledSend{ID: 1, AccountID: 2, From: "a@example.test", To: models.JSONB(`["b@example.test"]`),
		Subject: "Later", SendAt: now, Status: "pending", Attempts: 1, Request: models.JSONB(`{"secret":"request"}`),
		SentMessageID: &sent, LastError: "421", CreatedAt: now, UpdatedAt: now}
	type calEventSummary struct {
		UID         string `json:"uid"`
		Method      string `json:"method"`
		Status      string `json:"status"`
		Summary     string `json:"summary"`
		Sequence    int    `json:"sequence"`
		IsCancelled bool   `json:"is_cancelled"`
		MessageID   *uint  `json:"message_id,omitempty"`
		Versions    int    `json:"versions"`
	}
	for _, tc := range []struct {
		name         string
		old, current interface{}
	}{
		{"scheduled", scheduled, scheduledMessageOf(scheduled)},
		{"scheduled, nothing stored", models.ScheduledSend{}, scheduledMessageOf(models.ScheduledSend{})},
		{"calendar event", calEventSummary{"u", "REQUEST", "CONFIRMED", "Meet", 2, false, &sent, 3},
			CalendarEventSummary{"u", "REQUEST", "CONFIRMED", "Meet", 2, false, &sent, 3}},
		{"replied", map[string]string{"status": "sent", "response": "ACCEPTED"}, CalendarReplied{Response: "ACCEPTED", Status: "sent"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old, err := json.Marshal(tc.old)
			if err != nil {
				t.Fatal(err)
			}
			current, err := json.Marshal(tc.current)
			if err != nil {
				t.Fatal(err)
			}
			if string(old) != string(current) {
				t.Fatalf("response JSON changed:\nold %s\nnew %s", old, current)
			}
			if strings.Contains(string(current), "secret") {
				t.Fatalf("stored request answered: %s", current)
			}
		})
	}
}

// What binding refuses, it refuses before anything is looked up; the mobile app's draft
// update, with in_reply_to and references, is bound. Bound, a request without a session is
// the handler's 401.
func TestComposeBindingBeforeDatabase(t *testing.T) {
	routes := composeRoutes(NewMessageHandler(nil, nil, nil, "", nil))
	for _, tc := range []struct{ method, path, body, field string }{
		{http.MethodPost, "/api/v1/messages/draft", `{"to":"a@example.test"}`, "to"},
		{http.MethodPost, "/api/v1/messages/draft", `{"subject":"x","priority":"high"}`, "priority"},
		{http.MethodPut, "/api/v1/messages/draft/0", `{}`, "id"},
		{http.MethodPut, "/api/v1/messages/draft/1", `{"subject":7}`, "subject"},
		{http.MethodDelete, "/api/v1/messages/scheduled/nope", "", "id"},
		{http.MethodPost, "/api/v1/messages/1/calendar-reply", `{"response":1}`, "response"},
		{http.MethodGet, "/api/v1/accounts/0/calendar-events", "", "id"},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			routes.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"`+tc.field+`":`) {
				t.Fatalf("want 422 for %s, got %d %s", tc.field, rr.Code, rr.Body)
			}
		})
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/v1/messages/draft/1", `{"from":"a@example.test","to":["b@example.test"],"cc":[],"subject":"Re: x","body_text":"t","in_reply_to":"<x@y>","references":"<x@y>"}`},
		{http.MethodPost, "/api/v1/messages/draft", `{"from":"a@example.test","to":["b@example.test"],"in_reply_to":"<x@y>","references":"<x@y>"}`},
		{http.MethodGet, "/api/v1/messages/scheduled", ""},
		{http.MethodPost, "/api/v1/messages/1/calendar-reply", `{"response":"accepted","from":"a@example.test"}`},
	} {
		t.Run("bound "+tc.method+tc.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			routes.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("want 401, got %d %s", rr.Code, rr.Body)
			}
		})
	}
}
