package api

import (
	"bytes"
	"os"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

var domainCleanupScenarios = []struct {
	name, mode string
	status     int
}{
	{"configuration_removed", "", 204}, {"accounts_remain", "account", 409}, {"aliases_remain", "alias", 409},
	{"cleanup_failed", "cleanup_failure", 500}, {"grant_audit_failed", "audit_failure", 500},
	{"unauthenticated", "anonymous", 401}, {"delete_not_allowed", "denied", 403}, {"domain_missing", "missing", 404},
}

func domainCleanupToken(f *domainCleanupFixture, mode string) string {
	if mode == "anonymous" {
		return ""
	}
	if mode == "denied" {
		return f.reader
	}
	return f.admin
}

func assertDomainCleanupResponse(t *testing.T, f *domainCleanupFixture, status int) {
	t.Helper()
	if status == 204 {
		if _, ok := f.c.ledger.domains[7]; ok {
			t.Fatal("204 without domain deletion")
		}
		for table, rows := range f.c.ledger.live {
			if rows[7] != 0 || rows[8] != 1 || rows[0] != 1 {
				t.Fatalf("204 without isolated cleanup: %s %v", table, rows)
			}
		}
		if len(f.c.ledger.grants) != 1 || f.c.ledger.grants[0]["scope"] != "domain:other.test" {
			t.Fatal("204 without scoped grant removal")
		}
	} else {
		for table, rows := range f.c.ledger.live {
			if rows[7] != 1 || rows[8] != 1 || rows[0] != 1 {
				t.Fatalf("refusal left partial cleanup: %s %v", table, rows)
			}
		}
		if len(f.c.ledger.grants) != 3 {
			t.Fatal("refusal removed grants")
		}
	}
}

func TestDomainCleanupCanonicalExamples(t *testing.T) {
	useRealGrants(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	recorded := oapifly.ExampleFile{"deleteDomain": {}}
	for _, tc := range domainCleanupScenarios {
		t.Run(tc.name, func(t *testing.T) {
			f := newDomainCleanupFixture(t, tc.mode)
			if tc.mode == "missing" {
				delete(f.c.ledger.domains, 7)
			}
			w := f.request(t.Context(), "DELETE", "/api/v1/admin/domains/7", "", domainCleanupToken(f, tc.mode))
			if w.Code != tc.status {
				t.Fatalf("got%d want%d: %s", w.Code, tc.status, w.Body)
			}
			assertDomainCleanupResponse(t, f, w.Code)
			pair := oapifly.ExamplePair{Request: oapifly.ExampleRequest{Path: map[string]any{"id": 7}}, Response: oapifly.ExampleResponse{Status: w.Code}}
			if w.Code != 204 {
				pair.Response.Body = bytes.TrimSpace(w.Body.Bytes())
				pair.Response.MediaType = "application/json"
			}
			recorded["deleteDomain"][tc.name] = pair
			if os.Getenv("RESTMAIL_DOMAIN_CLEANUP_RECORD") == "" {
				want, ok := pairs["deleteDomain"][tc.name]
				if !ok {
					t.Fatal("canonical cleanup pair missing")
				}
				if want.Response.Status != w.Code {
					t.Fatal("canonical status differs")
				}
				if w.Code == 204 {
					if len(w.Body.Bytes()) != 0 || len(want.Response.Body) != 0 {
						t.Fatal("204 carried a body")
					}
				} else {
					assertPublicExampleBody(t, want.Response.Body, pair.Response.Body)
				}
				if string(mustImportJSON(t, want.Request.Path)) != string(mustImportJSON(t, pair.Request.Path)) {
					t.Fatal("canonical path differs")
				}
			}
		})
	}
	if path := os.Getenv("RESTMAIL_DOMAIN_CLEANUP_RECORD"); path != "" {
		data, err := oapifly.MarshalExamples(recorded)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
