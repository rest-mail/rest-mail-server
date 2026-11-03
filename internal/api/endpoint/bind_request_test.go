package endpoint

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// BindRequest is the binder with another body: the parameters still come from the request.
func TestBindRequestReadsTheGivenBody(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}
	type request struct {
		ID   uint32 `path:"id" minimum:"1"`
		Body body   `body:""`
	}
	bound := func(id, contentType, given string) (request, error) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"the request's own"}`))
		r.Header.Set("Content-Type", contentType)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", id)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
		var in request
		err := BindRequest(r, &in, []byte(given))
		return in, err
	}
	in, err := bound("7", "application/json", `{"name":"given"}`)
	if err != nil || in.ID != 7 || in.Body.Name != "given" {
		t.Fatalf("bound %+v, %v", in, err)
	}
	var refusal *Error
	if _, err := bound("0", "application/json", `{"name":"given"}`); !errors.As(err, &refusal) || refusal.Fields["id"] == "" {
		t.Fatalf("a bad parameter: %v", err)
	}
	if _, err := bound("7", "application/json", `{`); !errors.As(err, &refusal) || refusal.Status != http.StatusBadRequest {
		t.Fatalf("a bad body: %v", err)
	}
	if _, err := bound("7", "text/plain", `{"name":"given"}`); !errors.As(err, &refusal) || !strings.Contains(refusal.Message, "Unsupported media type") {
		t.Fatalf("a bad media type: %v", err)
	}
}
