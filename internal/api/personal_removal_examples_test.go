package api

import (
	"github.com/restmail/restmail/internal/examplepairs"
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

var personalRemovalPaths = map[string]string{"deleteContact": "/api/v1/accounts/{id}/contacts/{cid}", "deleteFolder": "/api/v1/accounts/{id}/folders/{folder}"}

func TestPersonalRemovalExamples(t *testing.T) {
	fixture := newPersonalRemovalScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range personalRemovalPaths {
		if len(pairs[op]) < 25 {
			t.Fatalf("incomplete %s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := fixture.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				before := len(fixture.store.snapshot())
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
				calls := fixture.store.snapshot()[before:]
				if op == "deleteContact" {
					wantRead := rr.Code == 204 || rr.Code == 404 || name == "lookup_unavailable" || name == "write_unavailable"
					wantWrite := rr.Code == 204 || name == "write_unavailable"
					read, writes := 0, 0
					for _, m := range calls {
						if strings.HasPrefix(m.query, "SELECT") && strings.Contains(m.query, `FROM "contacts"`) {
							read++
							if m.args[0].Value != int64(pair.Request.Path["cid"].(float64)) || m.args[1].Value != int64(pair.Request.Path["id"].(float64)) {
								t.Fatal("contact row/mailbox changed")
							}
						}
						if strings.HasPrefix(m.query, `DELETE FROM "contacts"`) {
							writes++
							if m.args[0].Value != int64(pair.Request.Path["cid"].(float64)) {
								t.Fatal("contact mutation changed ID")
							}
						}
					}
					if (read == 1) != wantRead || read > 1 || (writes == 1) != wantWrite || writes > 1 {
						t.Fatalf("contact read/write attempts %d/%d want %t/%t", read, writes, wantRead, wantWrite)
					}
				} else {
					wantWork := rr.Code == 204 || rr.Code == 404 || name == "sum_unavailable" || name == "delete_unavailable" || name == "quota_unavailable"
					if !wantWork {
						if len(calls) != 0 {
							t.Fatalf("refusal touched folder: %v", calls)
						}
						return
					}
					if len(calls) < 3 || calls[0].query != "BEGIN" {
						t.Fatalf("folder operation outside transaction: %v", calls)
					}
					end := "ROLLBACK"
					if rr.Code == 204 {
						end = "COMMIT"
					}
					if calls[len(calls)-1].query != end {
						t.Fatalf("folder transaction ended %s, want %s", calls[len(calls)-1].query, end)
					}
					wantOps := 3
					if name != "sum_unavailable" {
						wantOps++
					}
					if rr.Code == 204 && name != "empty_folder" || name == "quota_unavailable" {
						wantOps++
					}
					if len(calls) != wantOps {
						t.Fatalf("folder calls=%d want %d: %v", len(calls), wantOps, calls)
					}
					for _, m := range calls[1 : len(calls)-1] {
						if strings.Contains(m.query, `"messages"`) {
							if m.args[0].Value != int64(pair.Request.Path["id"].(float64)) || m.args[1].Value != pair.Request.Path["folder"] {
								t.Fatalf("folder mailbox/name changed: %v", m.args)
							}
						} else {
							// GORM adds the ordinary row update timestamp after the quota expression.
							if m.args[2].Value != int64(pair.Request.Path["id"].(float64)) {
								t.Fatal("quota changed mailbox")
							}
							stamp, ok := m.args[1].Value.(time.Time)
							if !ok || stamp.Before(started) || stamp.After(finished) {
								t.Fatal("quota timestamp outside request")
							}
						}
					}
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := personalRemovalPaths[w.OperationID]; ok {
				t.Errorf("personal removal warning: %v", w)
			}
		}
	}
}

func TestPersonalRemovalExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for personal removal exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newPersonalRemovalScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range personalRemovalPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := fixture.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{s, nil}, http.MethodDelete, path, name, pair)
			})
		}
	}
}
