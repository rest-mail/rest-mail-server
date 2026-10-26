package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

func TestAdminResourceDeleteExamples(t *testing.T) {
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, route := range adminResourceDeleteExamples {
		scenarios := adminResourceDeleteScenarios(t, operation)
		if len(pairs[operation]) < 12 {
			t.Fatalf("incomplete %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				var before int
				var reads int64
				if s.store != nil {
					before = len(s.store.snapshot())
					reads = s.reads.Load()
				}
				req := httptest.NewRequest(http.MethodDelete, messageCollectionReadURI(route.path, pair), nil)
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
					if rr.Header().Get("Content-Type") != "application/json" {
						t.Fatal("non-JSON resource answer")
					}
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				}
				var mutations []bulkQueueExampleMutation
				if s.store != nil {
					mutations = s.store.snapshot()[before:]
				}
				wantWrite := rr.Code < 300 || name == "internal_error" || (operation != "deleteCertificate" && name == "missing")
				if (len(mutations) == 1) != wantWrite || len(mutations) > 1 {
					t.Fatalf("delete attempts=%d for %s/%s", len(mutations), operation, name)
				}
				if s.reads != nil {
					wantReads := int64(0)
					if operation == "deleteCertificate" && (rr.Code < 300 || rr.Code == 404 || name == "internal_error" || name == "lookup_unavailable") {
						wantReads = 1
					}
					if s.reads.Load()-reads != wantReads {
						t.Fatalf("lookup count=%d, want %d", s.reads.Load()-reads, wantReads)
					}
				}
				if wantWrite {
					m := mutations[0]
					id := int64(pair.Request.Path["id"].(float64))
					if len(m.args) != 1 || m.args[0].Value != id || !strings.HasPrefix(m.query, `DELETE FROM "`+route.table+`"`) {
						t.Fatalf("delete target changed: %s %v", m.query, m.args)
					}
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := adminResourceDeleteExamples[w.OperationID]; ok {
				t.Errorf("admin deletion warning: %v", w)
			}
		}
	}
}

func TestAdminResourceDeleteExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for resource delete exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, route := range adminResourceDeleteExamples {
		scenarios := adminResourceDeleteScenarios(t, operation)
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				checkQueueMutationPairWithVertrag(t, binary, scenarios[name].bulkQueueExampleScenario, http.MethodDelete, route.path, name, pair)
			})
		}
	}
}
