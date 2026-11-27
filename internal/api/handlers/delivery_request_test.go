package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
)

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
