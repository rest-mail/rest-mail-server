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

var aliasMutationPaths = map[string]string{"createAlias": "/api/v1/admin/aliases", "updateAlias": "/api/v1/admin/aliases/{id}"}

func TestAliasMutationExamples(t *testing.T) {
	f := newAliasMutationScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range aliasMutationPaths {
		if len(pairs[op]) < 25 {
			t.Fatalf("incomplete %s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, op, name)
				s, ok := f[name]
				if !ok {
					t.Fatalf("missing scenario:%s", name)
				}
				method := http.MethodPost
				if op == "updateAlias" {
					method = http.MethodPatch
				}
				body := []byte(pair.Request.Body)
				if name == "malformed_json" {
					body = []byte("{")
				}
				if name == "request_too_large" {
					body = bytes.Repeat([]byte("x"), 513)
				}
				before := 0
				if s.store != nil {
					before = len(s.store.snapshot())
				}
				req := httptest.NewRequest(method, messageCollectionReadURI(path, pair), bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
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
						if strings.HasPrefix(m.query, "INSERT") {
							fields, err = exampleInsertFields(m.query, m.args)
							if fields["domain_id"] != int64(1) || fields["active"] != true {
								t.Fatal("alias creation changed domain/default")
							}
						} else {
							fields, err = vacationMutationFields(m.query, m.args)
							if fields["id"] != int64(pair.Request.Path["id"].(float64)) {
								t.Fatal("alias save changed row")
							}
						}
						if err != nil {
							t.Fatal(err)
						}
						if rr.Code < 300 {
							var answer struct{ Data map[string]interface{} }
							if err := json.Unmarshal(pair.Response.Body, &answer); err != nil {
								t.Fatal(err)
							}
							for _, key := range []string{"source_address", "destination_address", "active"} {
								if v, ok := fields[key]; ok && v != answer.Data[key] {
									t.Fatalf("stored%s=%v differs", key, v)
								}
							}
						}
					}
				}
				wantWrite := rr.Code < 300 || name == "duplicate" || name == "write_unavailable" || name == "update_unavailable" || name == "reload_unavailable" || name == "reload_missing" || name == "domain_preload_unavailable"
				if op == "updateAlias" && (name == "unchanged" || name == "null_fields") {
					wantWrite = false
				}
				if (writes == 1) != wantWrite || writes > 1 {
					t.Fatalf("writes=%d want%t", writes, wantWrite)
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := aliasMutationPaths[w.OperationID]; ok {
				t.Errorf("alias mutation warning:%v", w)
			}
		}
	}
}

func TestAliasMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for alias mutation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newAliasMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range aliasMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f[name]
				if !ok {
					t.Fatalf("missing scenario:%s", name)
				}
				method := http.MethodPost
				if op == "updateAlias" {
					method = http.MethodPatch
				}
				checkQueueMutationPairWithVertrag(t, binary, s, method, path, name, pair, 513)
			})
		}
	}
}
