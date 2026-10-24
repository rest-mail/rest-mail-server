package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
)

func restmailRoutes(h *RestmailHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()},
		endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }})
	return r
}

// restmailRoute serves a request made to any path through the registered route at path.
func restmailRoute(h *RestmailHandler, path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Path = path
		clone.URL = &u
		restmailRoutes(h).ServeHTTP(w, clone)
	}
}

// The capabilities are the bytes the map they replaced wrote.
func TestRestmailCapabilities_Bytes(t *testing.T) {
	rr := httptest.NewRecorder()
	restmailRoutes(NewRestmailHandler(nil, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})).
		ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/restmail/capabilities", nil))
	want := `{"data":{"features":["delivery","recipient-check"],"protocol":"RESTMAIL","version":"1.0"}}`
	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != want {
		t.Fatalf("got %d %s, want 200 %s", rr.Code, rr.Body, want)
	}
}

// A recipient check without an address is refused before any lookup: the handler has no
// database.
func TestRestmailCheckMailbox_AddressRequired(t *testing.T) {
	h := restmailRoutes(NewRestmailHandler(nil, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{}))
	for _, query := range []string{"", "?address="} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/restmail/mailboxes"+query, nil))
		want := parseShapeJSON(t, `{"error":{"code":"validation_failed","message":"Query parameters failed validation","details":{"fields":{"address":"address query parameter required"}}}}`)
		if rr.Code != http.StatusUnprocessableEntity || !reflect.DeepEqual(parseShapeJSON(t, rr.Body.String()), want) {
			t.Errorf("%q: got %d %s, want the address refused", query, rr.Code, rr.Body)
		}
	}
}

// A delivery body is bound before anything is looked up, verified or stored.
func TestRestmailDeliver_Binding(t *testing.T) {
	h := restmailRoutes(NewRestmailHandler(nil, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{}))
	for _, tc := range []struct {
		body   string
		fields map[string]string
	}{
		{`{"to":"eve@restmail.test","colour":"red"}`, map[string]string{"to": "must be an array", "colour": "not allowed"}},
		{`{"to":["eve@restmail.test"],"headers":["Subject: hi"],"from":7}`, map[string]string{"headers": "must be an object", "from": "must be a string"}},
		{`{"recipients":[1],"raw_message":null}`, map[string]string{"recipients[0]": "must be a string", "raw_message": "must not be null"}},
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, restmailMessagesPath, bytes.NewBufferString(tc.body)))
		fields := map[string]interface{}{}
		for k, v := range tc.fields {
			fields[k] = v
		}
		want := map[string]interface{}{"error": map[string]interface{}{"code": "validation_failed",
			"message": "Request body failed validation", "details": map[string]interface{}{"fields": fields}}}
		if rr.Code != http.StatusUnprocessableEntity || !reflect.DeepEqual(parseShapeJSON(t, rr.Body.String()), want) {
			t.Errorf("%s: got %d %s, want the fields %v refused", tc.body, rr.Code, rr.Body, tc.fields)
		}
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, restmailMessagesPath, bytes.NewBufferString(`{"to":`)))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), `"bad_request"`) {
		t.Errorf("a body that is not JSON: got %d %s, want 400 bad_request", rr.Code, rr.Body)
	}
}
