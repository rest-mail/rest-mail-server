package api

import (
	"bytes"
	"encoding/json"
	"fmt"
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

const vacationMutationPath = "/api/v1/accounts/{id}/vacation"

func TestVacationMutationExamples(t *testing.T) {
	f := newVacationMutationScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"setVacation", "disableVacation"} {
		if len(pairs[op]) < 25 {
			t.Fatalf("incomplete %s examples", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, op, name)
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing vacation scenario %s", name)
				}
				method := http.MethodPut
				if op == "disableVacation" {
					method = http.MethodDelete
				}
				var body []byte
				if name == "malformed_json" {
					body = []byte("{")
				} else if name == "request_too_large" {
					body = bytes.Repeat([]byte("x"), 257)
				} else if op == "setVacation" {
					body, err = json.Marshal(pair.Request.Body)
					if err != nil {
						t.Fatal(err)
					}
				}
				var input map[string]interface{}
				if op == "setVacation" && name != "body_array" && name != "malformed_json" {
					if err := json.Unmarshal(pair.Request.Body, &input); err != nil {
						t.Fatal(err)
					}
				}
				before := len(f.store.snapshot())
				req := httptest.NewRequest(method, messageCollectionReadURI(vacationMutationPath, pair), bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d %s, want %d", rr.Code, rr.Body, pair.Response.Status)
				}
				if rr.Code == 204 {
					if rr.Body.Len() != 0 {
						t.Fatalf("204 body:%s", rr.Body)
					}
				} else {
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				}
				calls := f.store.snapshot()[before:]
				reads, writes := 0, 0
				wantRead := rr.Code < 300 || name == "lookup_unavailable" || name == "create_unavailable" || name == "update_unavailable"
				wantWrite := wantRead && name != "lookup_unavailable" && !(op == "disableVacation" && name == "missing")
				for _, m := range calls {
					if strings.HasPrefix(m.query, "SELECT") {
						reads++
						if m.args[0].Value != int64(pair.Request.Path["id"].(float64)) {
							t.Fatal("vacation lookup changed mailbox")
						}
						continue
					}
					writes++
					fields, err := vacationMutationFields(m.query, m.args)
					if err != nil {
						t.Fatal(err)
					}
					id := int64(pair.Request.Path["id"].(float64))
					if strings.HasPrefix(m.query, "UPDATE") {
						if fields["id"] != id+100 {
							t.Fatalf("update selected wrong row:%v", fields)
						}
					} else if fields["mailbox_id"] != id {
						t.Fatal("insert changed mailbox")
					}
					if op == "disableVacation" {
						if len(fields) != 3 || fields["enabled"] != false {
							t.Fatalf("disable changed other settings:%v", fields)
						}
						continue
					}
					if len(fields) != 7 && len(fields) != 8 {
						t.Fatalf("unexpected configuration fields:%v", fields)
					}
					if fields["enabled"] != input["enabled"] {
						t.Fatal("enabled changed")
					}
					for _, key := range []string{"subject", "body", "start_date", "end_date"} {
						expected, exists := input[key]
						if key == "subject" || key == "body" {
							if !exists {
								expected = ""
							}
							if fields[key] != expected {
								t.Fatalf("%s changed:%v", key, fields)
							}
						} else if !exists || expected == nil {
							if fields[key] != nil {
								t.Fatalf("%s not cleared:%v", key, fields[key])
							}
						} else {
							stamp, ok := fields[key].(time.Time)
							want, err := time.Parse(time.RFC3339, fmt.Sprint(expected))
							if !ok || err != nil || !stamp.Equal(want) {
								t.Fatalf("%s=%v want%v", key, fields[key], expected)
							}
						}
					}
				}
				if (reads == 1) != wantRead || reads > 1 || (writes == 1) != wantWrite || writes > 1 {
					t.Fatalf("vacation lookup/write=%d/%d want %t/%t", reads, writes, wantRead, wantWrite)
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "setVacation" || w.OperationID == "disableVacation" {
				t.Errorf("vacation mutation warning:%v", w)
			}
		}
	}
}

func TestVacationMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for vacation mutation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newVacationMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"setVacation", "disableVacation"} {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing vacation scenario %s", name)
				}
				method := http.MethodPut
				if op == "disableVacation" {
					method = http.MethodDelete
				}
				checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{s, nil}, method, vacationMutationPath, name, pair, 257)
			})
		}
	}
}
