package endpoint

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// A text refusal is written as http.Error writes one: the message and a newline, as plain
// text, never the JSON error envelope.
func TestRefuseTextIsPlainText(t *testing.T) {
	r := chi.NewRouter()
	Register(Routes{Router: r, Registry: NewRegistry()}, Operation{Method: http.MethodGet, Path: "/config.xml", OperationID: "config",
		MediaType: "application/xml"}, func(*http.Request, struct{}) (Document, error) {
		return Document{}, RefuseText(http.StatusNotFound, "domain not found")
	})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/config.xml", nil))
	if rr.Code != http.StatusNotFound || rr.Body.String() != "domain not found\n" ||
		rr.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("refusal = %d %q %v", rr.Code, rr.Body, rr.Header())
	}
}
