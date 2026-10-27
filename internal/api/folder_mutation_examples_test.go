package api

import (
	"bytes"
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

var folderMutationPaths = map[string]string{"createFolder": "/api/v1/accounts/{id}/folders", "renameFolder": "/api/v1/accounts/{id}/folders/{folder}"}

func TestFolderMutationExamples(t *testing.T) {
	f := newFolderMutationScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range folderMutationPaths {
		if len(pairs[op]) < 30 {
			t.Fatalf("incomplete %s pairs", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing folder scenario %s", name)
				}
				method := http.MethodPost
				if op == "renameFolder" {
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
					t.Fatalf("got %d %s, want %d", rr.Code, rr.Body, pair.Response.Status)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				calls := f.store.snapshot()[before:]
				writes := 0
				var input struct{ Name string }
				_ = json.Unmarshal(pair.Request.Body, &input)
				for _, m := range calls {
					if strings.HasPrefix(m.query, "SELECT") {
						if len(m.args) != 2 || m.args[0].Value != int64(pair.Request.Path["id"].(float64)) {
							t.Fatal("folder count changed mailbox")
						}
						continue
					}
					writes++
					if op == "createFolder" {
						fields, err := exampleInsertFields(m.query, m.args)
						if err != nil {
							t.Fatal(err)
						}
						if fields["mailbox_id"] != int64(pair.Request.Path["id"].(float64)) || fields["folder"] != input.Name || fields["is_deleted"] != true || fields["size_bytes"] != int64(0) || fields["sender"] != "system" {
							t.Fatalf("marker changed:%v", fields)
						}
					} else {
						if m.args[0].Value != input.Name || m.args[2].Value != int64(pair.Request.Path["id"].(float64)) || m.args[3].Value != pair.Request.Path["folder"] {
							t.Fatal("rename changed selector or destination")
						}
					}
				}
				wantWrite := rr.Code < 300 || name == "write_unavailable" || name == "concurrent"
				if (writes == 1) != wantWrite || writes > 1 {
					t.Fatalf("writes=%d want %t", writes, wantWrite)
				}
				if !wantWrite && rr.Code != 404 && rr.Code != 409 && name != "source_unavailable" && name != "destination_unavailable" && len(calls) != 0 {
					t.Fatalf("refusal reached folder storage:%v", calls)
				}
				if name == "source_unavailable" && len(calls) != 1 {
					t.Fatal("failed source count proceeded")
				}
				if name == "destination_unavailable" && len(calls) != (map[bool]int{true: 1, false: 2}[op == "createFolder"]) {
					t.Fatal("failed destination count proceeded")
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := folderMutationPaths[w.OperationID]; ok {
				t.Errorf("folder mutation warning:%v", w)
			}
		}
	}
}

func TestFolderMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for folder mutation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newFolderMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range folderMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				method := http.MethodPost
				if op == "renameFolder" {
					method = http.MethodPatch
				}
				checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{s, nil}, method, path, name, pair, 513)
			})
		}
	}
}
