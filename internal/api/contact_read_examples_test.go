package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

const contactReadPath = "/api/v1/accounts/{id}/contacts/{cid}"

func TestContactReadExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	fixture := newContactMutationScenarios(t)
	const operation = "getContact"
	for name, pair := range pairs[operation] {
		t.Run(operation+"/"+name, func(t *testing.T) {
			examplepairs.Proven(t, operation, name)
			scenario, ok := fixture.scenarios[name]
			if !ok {
				t.Fatalf("missing scenario %s", name)
			}
			var body string
			if name == "request_too_large" {
				body = strings.Repeat("x", 513)
			}
			request := httptest.NewRequest(http.MethodGet, messageCollectionReadURI(contactReadPath, pair), strings.NewReader(body))
			if scenario.token != "" {
				request.Header.Set("Authorization", "Bearer "+scenario.token)
			}
			response := httptest.NewRecorder()
			scenario.handler.ServeHTTP(response, request)
			if response.Code != pair.Response.Status {
				t.Fatalf("got %d %s, want %d", response.Code, response.Body, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, response.Body.Bytes())
		})
	}
}

func TestContactReadExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for contact read exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	fixture := newContactMutationScenarios(t)
	const operation = "getContact"
	for name, pair := range pairs[operation] {
		t.Run(operation+"/"+name, func(t *testing.T) {
			scenario, ok := fixture.scenarios[name]
			if !ok {
				t.Fatalf("missing scenario %s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{scenario, nil}, http.MethodGet, contactReadPath, name, pair, 513)
		})
	}
}
