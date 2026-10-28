package api

import (
	"bytes"
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

const mailboxRemovalPath = "/api/v1/admin/mailboxes/{id}"

func TestMailboxRemovalExamples(t *testing.T) {
	f := newMailboxRemovalScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["deleteMailbox"]) != 38 {
		t.Fatalf("mailbox removal pairs:%d want38", len(pairs["deleteMailbox"]))
	}
	for name, pair := range pairs["deleteMailbox"] {
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "deleteMailbox", name)
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
			rr := mailboxSessionEndingRequest(t, s, http.MethodDelete, messageCollectionReadURI(mailboxRemovalPath, pair), body)
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
			begins, commits, rollbacks, deletes, credentials, refresh := 0, 0, 0, 0, 0, 0
			for _, q := range calls {
				switch q.query {
				case "BEGIN":
					begins++
				case "COMMIT":
					commits++
				case "ROLLBACK":
					rollbacks++
				}
				if strings.HasPrefix(q.query, `DELETE FROM "mailboxes"`) {
					deletes++
					if len(q.args) != 1 || q.args[0].Value != int64(pair.Request.Path["id"].(float64)) {
						t.Fatal("deleted another mailbox")
					}
				}
				if strings.HasPrefix(q.query, `UPDATE "credentials"`) {
					credentials++
					if len(q.args) != 7 || q.args[1].Value != int64(pair.Request.Path["id"].(float64)) {
						t.Fatal("credential target/kinds changed")
					}
					for _, kind := range []string{"password", "totp", "recovery_code", "app_password", "api_key"} {
						found := false
						for _, arg := range q.args[2:] {
							if arg.Value == kind {
								found = true
							}
						}
						if !found {
							t.Fatalf("credential kind missing:%s", kind)
						}
					}
				}
				if strings.HasPrefix(q.query, `UPDATE "refresh_tokens"`) {
					refresh++
				}
			}
			wantDelete := rr.Code == 204 || name == "delete_unavailable"
			if (deletes == 1) != wantDelete || deletes > 1 {
				t.Fatalf("mailbox deletes:%d want%t", deletes, wantDelete)
			}
			if rr.Code == 204 {
				if begins != 1 || commits != 1 || rollbacks != 0 || credentials != 1 || refresh != 1 {
					t.Fatalf("incomplete committed revocation:%d/%d/%d/%d/%d", begins, commits, rollbacks, credentials, refresh)
				}
			} else {
				if commits != 0 || (begins > 0 && rollbacks != 1) {
					t.Fatalf("failed deletion committed:%d/%d/%d", begins, commits, rollbacks)
				}
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "deleteMailbox" {
				t.Errorf("mailbox removal warning:%v", w)
			}
		}
	}
}

func TestMailboxRemovalExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for mailbox deletion exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newMailboxRemovalScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["deleteMailbox"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario:%s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, s, http.MethodDelete, mailboxRemovalPath, name, pair, 513)
		})
	}
}
