package api

import (
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

var mailResourceReadPaths = map[string]string{"listAttachments": "/api/v1/messages/{id}/attachments", "listScheduledMessages": "/api/v1/messages/scheduled"}

func TestMailResourceReadExamples(t *testing.T) {
	scenarios := mailResourceReadScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range mailResourceReadPaths {
		if len(pairs[operation]) < 12 {
			t.Fatalf("incomplete examples for %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, operation, name)
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				req := httptest.NewRequest(http.MethodGet, publicDiscoveryURI(domainAliasReadURI(path, pair), pair), nil)
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
				}
				if name == "rate_limited" {
					retry, err := strconv.Atoi(rr.Header().Get("Retry-After"))
					if err != nil || retry < 1 {
						t.Fatal("refusal lost Retry-After")
					}
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, warning := range g.Report() {
			if _, ok := mailResourceReadPaths[warning.OperationID]; ok {
				t.Errorf("mail resource warning: %v", warning)
			}
		}
	}
}

func TestMailResourceReadExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for mail resource read exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := mailResourceReadScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range mailResourceReadPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s := scenarios[name]
				checkMailboxLookupWithVertrag(t, binary, s.handler, s.token, path, name, pair)
			})
		}
	}
}
