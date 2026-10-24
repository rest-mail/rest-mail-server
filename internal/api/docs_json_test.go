package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// GET /api/docs/openapi.json answers the description the registered routes generate, itself
// among them, the same as Routers.Description gives, readable from any origin (#670).
func TestTheGeneratedDescriptionIsServed(t *testing.T) {
	routers := describedRouters(t)
	get := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		routers.Public.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/docs/openapi.json", nil))
		return rr
	}
	rr := get()
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "application/json" || rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("%d %v", rr.Code, rr.Header())
	}
	var served struct {
		OpenAPI string                            `json:"openapi"`
		Paths   map[string]map[string]interface{} `json:"paths"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &served); err != nil {
		t.Fatal(err)
	}
	if served.OpenAPI != "3.1.0" || served.Paths["/api/docs/openapi.json"]["get"] == nil || served.Paths["/api/v1/admin/domains"] == nil {
		t.Fatalf("served %s with paths %d", served.OpenAPI, len(served.Paths))
	}
	doc, _ := routers.Description("test")
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var described struct {
		Paths map[string]map[string]interface{} `json:"paths"`
	}
	if err := json.Unmarshal(data, &described); err != nil {
		t.Fatal(err)
	}
	if len(served.Paths) != len(described.Paths) {
		t.Errorf("served %d paths, Description has %d", len(served.Paths), len(described.Paths))
	}
	// CI gives both frontends the actual served bytes from this revision.
	// No database or independently maintained schema is needed.
	if path := os.Getenv("RESTMAIL_OPENAPI_OUTPUT"); path != "" {
		if err := os.WriteFile(path, rr.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if again := get(); again.Body.String() != rr.Body.String() {
		t.Error("a second request answered other bytes")
	}
}
