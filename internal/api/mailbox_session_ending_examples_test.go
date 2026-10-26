package api

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

const mailboxSessionEndingPath = "/api/v1/admin/mailboxes/{id}/sessions"

func TestMailboxSessionEndingExamples(t *testing.T) {
	f := newMailboxSessionEndingScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["endMailboxSessions"]) < 20 {
		t.Fatal("session ending examples incomplete")
	}
	for name, pair := range pairs["endMailboxSessions"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario%s", name)
			}
			before := 0
			if s.store != nil {
				before = len(s.store.snapshot())
			}
			started := time.Now()
			var body []byte
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 513)
			}
			rr := mailboxSessionEndingRequest(t, s, http.MethodDelete, messageCollectionReadURI(mailboxSessionEndingPath, pair), body)
			finished := time.Now()
			if rr.Code != pair.Response.Status {
				t.Fatalf("got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
			}
			if rr.Code == 204 {
				if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" {
					t.Fatal("204 carried content")
				}
			} else {
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			}
			var calls []bulkQueueExampleMutation
			if s.store != nil {
				calls = s.store.snapshot()[before:]
			}
			writes, begins, commits, rollbacks := 0, 0, 0, 0
			for _, m := range calls {
				switch m.query {
				case "BEGIN":
					begins++
				case "COMMIT":
					commits++
				case "ROLLBACK":
					rollbacks++
				default:
					if strings.HasPrefix(m.query, "SELECT") {
						continue
					}
					writes++
					id := int64(pair.Request.Path["id"].(float64))
					if strings.HasPrefix(m.query, `UPDATE "mailboxes"`) {
						if len(m.args) != 2 || m.args[1].Value != id {
							t.Fatal("epoch targeted wrong mailbox")
						}
					} else if strings.HasPrefix(m.query, `UPDATE "refresh_tokens"`) {
						if len(m.args) != 4 || m.args[2].Value != id {
							t.Fatal("refresh revocation targeted wrong account")
						}
						stamp, ok := m.args[1].Value.(time.Time)
						if !ok || stamp.Before(started) || stamp.After(finished) {
							t.Fatal("refresh revocation lost request-time timestamp")
						}
					} else {
						t.Fatal("session ending changed another resource")
					}
				}
			}
			wantWrites, wantCommit, wantRollback := 0, 0, 0
			if rr.Code == 204 {
				wantWrites = 2
				wantCommit = 1
			}
			if name == "epoch_unavailable" {
				wantWrites = 1
				wantRollback = 1
			}
			if name == "refresh_unavailable" {
				wantWrites = 2
				wantRollback = 1
			}
			if writes != wantWrites || begins != wantCommit+wantRollback || commits != wantCommit || rollbacks != wantRollback {
				t.Fatalf("write/begin/commit/rollback %d/%d/%d/%d want%d/%d/%d/%d", writes, begins, commits, rollbacks, wantWrites, wantCommit+wantRollback, wantCommit, wantRollback)
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "endMailboxSessions" {
				t.Errorf("session ending warning:%v", w)
			}
		}
	}
}

func TestMailboxSessionEndingExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for session ending exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newMailboxSessionEndingScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["endMailboxSessions"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario%s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, s, http.MethodDelete, mailboxSessionEndingPath, name, pair, 513)
		})
	}
}
