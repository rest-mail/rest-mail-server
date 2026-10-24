package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
)

// composeRoute is the compose routes, sending among them, through their registration.
func composeRoute(h *MessageHandler) http.HandlerFunc {
	routes := chi.NewRouter()
	h.RegisterCompose(endpoint.Routes{Router: routes, Registry: endpoint.NewRegistry()})
	return routes.ServeHTTP
}

// sendingAs is r with a session for accountID.
func sendingAs(r *http.Request, accountID uint) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: accountID}))
}

// A send body is bound before anything else, and from and to are still the handler's, with
// their 400s: a handler with no database proves nothing further is reached.
func TestSendBindingAndFirstChecksBeforeDatabase(t *testing.T) {
	route := composeRoute(&MessageHandler{})
	for _, tc := range []struct {
		path, body string
		status     int
		words      string
	}{
		{"/api/v1/messages/send", `{"from":5,"to":["b@example.net"]}`, http.StatusUnprocessableEntity, `"from":"must be a string"`},
		{"/api/v1/messages/send", `{"from":"a@example.com","to":["b@example.net"],"priority":"high"}`, http.StatusUnprocessableEntity, `"priority":"not allowed"`},
		{"/api/v1/messages/send", `{"from":"a@example.com","to":"b@example.net"}`, http.StatusUnprocessableEntity, `"to":"must be an array"`},
		{"/api/v1/messages/send", `{"from":`, http.StatusBadRequest, `"bad_request"`},
		{"/api/v1/messages/send", `{"to":["b@example.net"]}`, http.StatusBadRequest, `from is required`},
		{"/api/v1/messages/send", `{"from":"a@example.com"}`, http.StatusBadRequest, `to is required and must not be empty`},
		{"/api/v1/messages/send", `{"from":"a@example.com","to":["b@example.net\r\nRCPT TO:<x@evil.test>"]}`, http.StatusBadRequest, `invalid recipient address in to`},
		{"/api/v1/messages/0/forward", `{"from":"a@example.com","to":["b@example.net"]}`, http.StatusUnprocessableEntity, `"id":`},
		{"/api/v1/messages/1/forward", `{"from":"a@example.com","cc":["b@example.net"]}`, http.StatusUnprocessableEntity, `"cc":"not allowed"`},
		{"/api/v1/messages/draft/0/send", ``, http.StatusUnprocessableEntity, `"id":`},
	} {
		t.Run(tc.path+" "+tc.body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			route(rr, sendingAs(httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)), 1))
			if rr.Code != tc.status || !strings.Contains(rr.Body.String(), tc.words) {
				t.Fatalf("want %d with %s, got %d %s", tc.status, tc.words, rr.Code, rr.Body)
			}
		})
	}
	// Without a session, nothing is looked at.
	rr := httptest.NewRecorder()
	route(rr, httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", strings.NewReader(`{}`)))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no session: want 401, got %d %s", rr.Code, rr.Body)
	}
}

// A send is described with each of its answers in its own status: sent, scheduled, discarded.
// A draft's send and a forward cannot be scheduled.
func TestSendDescribesEachOutcome(t *testing.T) {
	reg := endpoint.NewRegistry()
	(&MessageHandler{}).RegisterCompose(endpoint.Routes{Router: chi.NewRouter(), Registry: reg})
	want := map[string]map[int]reflect.Type{
		"sendMessage": {
			http.StatusCreated:  reflect.TypeOf(endpoint.Data[Message]{}),
			http.StatusAccepted: reflect.TypeOf(endpoint.Data[ScheduledMessage]{}),
			http.StatusOK:       reflect.TypeOf(endpoint.Data[SendDiscarded]{}),
		},
		"sendDraft": {
			http.StatusCreated: reflect.TypeOf(endpoint.Data[Message]{}),
			http.StatusOK:      reflect.TypeOf(endpoint.Data[SendDiscarded]{}),
		},
		"forwardMessage": {
			http.StatusCreated: reflect.TypeOf(endpoint.Data[Message]{}),
			http.StatusOK:      reflect.TypeOf(endpoint.Data[SendDiscarded]{}),
		},
	}
	for _, op := range reg.Endpoints() {
		outcomes, ok := want[op.OperationID]
		if !ok {
			continue
		}
		got := map[int]reflect.Type{}
		for _, response := range op.Responses {
			if response.Status < 300 {
				got[response.Status] = response.Type
			}
		}
		if !reflect.DeepEqual(got, outcomes) {
			t.Errorf("%s answers %v, want %v", op.OperationID, got, outcomes)
		}
		delete(want, op.OperationID)
	}
	if len(want) > 0 {
		t.Errorf("not registered: %v", want)
	}
}
