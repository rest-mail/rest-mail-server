package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
)

func TestDeliveryStripsOnlyImportFields(t *testing.T) {
	const body = `{"address":"one@example.test","folder":{},"is_answered":42,"is_read":"yes","is_flagged":[],"is_starred":null,"is_draft":{},"received_at":"not a date","mailbox_id":"not an id","priority":1,"recipients_to":{"folder":"kept"}}`
	called := false
	stripDeliveryImportFields(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(got, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 3 || string(fields["address"]) != `"one@example.test"` || string(fields["priority"]) != "1" || string(fields["recipients_to"]) != `{"folder":"kept"}` {
			t.Fatalf("unexpected stripped body: %s", got)
		}
		if r.ContentLength != int64(len(got)) {
			t.Fatalf("content length = %d, want %d", r.ContentLength, len(got))
		}
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if !called {
		t.Fatal("stripped delivery did not reach the binder")
	}
}

func TestDeliveryStrippingPreservesBindingErrors(t *testing.T) {
	for _, test := range []struct {
		body, field string
		status      int
	}{
		{`{"address":`, "bad_request", 400},
		{`[]`, "body", 422},
		{`null`, "body", 422},
		{`{"folder":"Archive","mailbox_id":7}`, "address", 422},
		{`{"address":"one@example.test","folder":"Archive","priority":1}`, "priority", 422},
		{`{"address":"one@example.test","folder":"Archive","raw_message":"invalid base64"}`, "raw_message", 422},
	} {
		t.Run(test.body, func(t *testing.T) {
			w := httptest.NewRecorder()
			deliveryRoute(&MessageHandler{})(w, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(test.body)))
			if w.Code != test.status || !strings.Contains(w.Body.String(), `"`+test.field+`"`) {
				t.Fatalf("got %d %s, want %d naming %s", w.Code, w.Body, test.status, test.field)
			}
		})
	}
}

func TestDeliveryCapsBodyBeforeStripping(t *testing.T) {
	router := chi.NewRouter()
	(&MessageHandler{}).RegisterDelivery(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}, endpoint.Middleware{
		Wrap: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Body = http.MaxBytesReader(w, r.Body, 100)
				next.ServeHTTP(w, r)
			})
		},
	})
	w := httptest.NewRecorder()
	body := `{"address":"one@example.test","folder":"` + strings.Repeat("a", 101) + `"}`
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(body)))
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), `"request_too_large"`) {
		t.Fatalf("body cap bypassed: %d %s", w.Code, w.Body)
	}
}
