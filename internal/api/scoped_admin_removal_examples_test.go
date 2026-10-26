package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

func TestScopedAdminRemovalExamples(t *testing.T) {
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range scopedAdminRemovalPaths {
		scenarios := scopedAdminRemovalScenarios(t, op)
		if len(pairs[op]) < 18 {
			t.Fatalf("incomplete %s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				before, reads := 0, int64(0)
				if s.store != nil {
					before = len(s.store.snapshot())
					reads = s.reads.Load()
				}
				req := httptest.NewRequest(http.MethodDelete, messageCollectionReadURI(path, pair), nil)
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				started := time.Now()
				s.handler.ServeHTTP(rr, req)
				finished := time.Now()
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
				var writes []bulkQueueExampleMutation
				if s.store != nil {
					writes = s.store.snapshot()[before:]
				}
				wantWrite := rr.Code == 204 || name == "internal_error"
				if (len(writes) == 1) != wantWrite || len(writes) > 1 {
					t.Fatalf("mutation attempts=%d for %s", len(writes), name)
				}
				wantReads := int64(0)
				switch name {
				case "found", "missing", "lookup_unavailable", "required_capability", "internal_error", "wrong_domain", "mailbox_scope", "scope_lookup_unavailable", "scoped_missing":
					wantReads = 1
				case "source_domain", "scoped_lookup_unavailable":
					wantReads = 2
				}
				if s.reads != nil && s.reads.Load()-reads != wantReads {
					t.Fatalf("lookups=%d want %d", s.reads.Load()-reads, wantReads)
				}
				if !wantWrite {
					return
				}
				m := writes[0]
				id := int64(pair.Request.Path["id"].(float64))
				if op == "deleteAlias" {
					if m.args[0].Value != id {
						t.Fatalf("wrong alias ID: %v", m.args)
					}
				} else {
					if len(m.args) != 5 || m.args[0].Value != nil || m.args[1].Value != "" || m.args[2].Value != "" || m.args[4].Value != id ||
						!strings.Contains(m.query, `"dkim_created_at"=$1,"dkim_private_key"=$2,"dkim_selector"=$3,"updated_at"=$4`) {
						t.Fatalf("DKIM fields/ID changed: %s %v", m.query, m.args)
					}
					stamp, ok := m.args[3].Value.(time.Time)
					if !ok || stamp.Before(started) || stamp.After(finished) {
						t.Fatalf("DKIM update timestamp outside request: %v", m.args[3].Value)
					}
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := scopedAdminRemovalPaths[w.OperationID]; ok {
				t.Errorf("scoped removal warning: %v", w)
			}
		}
	}
}

func TestScopedAdminRemovalExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for scoped removal exchanges")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("Vertrag binary must be absolute")
	}
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range scopedAdminRemovalPaths {
		scenarios := scopedAdminRemovalScenarios(t, op)
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				checkQueueMutationPairWithVertrag(t, binary, s.bulkQueueExampleScenario, http.MethodDelete, path, name, pair)
			})
		}
	}
}
