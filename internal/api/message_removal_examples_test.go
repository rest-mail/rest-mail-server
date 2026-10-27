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

const messageRemovalPath = "/api/v1/messages/{id}"

func TestMessageRemovalExamples(t *testing.T) {
	f := newMessageRemovalScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["deleteMessage"]) != 44 {
		t.Fatalf("message removal pairs:%d want44", len(pairs["deleteMessage"]))
	}
	for name, pair := range pairs["deleteMessage"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f.scenarios[name]
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
			before := len(f.store.snapshot())
			rr := mailboxSessionEndingRequest(t, bulkQueueExampleScenario{accountReadScenario: s, store: f.store}, http.MethodDelete, messageCollectionReadURI(messageRemovalPath, pair), body)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
			}
			if rr.Code == 204 {
				if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" {
					t.Fatal("204 has representation")
				}
			} else {
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			}
			calls := f.store.snapshot()[before:]
			begins, commits, rollbacks, moves, attDeletes, msgDeletes, sums, quota := 0, 0, 0, 0, 0, 0, 0, 0
			numeric, _ := pair.Request.Path["id"].(float64)
			id := int64(numeric)
			var quotaAmount int64
			for _, q := range calls {
				switch q.query {
				case "BEGIN":
					begins++
				case "COMMIT":
					commits++
				case "ROLLBACK":
					rollbacks++
				}
				if strings.HasPrefix(q.query, `UPDATE "messages"`) {
					moves++
				}
				if strings.HasPrefix(q.query, `DELETE FROM "attachments"`) {
					attDeletes++
				}
				if strings.HasPrefix(q.query, `DELETE FROM "messages"`) {
					msgDeletes++
				}
				if strings.Contains(q.query, "SUM(size_bytes)") {
					sums++
				}
				if strings.HasPrefix(q.query, `UPDATE "mailboxes"`) {
					quota++
					quotaAmount = q.args[0].Value.(int64)
				}
			}
			if rr.Code == 204 {
				hard := id == 31 || id == 32 || pair.Request.Query["permanent"] == "true"
				if begins != 1 || commits != 1 || rollbacks != 0 {
					t.Fatalf("mutation commit:%d/%d/%d", begins, commits, rollbacks)
				}
				if !hard {
					if moves != 1 || attDeletes+msgDeletes+sums+quota != 0 {
						t.Fatal("Trash move changed stored size or deleted rows")
					}
				} else {
					wantAtt := 1
					if id == 33 || id == 34 {
						wantAtt = 0
					}
					wantSum := wantAtt
					if id == 32 {
						wantSum = 0
					}
					wantBytes := int64(180)
					if id == 33 || id == 41 {
						wantBytes = 80
					}
					if id == 32 || id == 34 || id == 40 {
						wantBytes = 0
					}
					wantQuota := 0
					if wantBytes > 0 {
						wantQuota = 1
					}
					if moves != 0 || msgDeletes != 1 || attDeletes != wantAtt || sums != wantSum || quota != wantQuota || quotaAmount != wantBytes {
						t.Fatalf("purge wiring moves%d msg%d att%d sums%d quota%d amount%d wantAtt%d wantSum%d wantBytes%d", moves, msgDeletes, attDeletes, sums, quota, quotaAmount, wantAtt, wantSum, wantBytes)
					}
				}
			} else {
				if commits != 0 || begins > 0 && rollbacks != 1 {
					t.Fatalf("failed deletion committed:%d/%d/%d", begins, commits, rollbacks)
				}
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "deleteMessage" {
				t.Errorf("message removal warning:%v", w)
			}
		}
	}
}

func TestMessageRemovalExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for message deletion exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newMessageRemovalScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["deleteMessage"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f.scenarios[name]
			if !ok {
				t.Fatalf("missing scenario:%s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{accountReadScenario: s, store: f.store}, http.MethodDelete, messageRemovalPath, name, pair, 513)
		})
	}
}
