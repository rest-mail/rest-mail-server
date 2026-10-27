package api

import (
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

func TestBanRemovalExamples(t *testing.T) {
	scenarios := banRemovalScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range banRemovalPaths {
		if len(pairs[operation]) < 10 {
			t.Fatalf("incomplete %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, operation, name)
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				before := 0
				if s.store != nil {
					before = len(s.store.snapshot())
				}
				req := httptest.NewRequest(http.MethodDelete, messageCollectionReadURI(path, pair), nil)
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
				}
				if rr.Code == 204 {
					if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" {
						t.Fatal("204 carried content")
					}
				} else {
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				}
				var calls []bulkQueueExampleMutation
				if s.store != nil {
					calls = s.store.snapshot()[before:]
				}
				wantWrite := rr.Code == 204 || name == "missing" || name == "literal_address" || name == "internal_error"
				if (len(calls) == 1) != wantWrite || len(calls) > 1 {
					t.Fatalf("deletion attempts=%d for %s", len(calls), name)
				}
				if !wantWrite {
					return
				}
				m := calls[0]
				if operation == "unbanIP" {
					if !strings.Contains(m.query, "ip =") || m.args[0].Value != pair.Request.Path["ip"] {
						t.Fatalf("wrong address selector: %s %v", m.query, m.args)
					}
				} else if !strings.Contains(m.query, `"id"`) || m.args[0].Value != int64(pair.Request.Path["id"].(float64)) {
					t.Fatal("wrong ID selector")
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := banRemovalPaths[w.OperationID]; ok {
				t.Errorf("ban removal warning: %v", w)
			}
		}
	}
}

func TestBanRemovalExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for ban removal exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	s := banRemovalScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range banRemovalPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				checkQueueMutationPairWithVertrag(t, binary, s[name], http.MethodDelete, path, name, pair)
			})
		}
	}
}
