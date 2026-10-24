package endpoint

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// A Document is written byte for byte, in its own content type and with its own headers,
// and described as text in the operation's media type.
func TestDocumentIsWrittenAsItIsAndDescribedInItsMediaType(t *testing.T) {
	r := chi.NewRouter()
	reg := NewRegistry()
	page := []byte("<!DOCTYPE html><p>hi</p>")
	Register(Routes{Router: r, Registry: reg}, Operation{Method: http.MethodGet, Path: "/page", OperationID: "page",
		MediaType: "text/html"}, func(*http.Request, struct{}) (Document, error) {
		return Document{ContentType: "text/html; charset=utf-8", Header: http.Header{"Access-Control-Allow-Origin": {"*"}}, Body: page}, nil
	})

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/page", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != string(page) {
		t.Fatalf("answer = %d %q", rr.Code, rr.Body)
	}
	if rr.Header().Get("Content-Type") != "text/html; charset=utf-8" || rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("headers = %v", rr.Header())
	}

	ok := reg.Endpoints()[0].Responses[0]
	if ok.Status != http.StatusOK || ok.MediaType != "text/html" || ok.Type.Kind().String() != "string" {
		t.Fatalf("described as %+v", ok)
	}
}

// An operation answering a Document must name its media type, and only such an operation
// may: the description would otherwise say JSON where the server writes something else.
func TestDocumentNeedsAMediaType(t *testing.T) {
	for name, register := range map[string]func(Routes){
		"document without": func(rs Routes) {
			Register(rs, Operation{Method: http.MethodGet, Path: "/a", OperationID: "a"},
				func(*http.Request, struct{}) (Document, error) { return Document{}, nil })
		},
		"media type without a document": func(rs Routes) {
			Register(rs, Operation{Method: http.MethodGet, Path: "/b", OperationID: "b", MediaType: "text/plain"},
				func(*http.Request, struct{}) (Empty, error) { return Empty{}, nil })
		},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("registered without complaint")
				}
			}()
			register(Routes{Router: chi.NewRouter(), Registry: NewRegistry()})
		})
	}
}
