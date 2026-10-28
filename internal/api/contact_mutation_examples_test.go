package api

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
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

var contactMutationPaths = map[string]string{"createContact": "/api/v1/accounts/{id}/contacts", "updateContact": "/api/v1/accounts/{id}/contacts/{cid}", "blockSender": "/api/v1/accounts/{id}/contacts/block"}

func TestContactMutationExamples(t *testing.T) {
	f := newContactMutationScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range contactMutationPaths {
		if len(pairs[op]) < 25 {
			t.Fatalf("incomplete %s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, op, name)
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario:%s", name)
				}
				method := http.MethodPost
				if op == "updateContact" {
					method = http.MethodPatch
				}
				body := []byte(pair.Request.Body)
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
				writes := 0
				var input map[string]interface{}
				_ = json.Unmarshal(pair.Request.Body, &input)
				for _, m := range calls {
					if strings.HasPrefix(m.query, "SELECT") {
						if op == "blockSender" && strings.Contains(m.query, "email =") {
							if m.args[0].Value != int64(pair.Request.Path["id"].(float64)) || m.args[1].Value != input["email"] {
								t.Fatal("block lookup changed mailbox/email")
							}
						} else {
							cid := int64(1)
							if op == "updateContact" {
								cid = int64(pair.Request.Path["cid"].(float64))
							}
							if m.args[0].Value != cid || m.args[1].Value != int64(pair.Request.Path["id"].(float64)) {
								t.Fatal("contact lookup changed row/mailbox")
							}
						}
						continue
					}
					writes++
					var fields map[string]driver.Value
					var err error
					if strings.HasPrefix(m.query, "INSERT") {
						fields, err = exampleInsertFields(m.query, m.args)
						if fields["mailbox_id"] != int64(pair.Request.Path["id"].(float64)) || fields["email"] != input["email"] || fields["source"] != "manual" {
							t.Fatal("contact creation changed mailbox/email/source")
						}
					} else {
						fields, err = vacationMutationFields(m.query, m.args)
						cid := int64(1)
						if op == "updateContact" {
							cid = int64(pair.Request.Path["cid"].(float64))
						}
						if fields["id"] != cid {
							t.Fatal("contact write changed loaded row")
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					if rr.Code < 300 {
						var expected struct{ Data map[string]interface{} }
						if err := json.Unmarshal(pair.Response.Body, &expected); err != nil {
							t.Fatal(err)
						}
						for _, key := range []string{"email", "name", "trust_level", "source"} {
							if value, ok := fields[key]; ok && value != expected.Data[key] {
								t.Fatalf("stored %s=%v differs from response", key, value)
							}
						}
					}
				}
				wantWrite := rr.Code < 300 || name == "duplicate" || name == "write_unavailable" || name == "create_unavailable" || name == "reload_unavailable" || name == "reload_missing"
				if op == "updateContact" && (name == "unchanged" || name == "null_fields") {
					wantWrite = false
				}
				if (writes == 1) != wantWrite || writes > 1 {
					t.Fatalf("writes=%d want%t", writes, wantWrite)
				}
				if name == "lookup_unavailable" && len(calls) != 1 {
					t.Fatal("failed lookup proceeded")
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := contactMutationPaths[w.OperationID]; ok {
				t.Errorf("contact mutation warning:%v", w)
			}
		}
	}
}

func TestContactMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for contact mutation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newContactMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range contactMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario:%s", name)
				}
				method := http.MethodPost
				if op == "updateContact" {
					method = http.MethodPatch
				}
				checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{s, nil}, method, path, name, pair, 513)
			})
		}
	}
}
