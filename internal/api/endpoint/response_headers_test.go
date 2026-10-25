package endpoint

import (
	"net/http"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"
)

func TestRouterResponseHeadersDoNotLeakAcrossListeners(t *testing.T) {
	registry := NewRegistry()
	shared := oapifly.Middleware{Responses: []oapifly.EndpointResponse{{Status: http.StatusUnauthorized}}}
	public := Routes{Router: chi.NewRouter(), Registry: registry, Applied: []oapifly.Middleware{shared},
		ResponseHeaders: func(string) map[string]oapifly.ResponseHeader {
			return map[string]oapifly.ResponseHeader{"X-Frame-Options": {Description: "DENY", Required: true}}
		}}
	internal := Routes{Router: chi.NewRouter(), Registry: registry, Applied: []oapifly.Middleware{shared}}
	handler := func(*http.Request, struct{}) (Empty, error) { return Empty{}, nil }
	Register(public, Operation{Method: "GET", Path: "/public", OperationID: "public"}, handler)
	Register(internal, Operation{Method: "GET", Path: "/internal", OperationID: "internal"}, handler)
	for i, operation := range registry.Endpoints() {
		responses := append(append([]oapifly.EndpointResponse(nil), operation.Responses...), operation.Middleware[0].Responses...)
		for _, response := range responses {
			_, present := response.Headers["X-Frame-Options"]
			if present != (i == 0) {
				t.Errorf("%s %d: security header present %v", operation.OperationID, response.Status, present)
			}
		}
	}
	if len(shared.Responses[0].Headers) != 0 {
		t.Fatal("shared middleware declaration was modified")
	}
}
