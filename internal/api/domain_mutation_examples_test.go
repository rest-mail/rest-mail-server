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
	"github.com/restmail/restmail/internal/pipeline"
)

var domainMutationPaths = map[string]string{"createDomain": "/api/v1/admin/domains", "updateDomain": "/api/v1/admin/domains/{id}"}

func TestDomainMutationExamples(t *testing.T) {
	f := newDomainMutationScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range domainMutationPaths {
		if len(pairs[op]) < 30 {
			t.Fatalf("incomplete operation%s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, op, name)
				s, ok := f[name]
				if !ok {
					t.Fatalf("missing scenario%s", name)
				}
				method := http.MethodPost
				if op == "updateDomain" {
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
						t.Fatal("oversized boundary")
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
				var calls []bulkQueueExampleMutation
				if s.store != nil {
					calls = s.store.snapshot()[before:]
				}
				var writes []bulkQueueExampleMutation
				begins, commits, rollbacks := 0, 0, 0
				for _, m := range calls {
					switch m.query {
					case "BEGIN":
						begins++
					case "COMMIT":
						commits++
					case "ROLLBACK":
						rollbacks++
					default:
						if !strings.HasPrefix(m.query, "SELECT") {
							writes = append(writes, m)
						}
					}
				}
				if op == "createDomain" {
					wantWrites := 0
					wantCommit := 0
					wantRollback := 0
					if rr.Code == 201 {
						wantWrites = 3
						wantCommit = 1
					}
					if name == "duplicate" || name == "write_unavailable" {
						wantWrites = 1
						wantRollback = 1
					}
					if name == "inbound_unavailable" {
						wantWrites = 2
						wantRollback = 1
					}
					if name == "outbound_unavailable" {
						wantWrites = 3
						wantRollback = 1
					}
					if len(writes) != wantWrites || commits != wantCommit || rollbacks != wantRollback || begins != wantCommit+wantRollback {
						t.Fatalf("writes/begin/commit/rollback %d/%d/%d/%d want%d/%d/%d/%d", len(writes), begins, commits, rollbacks, wantWrites, wantCommit+wantRollback, wantCommit, wantRollback)
					}
					if rr.Code == 201 {
						var answer struct{ Data map[string]interface{} }
						if err := json.Unmarshal(pair.Response.Body, &answer); err != nil {
							t.Fatal(err)
						}
						fields, err := exampleInsertFields(writes[0].query, writes[0].args)
						if err != nil {
							t.Fatal(err)
						}
						if fields["name"] != answer.Data["name"] || fields["server_type"] != answer.Data["server_type"] || fields["active"] != false || fields["default_quota_bytes"] != int64(answer.Data["default_quota_bytes"].(float64)) || fields["sending_policy"] != answer.Data["sending_policy"] || fields["dmarc_policy"] != answer.Data["dmarc_policy"] || fields["created_at"] != sieveInstallationTime || fields["updated_at"] != sieveInstallationTime {
							t.Fatal("created domain fields differ from full answer")
						}
						for i, direction := range []string{"inbound", "outbound"} {
							fields, err := exampleInsertFields(writes[i+1].query, writes[i+1].args)
							if err != nil {
								t.Fatal(err)
							}
							if fields["domain_id"] != int64(1) || fields["direction"] != direction || fields["active"] != true {
								t.Fatal("default pipeline lost domain/direction/active")
							}
							raw, ok := fields["filters"].([]byte)
							if !ok {
								t.Fatalf("pipeline filters type%T", fields["filters"])
							}
							var got, want interface{}
							if err := json.Unmarshal(raw, &got); err != nil {
								t.Fatal(err)
							}
							if err := json.Unmarshal(pipeline.DefaultPipelineJSON(direction), &want); err != nil {
								t.Fatal(err)
							}
							a, _ := json.Marshal(got)
							b, _ := json.Marshal(want)
							if !bytes.Equal(a, b) {
								t.Fatal("default pipeline content differs")
							}
						}
					}
				} else {
					want := rr.Code == 200 && name != "unchanged" && name != "null_fields" || name == "update_unavailable" || name == "reload_unavailable" || name == "reload_missing"
					if (len(writes) == 1) != want || len(writes) > 1 {
						t.Fatalf("update writes%d want%t", len(writes), want)
					}
					if len(writes) == 1 {
						fields, err := vacationMutationFields(writes[0].query, writes[0].args)
						if err != nil {
							t.Fatal(err)
						}
						if fields["id"] != int64(pair.Request.Path["id"].(float64)) {
							t.Fatal("update changed domain identity")
						}
						if _, ok := fields["name"]; ok {
							t.Fatal("update renamed domain")
						}
						if _, ok := fields["dkim_private_key"]; ok {
							t.Fatal("update changed private key")
						}
						if rr.Code == 200 {
							var a struct{ Data map[string]interface{} }
							if err := json.Unmarshal(pair.Response.Body, &a); err != nil {
								t.Fatal(err)
							}
							for k, v := range fields {
								if k == "id" || k == "updated_at" {
									continue
								}
								expected := a.Data[k]
								if k == "default_quota_bytes" {
									expected = int64(expected.(float64))
								}
								if v != expected {
									t.Fatalf("updated%s=%v want%v", k, v, expected)
								}
							}
						}
					}
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := domainMutationPaths[w.OperationID]; ok {
				t.Errorf("domain mutation warning:%v", w)
			}
		}
	}
}

func TestDomainMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for domain mutation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newDomainMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range domainMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f[name]
				if !ok {
					t.Fatalf("missing scenario%s", name)
				}
				method := http.MethodPost
				if op == "updateDomain" {
					method = http.MethodPatch
				}
				checkQueueMutationPairWithVertrag(t, binary, s, method, path, name, pair, 513)
			})
		}
	}
}
