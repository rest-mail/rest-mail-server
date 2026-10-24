package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
)

// deliveryRoute is inbound delivery through its registration, without its body cap.
func deliveryRoute(h *MessageHandler) http.HandlerFunc {
	routes := chi.NewRouter()
	h.RegisterDelivery(endpoint.Routes{Router: routes, Registry: endpoint.NewRegistry()},
		endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }})
	return routes.ServeHTTP
}

// addressCheckRoute is the recipient check through its registration.
func addressCheckRoute(h *MailboxHandler) http.HandlerFunc {
	routes := chi.NewRouter()
	h.RegisterAddressCheck(endpoint.Routes{Router: routes, Registry: endpoint.NewRegistry()})
	return routes.ServeHTTP
}

// The address check writes the bytes the maps it replaced wrote: the gateways read them.
func TestAddressCheckJSONParity(t *testing.T) {
	for _, tc := range []struct {
		old     map[string]interface{}
		current AddressCheck
	}{
		{map[string]interface{}{"exists": false, "address": "x@remote.test"}, AddressCheck{Address: "x@remote.test"}},
		{map[string]interface{}{"exists": true, "mailbox_id": uint(7), "address": "a@b.test"}, AddressCheck{Address: "a@b.test", Exists: true, MailboxID: 7}},
	} {
		old, _ := json.Marshal(tc.old)
		current, _ := json.Marshal(tc.current)
		if string(old) != string(current) {
			t.Fatalf("response JSON changed:\nold %s\nnew %s", old, current)
		}
	}
}

// What binding refuses, and what the handlers refuse first, is answered with no database: a
// body that is not JSON, a field the gateway never sends, a wrong type, no mailbox named, a
// folder the folder rule refuses, and a recipient check without an address.
func TestDeliveryAndAddressCheckBeforeDatabase(t *testing.T) {
	deliver := deliveryRoute(&MessageHandler{})
	for _, tc := range []struct {
		body   string
		status int
		words  string
	}{
		{`{"address":`, http.StatusBadRequest, `"bad_request"`},
		{`{"address":"a@b.test","priority":1}`, http.StatusUnprocessableEntity, `"priority":"not allowed"`},
		{`{"mailbox_id":"seven"}`, http.StatusUnprocessableEntity, `"mailbox_id":`},
		{`{"address":"a@b.test","raw_message":"not base64!"}`, http.StatusUnprocessableEntity, `"raw_message":`},
		{`{"sender":"x@remote.test"}`, http.StatusBadRequest, `mailbox_id or address required`},
		// The folder follows the folder rule, before the mailbox is looked up (#759).
		{`{"address":"a@b.test","folder":"Archive/../Other"}`, http.StatusUnprocessableEntity, `"folder":"must not contain \"..\""`},
		{`{"address":"a@b.test","folder":" Archive"}`, http.StatusUnprocessableEntity, `"folder":"must not begin or end with a space"`},
		{`{"address":"a@b.test","folder":"a\u0007b"}`, http.StatusUnprocessableEntity, `"folder":"must not contain a control character"`},
	} {
		rr := httptest.NewRecorder()
		deliver(rr, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(tc.body)))
		if rr.Code != tc.status || !strings.Contains(rr.Body.String(), tc.words) {
			t.Errorf("%s: want %d with %s, got %d %s", tc.body, tc.status, tc.words, rr.Code, rr.Body)
		}
	}
	check := addressCheckRoute(&MailboxHandler{})
	for target, want := range map[string]int{
		"/api/mailboxes":          http.StatusUnprocessableEntity,
		"/api/mailboxes?address=": http.StatusBadRequest,
	} {
		rr := httptest.NewRecorder()
		check(rr, httptest.NewRequest(http.MethodGet, target, nil))
		if rr.Code != want {
			t.Errorf("%s: want %d, got %d %s", target, want, rr.Code, rr.Body)
		}
	}
}

// Delivery is described with both its answers: the message made, and the pipeline's
// quarantine or discard.
func TestDeliveryDescribesEachOutcome(t *testing.T) {
	reg := endpoint.NewRegistry()
	(&MessageHandler{}).RegisterDelivery(endpoint.Routes{Router: chi.NewRouter(), Registry: reg},
		endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }})
	got := map[int]reflect.Type{}
	for _, response := range reg.Endpoints()[0].Responses {
		if response.Status < 300 {
			got[response.Status] = response.Type
		}
	}
	want := map[int]reflect.Type{
		http.StatusCreated: reflect.TypeOf(endpoint.Data[Message]{}),
		http.StatusOK:      reflect.TypeOf(endpoint.Data[DeliveryProcessed]{}),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delivery answers %v, want %v", got, want)
	}
}
