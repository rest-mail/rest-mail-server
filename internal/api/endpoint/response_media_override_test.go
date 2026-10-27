package endpoint

import (
	"net/http"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
)

func TestResponseOverridesPreserveEveryMediaType(t *testing.T) {
	reg := NewRegistry()
	Register(Routes{Router: chi.NewRouter(), Registry: reg}, Operation{
		Method: "GET", Path: "/file", OperationID: "file", MediaType: "application/octet-stream",
		Responses: []oapifly.EndpointResponse{
			{Status: 200, Type: oapifly.TypeOf[[]byte](), MediaType: "application/octet-stream"},
			{Status: 206, Type: oapifly.TypeOf[[]byte](), MediaType: "application/octet-stream"},
			{Status: 206, Type: oapifly.TypeOf[[]byte](), MediaType: "multipart/byteranges"},
			{Status: 500, Type: oapifly.TypeOf[respond.ErrorResponse]()},
			{Status: 500, Type: oapifly.TypeOf[string](), MediaType: "text/plain"},
		},
	}, func(*http.Request, struct{}) (Served, error) { return Served{}, nil })
	if reg.Endpoints()[0].Responses[0].Status != 200 {
		t.Fatal("overriding a response changed its established position")
	}
	g := oapifly.New(oapifly.Config{})
	g.Register(reg.Endpoints()...)
	paths := g.Generate()["paths"].(map[string]map[string]oapifly.PathItem)
	for status, types := range map[string][]string{
		"206": {"application/octet-stream", "multipart/byteranges"},
		"500": {"application/json", "text/plain"},
	} {
		response := paths["/file"]["get"].Responses[status]
		if len(response.Content) != len(types) {
			t.Errorf("%s content = %v, want %v", status, response.Content, types)
		}
		for _, media := range types {
			if _, present := response.Content[media]; !present {
				t.Errorf("%s lost %s", status, media)
			}
		}
	}
	if _, present := paths["/file"]["get"].Responses["200"]; !present {
		t.Fatal("overrides removed an unrelated response")
	}
}
