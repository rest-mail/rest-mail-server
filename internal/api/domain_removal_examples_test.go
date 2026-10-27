package api

import (
	"bytes"
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

const domainRemovalPath = "/api/v1/admin/domains/{id}"

func TestDomainRemovalExamples(t *testing.T) {
	f := newDomainRemovalScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["deleteDomain"]) != 34 {
		t.Fatalf("domain removal pairs:%d want34", len(pairs["deleteDomain"]))
	}
	for name, pair := range pairs["deleteDomain"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario:%s", name)
			}
			body := []byte(pair.Request.Body)
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 513)
			}
			if name == "body_boundary" {
				body = bytes.Repeat([]byte(" "), 512)
			}
			before := 0
			if s.store != nil {
				before = len(s.store.snapshot())
			}
			r := httptest.NewRequest(http.MethodDelete, messageCollectionReadURI(domainRemovalPath, pair), bytes.NewReader(body))
			if s.token != "" {
				r.Header.Set("Authorization", "Bearer "+s.token)
			}
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, r)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
			}
			if rr.Code == http.StatusNoContent {
				if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" {
					t.Fatalf("204 has representation: %s %v", rr.Body, rr.Header())
				}
			} else {
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			}
			var calls []bulkQueueExampleMutation
			if s.store != nil {
				calls = s.store.snapshot()[before:]
			}
			var deletes []bulkQueueExampleMutation
			cleanups := 0
			counts := []string{}
			for _, call := range calls {
				if strings.HasPrefix(call.query, `DELETE FROM "domains"`) {
					deletes = append(deletes, call)
				} else if strings.HasPrefix(call.query, "DELETE") {
					cleanups++
				}
				for _, table := range []string{"mailboxes", "aliases"} {
					if strings.HasPrefix(call.query, `SELECT count(*) FROM "`+table+`"`) {
						counts = append(counts, table)
					}
				}
			}
			wantWrite := rr.Code == 204 || name == "delete_unavailable"
			if (len(deletes) == 1) != wantWrite || len(deletes) > 1 {
				t.Fatalf("deletion writes:%d want%t", len(deletes), wantWrite)
			}
			// The domain's own DELETE comes after its configuration is cleared.
			if (cleanups == len(domainOwnedTables)) != wantWrite || (!wantWrite && cleanups != 0) {
				t.Fatalf("configuration cleanups:%d want%t", cleanups, wantWrite)
			}
			if len(deletes) == 1 {
				if len(deletes[0].args) != 1 || deletes[0].args[0].Value != int64(pair.Request.Path["id"].(float64)) {
					t.Fatal("delete selected another domain")
				}
			}
			wantCounts := ""
			switch {
			case rr.Code == 204 || name == "delete_unavailable" || name == "has_aliases" || name == "alias_count_unavailable":
				wantCounts = "mailboxes,aliases"
			case name == "has_accounts" || name == "account_count_unavailable":
				wantCounts = "mailboxes"
			}
			if strings.Join(counts, ",") != wantCounts {
				t.Fatalf("address guards:%v want%s", counts, wantCounts)
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "deleteDomain" {
				t.Errorf("domain removal warning:%v", w)
			}
		}
	}
}

func TestDomainRemovalExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for domain removal exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newDomainRemovalScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["deleteDomain"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario:%s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, s, http.MethodDelete, domainRemovalPath, name, pair, 513)
		})
	}
}
