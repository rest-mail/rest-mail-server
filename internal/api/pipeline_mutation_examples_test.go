package api

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

var pipelineMutationPaths = map[string]string{"createPipeline": "/api/v1/admin/pipelines", "updatePipeline": "/api/v1/admin/pipelines/{id}"}

func TestPipelineMutationExamples(t *testing.T) {
	f := newPipelineMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range pipelineMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f[name]
				if !ok {
					t.Fatal("missing pipeline scenario")
				}
				body := []byte(pair.Request.Body)
				if name == "malformed_json" {
					body = []byte("{")
				}
				if name == "request_too_large" {
					body = bytes.Repeat([]byte("x"), 513)
				}
				if name == "body_boundary" {
					body = append(body, bytes.Repeat([]byte(" "), 512-len(body))...)
				}
				before := 0
				if s.store != nil {
					before = len(s.store.snapshot())
				}
				method := http.MethodPost
				if op == "updatePipeline" {
					method = http.MethodPatch
				}
				rr := mailboxSessionEndingRequest(t, s, method, messageCollectionReadURI(path, pair), body)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got%d want%d", rr.Code, pair.Response.Status)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				if s.store == nil {
					return
				}
				writes := 0
				for _, q := range s.store.snapshot()[before:] {
					if strings.HasPrefix(q.query, `INSERT INTO "pipelines"`) || strings.HasPrefix(q.query, `UPDATE "pipelines"`) {
						writes++
						if rr.Code/100 == 2 {
							if op == "updatePipeline" {
								v, err := vacationMutationFields(q.query, q.args)
								if err != nil {
									t.Fatal(err)
								}
								if len(v) != 4 || v["id"] != int64(pair.Request.Path["id"].(float64)) {
									t.Fatal("pipeline update changed identity/direction/creation metadata")
								}
							}
						}
					}
				}
				want := rr.Code/100 == 2 || name == "insert_unavailable" || name == "update_unavailable" || name == "concurrent"
				if (writes == 1) != want || writes > 1 {
					t.Fatal("unexpected pipeline mutation count")
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := pipelineMutationPaths[w.OperationID]; ok {
				t.Errorf("pipeline warning:%v", w)
			}
		}
	}
}

func TestPipelineMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newPipelineMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range pipelineMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				method := http.MethodPost
				if op == "updatePipeline" {
					method = http.MethodPatch
				}
				checkQueueMutationPairWithVertrag(t, binary, f[name], method, path, name, pair, 513)
			})
		}
	}
}
