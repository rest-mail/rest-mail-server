package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
)

func TestSendIdempotencyDescription(t *testing.T) {
	doc, _ := describedRouters(t).Description("test")
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name     string
				In       string
				Required bool
				Schema   struct {
					Type      string
					MaxLength int
				}
			}
			Responses map[string]any
		}
	}
	if err := json.Unmarshal(encoded, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/messages/send", "/api/v1/messages/draft/{id}/send", "/api/v1/messages/{id}/forward"} {
		operation := parsed.Paths[path]["post"]
		found := false
		for _, parameter := range operation.Parameters {
			if parameter.Name != "Idempotency-Key" {
				continue
			}
			found = true
			if parameter.In != "header" || parameter.Required || parameter.Schema.Type != "string" || parameter.Schema.MaxLength != 255 {
				t.Errorf("%s: wrong header: %+v", path, parameter)
			}
		}
		if !found || operation.Responses["409"] == nil {
			t.Errorf("%s: optional header or conflict response missing", path)
		}
	}
}

func TestSendIdempotencyConflictExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range map[string]string{
		"sendMessage":    "/api/v1/messages/send",
		"sendDraft":      "/api/v1/messages/draft/{id}/send",
		"forwardMessage": "/api/v1/messages/{id}/forward",
	} {
		t.Run(operation, func(t *testing.T) {
			t.Cleanup(filters.SetRateLimitClock(time.Now))
			pair := pairs[operation]["idempotency_conflict"]
			examplepairs.Proven(t, operation, "idempotency_conflict")
			fixture := newCredentialFixture(t)
			key := fmt.Sprint(pair.Request.Header["Idempotency-Key"])
			send := func(path string, body []byte) *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", fixture.session)
				request.Header.Set("Idempotency-Key", key)
				response := httptest.NewRecorder()
				fixture.router.ServeHTTP(response, request)
				return response
			}
			first := send("/api/v1/messages/send", []byte(fmt.Sprintf(`{"from":%q,"to":["far@remote.example"],"body_text":"The first request"}`, fixture.address)))
			if first.Code != 201 {
				t.Fatalf("first send: %d %s", first.Code, first.Body)
			}
			path = strings.ReplaceAll(path, "{id}", fmt.Sprint(pair.Request.Path["id"]))
			response := send(path, pair.Request.Body)
			var got, want any
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
				t.Fatal(err)
			}
			if response.Code != pair.Response.Status || !reflect.DeepEqual(got, want) {
				t.Fatalf("conflict: %d %s; want %d %s", response.Code, response.Body, pair.Response.Status, pair.Response.Body)
			}
			for _, model := range []any{&models.Message{}, &models.OutboundQueue{}, &models.SendIdempotency{}} {
				var count int64
				if err := fixture.db.Model(model).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Errorf("%T count=%d want=1", model, count)
				}
			}
		})
	}
}
