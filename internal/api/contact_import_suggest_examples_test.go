package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

var contactImportSuggestPaths = map[string]string{"importContacts": "/api/v1/accounts/{id}/contacts/import", "suggestContacts": "/api/v1/accounts/{id}/contacts/suggest"}

func TestContactImportSuggestExamples(t *testing.T) {
	f := newContactImportSuggestScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range contactImportSuggestPaths {
		if len(pairs[op]) < 25 {
			t.Fatalf("incomplete %s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario:%s", name)
				}
				method := http.MethodPost
				var body []byte
				if op == "suggestContacts" {
					method = http.MethodGet
				} else {
					body = []byte(pair.Request.Body)
				}
				if name == "malformed_json" {
					body = []byte("{")
				}
				if name == "request_too_large" {
					body = bytes.Repeat([]byte("x"), 513)
				}
				before := len(f.store.snapshot())
				req := httptest.NewRequest(method, messageCollectionReadURI(path, pair), bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d %s want%d", rr.Code, rr.Body, pair.Response.Status)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				calls := f.store.snapshot()[before:]
				if op == "suggestContacts" {
					q := pair.Request.Query["q"]
					wantRead := (rr.Code == 200 && q != nil && q != "") || name == "query_unavailable"
					if (len(calls) == 1) != wantRead || len(calls) > 1 {
						t.Fatalf("suggestion reads=%d want%t", len(calls), wantRead)
					}
					if wantRead {
						escaped := "%" + strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(q.(string)) + "%"
						if calls[0].args[0].Value != int64(pair.Request.Path["id"].(float64)) || calls[0].args[1].Value != escaped {
							t.Fatal("literal suggestion query changed")
						}
					}
					return
				}
				wantWrite := rr.Code == 200 || name == "batch_failure"
				if !wantWrite {
					if len(calls) != 0 {
						t.Fatalf("refusal reached storage:%v", calls)
					}
					return
				}
				if len(calls) < 3 || calls[0].query != "BEGIN" || calls[len(calls)-1].query != map[bool]string{true: "ROLLBACK", false: "COMMIT"}[name == "batch_failure"] {
					t.Fatalf("batch transaction boundary:%v", calls)
				}
				var input struct {
					Contacts []struct{ Email, Name string }
				}
				if err := json.Unmarshal(pair.Request.Body, &input); err != nil {
					t.Fatal(err)
				}
				for i, m := range calls[1 : len(calls)-1] {
					fields, err := exampleInsertFields(m.query, m.args)
					if err != nil {
						t.Fatal(err)
					}
					if i >= len(input.Contacts) || fields["email"] != input.Contacts[i].Email || fields["name"] != input.Contacts[i].Name || fields["mailbox_id"] != int64(pair.Request.Path["id"].(float64)) || fields["trust_level"] != "auto" || fields["source"] != "import" || !strings.Contains(m.query, `ON CONFLICT ("mailbox_id","email") DO NOTHING`) {
						t.Fatalf("import fields/conflict target changed:%v", fields)
					}
				}
				if name == "batch_failure" && len(calls) != 4 {
					t.Fatal("failed batch attempted extra entries")
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := contactImportSuggestPaths[w.OperationID]; ok {
				t.Errorf("contact import/suggest warning:%v", w)
			}
		}
	}
}

func TestContactImportSuggestExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for contact import/suggest exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newContactImportSuggestScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range contactImportSuggestPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario:%s", name)
				}
				method := http.MethodPost
				if op == "suggestContacts" {
					method = http.MethodGet
				}
				checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{s, nil}, method, path, name, pair, 513)
			})
		}
	}
}
