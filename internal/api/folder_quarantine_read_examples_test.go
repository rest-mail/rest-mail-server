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

var folderQuarantineReadPaths = map[string]string{
	"listFolders":    "/api/v1/accounts/{id}/folders",
	"listQuarantine": "/api/v1/accounts/{id}/quarantine",
}

func TestFolderQuarantineReadExamples(t *testing.T) {
	scenarios := folderQuarantineReadScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range folderQuarantineReadPaths {
		if len(pairs[operation]) < 16 {
			t.Fatalf("incomplete examples for %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, operation, name)
				scenario, ok := scenarios[name]
				if !ok {
					t.Fatalf("no scenario for %s", name)
				}
				req := httptest.NewRequest(http.MethodGet, publicDiscoveryURI(domainAliasReadURI(path, pair), pair), nil)
				if scenario.token != "" {
					req.Header.Set("Authorization", "Bearer "+scenario.token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				scenario.handler.ServeHTTP(rr, req)
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
			if _, ok := folderQuarantineReadPaths[warning.OperationID]; ok {
				t.Errorf("folder/quarantine warning: %v", warning)
			}
		}
	}
}

func TestFolderQuarantineReadExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for folder/quarantine read exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := folderQuarantineReadScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range folderQuarantineReadPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				scenario := scenarios[name]
				checkMailboxLookupWithVertrag(t, binary, scenario.handler, scenario.token, path, name, pair)
			})
		}
	}
}
