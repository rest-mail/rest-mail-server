package api

import (
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

var mailRemovalPaths = map[string]string{
	"cancelScheduledMessage": "/api/v1/messages/scheduled/{id}",
	"deleteQuarantine":       "/api/v1/accounts/{id}/quarantine/{mid}",
}

func TestMailRemovalExamples(t *testing.T) {
	fixture := newMailRemovalScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range mailRemovalPaths {
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
				if name == "rate_limited" {
					n, err := strconv.Atoi(rr.Header().Get("Retry-After"))
					if err != nil || n < 1 {
						t.Fatal("rate refusal lost Retry-After")
					}
				}
				calls := fixture.store.snapshot()[before:]
				if op == "cancelScheduledMessage" {
					wantLookup := true
					switch name {
					case "unauthenticated", "session_unavailable", "setup_required", "rate_limited", "request_too_large", "invalid_id", "invalid_format", "csrf_missing", "csrf_mismatch":
						wantLookup = false
					}
					wantWrite := rr.Code == 204 || name == "concurrent" || name == "write_unavailable"
					lookups, writes := 0, 0
					for _, m := range calls {
						if strings.Contains(m.query, `FROM "scheduled_sends"`) {
							lookups++
							if m.args[0].Value != int64(pair.Request.Path["id"].(float64)) {
								t.Fatal("scheduled lookup changed ID")
							}
						} else {
							writes++
							if m.args[2].Value != int64(pair.Request.Path["id"].(float64)) {
								t.Fatal("scheduled update changed ID")
							}
							stamp, ok := m.args[1].Value.(time.Time)
							if !ok || stamp.Before(started) || stamp.After(finished) {
								t.Fatal("scheduled updated_at outside request")
							}
						}
					}
					if (lookups == 1) != wantLookup || lookups > 1 || (writes == 1) != wantWrite || writes > 1 {
						t.Fatalf("lookup/write attempts=%d/%d, want %t/%t", lookups, writes, wantLookup, wantWrite)
					}
				} else {
					wantWrite := rr.Code == 200 || rr.Code == 404 || name == "write_unavailable"
					if (len(calls) == 1) != wantWrite || len(calls) > 1 {
						t.Fatalf("quarantine mutation attempts=%d for %s", len(calls), name)
					}
					if wantWrite {
						m := calls[0]
						if m.args[0].Value != int64(pair.Request.Path["mid"].(float64)) || m.args[1].Value != int64(pair.Request.Path["id"].(float64)) {
							t.Fatalf("quarantine row/mailbox changed: %v", m.args)
						}
					}
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := mailRemovalPaths[w.OperationID]; ok {
				t.Errorf("mail removal warning: %v", w)
			}
		}
	}
}

func TestMailRemovalExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for mail removal exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newMailRemovalScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range mailRemovalPaths {
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
