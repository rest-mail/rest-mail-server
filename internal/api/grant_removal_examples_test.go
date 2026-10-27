package api

import (
	"bytes"
	"fmt"
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/grants"
)

const grantRemovalPath = "/api/v1/admin/grants/{id}"

func TestGrantRemovalExamples(t *testing.T) {
	f := newGrantRemovalScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["deleteGrant"]) != 43 {
		t.Fatalf("grant removal pairs:%d want43", len(pairs["deleteGrant"]))
	}
	for name, pair := range pairs["deleteGrant"] {
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
			rr := mailboxSessionEndingRequest(t, s, http.MethodDelete, messageCollectionReadURI(grantRemovalPath, pair), body)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
			}
			if rr.Code == 204 {
				if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" {
					t.Fatal("204 has a representation")
				}
			} else {
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			}
			var calls []bulkQueueExampleMutation
			if s.store != nil {
				calls = s.store.snapshot()[before:]
			}
			begins, commits, rollbacks, deletes, audits := 0, 0, 0, 0, 0
			var actions []string
			for _, q := range calls {
				switch q.query {
				case "BEGIN":
					begins++
				case "COMMIT":
					commits++
				case "ROLLBACK":
					rollbacks++
				}
				if strings.HasPrefix(q.query, `DELETE FROM "grants"`) || strings.HasPrefix(q.query, `DELETE FROM "grant_activations"`) {
					deletes++
					if len(q.args) != 1 || q.args[0].Value != int64(pair.Request.Path["id"].(float64)) {
						t.Fatal("grant removal selected another grant")
					}
				}
				if strings.HasPrefix(q.query, `INSERT INTO "audit_log"`) {
					fields, err := revocationAuditFields(q.query, q.args)
					if err != nil {
						t.Fatal(err)
					}
					action := fmt.Sprint(fields["action"])
					if strings.HasPrefix(action, "grants.") {
						audits++
						actions = append(actions, action)
					}
				}
			}
			if rr.Code == 204 {
				wantAudits := 1
				if name == "live_activation" {
					wantAudits = 2
				}
				if begins != 1 || commits != 1 || rollbacks != 0 || deletes != 2 || audits != wantAudits {
					t.Fatalf("grant/audit atomic wiring:%d/%d/%d deletes%d audits%d", begins, commits, rollbacks, deletes, audits)
				}
				if actions[len(actions)-1] != grants.ActionDelete || name == "live_activation" && actions[0] != grants.ActionDeactivate {
					t.Fatalf("grant audit actions:%v", actions)
				}
			} else {
				if commits != 0 || begins > 0 && rollbacks != 1 {
					t.Fatalf("failed deletion committed:%d/%d/%d", begins, commits, rollbacks)
				}
				if (name == "two_factor_required" || name == "read_only" || strings.HasPrefix(name, "key_") || name == "domain_scope" || name == "mailbox_scope") && deletes != 0 {
					t.Fatal("early refusal mutated a grant")
				}
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "deleteGrant" {
				t.Errorf("grant removal warning:%v", w)
			}
		}
	}
}

func TestGrantRemovalExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for grant deletion exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newGrantRemovalScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["deleteGrant"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario:%s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, s, http.MethodDelete, grantRemovalPath, name, pair, 513)
		})
	}
}
