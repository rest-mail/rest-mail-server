package api

import (
	"fmt"
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

var messageCollectionReadPaths = map[string]string{
	"listMessages": "/api/v1/accounts/{id}/folders/{folder}/messages",
	"getThread":    "/api/v1/accounts/{id}/threads/{threadID}",
}

func messageCollectionReadURI(path string, pair oapifly.ExamplePair) string {
	for key, value := range pair.Request.Path {
		text := fmt.Sprint(value)
		if n, ok := value.(float64); ok {
			text = strconv.FormatFloat(n, 'f', -1, 64)
		}
		path = strings.ReplaceAll(path, "{"+key+"}", url.PathEscape(text))
	}
	return publicDiscoveryURI(path, pair)
}

func TestMessageCollectionReadExamples(t *testing.T) {
	scenarios := messageCollectionReadScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range messageCollectionReadPaths {
		if len(pairs[operation]) < 16 {
			t.Fatalf("incomplete examples for %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				req := httptest.NewRequest(http.MethodGet, messageCollectionReadURI(path, pair), nil)
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
			if _, ok := messageCollectionReadPaths[warning.OperationID]; ok {
				t.Errorf("message collection warning: %v", warning)
			}
		}
	}
}

func TestMessageCollectionReadExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for message collection exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := messageCollectionReadScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range messageCollectionReadPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s := scenarios[name]
				checkMailboxLookupWithVertrag(t, binary, s.handler, s.token, path, name, pair)
			})
		}
	}
}
