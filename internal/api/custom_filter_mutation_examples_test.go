package api

import (
	"bytes"
	"database/sql/driver"
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

var customFilterMutationPaths = map[string]string{"createCustomFilter": "/api/v1/admin/custom-filters", "updateCustomFilter": "/api/v1/admin/custom-filters/{id}"}

func TestCustomFilterMutationExamples(t *testing.T) {
	scenarios := newCustomFilterMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range customFilterMutationPaths {
		if len(pairs[op]) < 30 {
			t.Fatalf("incomplete operation%s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario%s", name)
				}
				method := http.MethodPost
				if op == "updateCustomFilter" {
					method = http.MethodPatch
				}
				body := []byte(pair.Request.Body)
				if name == "malformed_json" {
					body = []byte("{")
				}
				if name == "request_too_large" {
					body = bytes.Repeat([]byte("x"), 513)
				}
				if name == "body_boundary" {
					if len(body) > 512 {
						t.Fatal("boundary example exceeds the configured cap")
					}
					body = append(body, bytes.Repeat([]byte(" "), 512-len(body))...)
				}
				before := 0
				if s.store != nil {
					before = len(s.store.snapshot())
				}
				r := httptest.NewRequest(method, messageCollectionReadURI(path, pair), bytes.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				if s.token != "" {
					r.Header.Set("Authorization", "Bearer "+s.token)
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, r)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				writes := 0
				if s.store != nil {
					for _, m := range s.store.snapshot()[before:] {
						if strings.HasPrefix(m.query, "SELECT") {
							continue
						}
						writes++
						var fields map[string]driver.Value
						var err error
						if method == http.MethodPost {
							fields, err = exampleInsertFields(m.query, m.args)
						} else {
							fields, err = vacationMutationFields(m.query, m.args)
							if fields["id"] != int64(pair.Request.Path["id"].(float64)) {
								t.Fatal("update lost exact row")
							}
						}
						if err != nil {
							t.Fatal(err)
						}
						if fields["domain_id"] != int64(1) {
							t.Fatal("mutation changed domain identity")
						}
						if rr.Code < 300 {
							var answer struct{ Data map[string]interface{} }
							if err := json.Unmarshal(pair.Response.Body, &answer); err != nil {
								t.Fatal(err)
							}
							for _, key := range []string{"name", "description", "filter_type", "direction", "enabled"} {
								if fields[key] != answer.Data[key] {
									t.Fatalf("stored%s=%v want%v", key, fields[key], answer.Data[key])
								}
							}
							var config interface{}
							raw, ok := fields["config"].([]byte)
							if !ok {
								t.Fatalf("config binding has type %T", fields["config"])
							}
							if err := json.Unmarshal(raw, &config); err != nil {
								t.Fatal(err)
							}
							got, _ := json.Marshal(config)
							want, _ := json.Marshal(answer.Data["config"])
							if !bytes.Equal(got, want) {
								t.Fatalf("stored config%s want%s", got, want)
							}
							if fields["created_at"] != sieveInstallationTime || fields["updated_at"] != sieveInstallationTime {
								t.Fatal("lost server timestamps")
							}
						}
					}
				}
				want := rr.Code < 300 || name == "write_unavailable"
				if (writes == 1) != want || writes > 1 {
					t.Fatalf("writes%d want%t", writes, want)
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := customFilterMutationPaths[w.OperationID]; ok {
				t.Errorf("custom filter warning:%v", w)
			}
		}
	}
}

func TestCustomFilterMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for custom filter exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := newCustomFilterMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range customFilterMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario%s", name)
				}
				method := http.MethodPost
				if op == "updateCustomFilter" {
					method = http.MethodPatch
				}
				checkQueueMutationPairWithVertrag(t, binary, s, method, path, name, pair, 513)
			})
		}
	}
}
