package api

import (
	"bytes"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server"
)

func TestTestDataMutationExamples(t *testing.T) {
	f := newTestDataMutationScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range testDataMutationPaths {
		if len(pairs[op]) < 20 {
			t.Fatalf("incomplete mutation examples%s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, op, name)
				s, ok := f[name]
				if !ok {
					t.Fatalf("missing scenario%s", name)
				}
				before := 0
				if s.store != nil {
					before = len(s.store.snapshot())
				}
				var body []byte
				if name == "request_too_large" {
					body = bytes.Repeat([]byte("x"), 513)
				}
				rr := mailboxSessionEndingRequest(t, s, http.MethodPost, path, body)
				if rr.Code != pair.Response.Status {
					t.Fatalf("status%d want%d", rr.Code, pair.Response.Status)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				calls := []bulkQueueExampleMutation{}
				if s.store != nil {
					calls = s.store.snapshot()[before:]
				}
				begins, commits, rollbacks, passwords, clears, restarts, domainRestarts := 0, 0, 0, 0, 0, 0, 0
				lookups := map[string]int{}
				deleted := map[string]bool{}
				for _, c := range calls {
					switch c.query {
					case "BEGIN":
						begins++
					case "COMMIT":
						commits++
					case "ROLLBACK":
						rollbacks++
					case "PASSWORD_CREATED":
						passwords++
					case "USAGE_RESTARTED":
						restarts++
					case "DOMAINS_RESTARTED":
						domainRestarts++
					case "DATA_CONTEXT":
						if c.args[0].Value != true {
							t.Fatal("test-data work lost HTTP context")
						}
					}
					if strings.HasPrefix(c.query, "DELETE FROM ") {
						clears++
						deleted[strings.Fields(c.query)[2]] = true
					}
					for _, table := range []string{"domains", "mailboxes", "quota_usage", "aliases"} {
						if strings.Contains(c.query, `FROM "`+table+`"`) && strings.HasPrefix(c.query, "SELECT") {
							lookups[table]++
						}
					}
				}
				if rr.Code == 200 {
					if begins != 1 || commits != 1 || rollbacks != 0 {
						t.Fatalf("successful fixture not one transaction: begin%d commit%d rollback%d", begins, commits, rollbacks)
					}
					expectedPasswords := 0
					if op == "resetTestData" || name == "seed_created" {
						expectedPasswords = 9
					}
					if passwords != expectedPasswords {
						t.Fatalf("password creations%d want%d", passwords, expectedPasswords)
					}
					for table, n := range map[string]int{"domains": 3, "mailboxes": 9, "quota_usage": 9, "aliases": 4} {
						if lookups[table] != n {
							t.Fatalf("%s lookups%d want%d", table, lookups[table], n)
						}
					}
					if op == "resetTestData" {
						for _, table := range []string{"quota_usage", "quarantine", "domain_sender_rules", "suppressions", "refresh_tokens"} {
							if !deleted[table] {
								t.Fatalf("reset did not target actual model table%s", table)
							}
						}
						expectedClears := len(testResetScenarioTables) - 4
						if name == "missing_table" {
							expectedClears--
						}
						if clears != expectedClears {
							t.Fatalf("clear statements%d want%d", clears, expectedClears)
						}
						if restarts != 1 {
							t.Fatalf("retained accounts' usage restarted%d times want1", restarts)
						}
						if domainRestarts != 1 {
							t.Fatalf("retained domains' settings restarted%d times want1", domainRestarts)
						}
					} else if clears != 0 || restarts != 0 || domainRestarts != 0 {
						t.Fatal("seed cleared existing data")
					}
				} else if name == "commit_failure" {
					if begins != 1 || commits != 0 {
						t.Fatal("failed commit reported a successful commit")
					}
				} else if rr.Code == 500 {
					if begins != 1 || commits != 0 || rollbacks != 1 {
						t.Fatalf("failed mutation escaped rollback: begin%d commit%d rollback%d", begins, commits, rollbacks)
					}
				} else if len(calls) != 0 {
					t.Fatal("refused mutation accessed fixture resources")
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := testDataMutationPaths[w.OperationID]; ok && gapWarningApplies(map[string]gapOperation{w.OperationID: {}}, w) {
				t.Errorf("mutation warning:%v", w)
			}
		}
	}
}

func TestTestDataMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newTestDataMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range testDataMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				checkQueueMutationPairWithVertrag(t, binary, f[name], http.MethodPost, path, name, pair, 513)
			})
		}
	}
}
