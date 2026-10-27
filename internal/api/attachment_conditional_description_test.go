package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
)

func TestAttachmentConditionalDescriptionIsServed(t *testing.T) {
	routers := describedRouters(t)
	rr := httptest.NewRecorder()
	routers.Public.ServeHTTP(rr, httptest.NewRequest("GET", "/api/docs/openapi.json", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("description status=%d", rr.Code)
	}
	var doc struct {
		Paths map[string]map[string]oapifly.PathItem `json:"paths"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	operation := doc.Paths["/api/v1/attachments/{id}"]["get"]
	for _, name := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		found := false
		for _, parameter := range operation.Parameters {
			if parameter.In == "header" && parameter.Name == name {
				found = true
				if parameter.Required || parameter.Schema["type"] != "string" {
					t.Errorf("conditional header %s changed its optional string contract", name)
				}
			}
		}
		if !found {
			t.Errorf("served request is missing %s", name)
		}
	}
	responses := operation.Responses
	for status, media := range map[string][]string{
		"206": {"application/octet-stream", "multipart/byteranges"},
		"304": {}, "412": {},
		"416": {"text/plain"}, "500": {"application/json", "text/plain"},
	} {
		response, exists := responses[status]
		if !exists || len(response.Content) != len(media) {
			t.Fatalf("%s missing/wrong served media: %+v", status, response)
		}
		for _, name := range media {
			if _, exists := response.Content[name]; !exists {
				t.Errorf("served %s is missing %s", status, name)
			}
		}
	}
	if responses["206"].Headers["Content-Range"].Required || !responses["206"].Headers["Content-Length"].Required {
		t.Fatal("single/multipart 206 header requirements disagree")
	}
	if responses["500"].Headers["Content-Disposition"].Required {
		t.Fatal("a dependency failure cannot promise a download header")
	}
	// Keep both generated clients on the exact served bytes, without a database
	// or a separate server process. No credentials are in this description.
	if output := os.Getenv("RESTMAIL_ATTACHMENT_OPENAPI_OUTPUT"); output != "" {
		if err := os.WriteFile(output, rr.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
