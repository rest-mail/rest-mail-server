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
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

const sieveInstallationPath = "/api/v1/accounts/{id}/sieve"

func TestSieveInstallationExamples(t *testing.T) {
	f := newSieveInstallationScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["putSieveScript"]) < 30 {
		t.Fatal("incomplete installation examples")
	}
	for name, pair := range pairs["putSieveScript"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f.scenarios[name]
			if !ok {
				t.Fatalf("missing scenario %s", name)
			}
			var body []byte
			if name == "malformed_json" {
				body = []byte("{")
			} else if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 257)
			} else {
				body, err = json.Marshal(pair.Request.Body)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := len(f.store.snapshot())
			req := httptest.NewRequest(http.MethodPut, messageCollectionReadURI(sieveInstallationPath, pair), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if s.token != "" {
				req.Header.Set("Authorization", "Bearer "+s.token)
			}
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			calls := f.store.snapshot()[before:]
			wantRead := rr.Code == 200 || rr.Code == 201 || name == "lookup_unavailable" || name == "create_unavailable" || name == "update_unavailable"
			wantWrite := rr.Code == 200 || rr.Code == 201 || name == "create_unavailable" || name == "update_unavailable"
			reads, writes := 0, 0
			for _, m := range calls {
				if strings.HasPrefix(m.query, "SELECT") {
					reads++
					if m.args[0].Value != int64(pair.Request.Path["id"].(float64)) {
						t.Fatal("lookup changed mailbox")
					}
					continue
				}
				writes++
				fields, err := sieveInstallationFields(m.query, m.args)
				if err != nil {
					t.Fatal(err)
				}
				if fields["mailbox_id"] != int64(pair.Request.Path["id"].(float64)) {
					t.Fatal("write changed mailbox")
				}
				if rr.Code < 300 {
					var envelope struct{ Data map[string]interface{} }
					if err := json.Unmarshal(pair.Response.Body, &envelope); err != nil {
						t.Fatal(err)
					}
					expected := envelope.Data
					if fields["script"] != expected["script"] || fields["script_hash"] != expected["script_hash"] || fields["active"] != expected["active"] {
						t.Fatalf("stored script/hash/active differs: %v", fields)
					}
					for _, key := range []string{"validated_at", "created_at", "updated_at"} {
						stamp, ok := fields[key].(time.Time)
						if !ok || !stamp.Equal(sieveInstallationTime) {
							t.Fatalf("%s=%v, want persistence clock", key, fields[key])
						}
					}
				}
			}
			if (reads == 1) != wantRead || reads > 1 || (writes == 1) != wantWrite || writes > 1 {
				t.Fatalf("installation lookup/write=%d/%d want %t/%t", reads, writes, wantRead, wantWrite)
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "putSieveScript" {
				t.Errorf("installation warning:%v", w)
			}
		}
	}
}

func TestSieveInstallationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for Sieve install exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newSieveInstallationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["putSieveScript"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f.scenarios[name]
			if !ok {
				t.Fatalf("missing scenario %s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{s, nil}, http.MethodPut, sieveInstallationPath, name, pair, 257)
		})
	}
}
