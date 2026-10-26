package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

var sieveMutationPaths = map[string]struct{ method, path string }{
	"deleteSieveScript":   {http.MethodDelete, "/api/v1/accounts/{id}/sieve"},
	"validateSieveScript": {http.MethodPost, "/api/v1/accounts/{id}/sieve/validate"},
}

func TestSieveMutationExamples(t *testing.T) {
	f := newSieveMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, route := range sieveMutationPaths {
		if len(pairs[op]) < 20 {
			t.Fatalf("incomplete %s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				var body []byte
				if route.method == http.MethodPost {
					if name == "malformed_json" {
						body = []byte("{")
					} else if name == "request_too_large" {
						body = bytes.Repeat([]byte("x"), 257)
					} else {
						body, err = json.Marshal(pair.Request.Body)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				before := len(f.store.snapshot())
				req := httptest.NewRequest(route.method, messageCollectionReadURI(route.path, pair), bytes.NewReader(body))
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				if route.method == http.MethodPost {
					req.Header.Set("Content-Type", "application/json")
				}
				if name == "request_too_large" {
					req.ContentLength = 257
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				calls := f.store.snapshot()[before:]
				wantWrite := op == "deleteSieveScript" && (rr.Code == 200 || name == "internal_error")
				if (len(calls) == 1) != wantWrite || len(calls) > 1 {
					t.Fatalf("Sieve mutation attempts=%d want %t", len(calls), wantWrite)
				}
				if wantWrite && calls[0].args[0].Value != int64(pair.Request.Path["id"].(float64)) {
					t.Fatal("Sieve deletion changed mailbox")
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := sieveMutationPaths[w.OperationID]; ok {
				t.Errorf("Sieve mutation warning: %v", w)
			}
		}
	}
}

func TestSieveMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for Sieve exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newSieveMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, route := range sieveMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{s, nil}, route.method, route.path, name, pair, 257)
			})
		}
	}
}
