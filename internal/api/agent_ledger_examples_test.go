package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// agentVolatile are the fields of a ledger entry only the run can know.
var agentVolatile = []string{"occurred_at", "undo_until", "request_id", "prev_hash", "hash"}

// assertAgentEntryExample compares the answer with the pair once the fields only
// the run can know are the pair's, having checked each is filled in as the pair's is.
func assertAgentEntryExample(t *testing.T, pair oapifly.ExamplePair, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != pair.Response.Status {
		t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
	}
	var got, want map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
		t.Fatal(err)
	}
	gotEntries, wantEntries := agentEntriesOf(got), agentEntriesOf(want)
	if len(gotEntries) != len(wantEntries) {
		t.Fatalf("got %s, want %s", rr.Body.String(), pair.Response.Body)
	}
	for i, entry := range gotEntries {
		for _, field := range agentVolatile {
			if (entry[field] == nil) != (wantEntries[i][field] == nil) {
				t.Fatalf("%s: got %v, want %v", field, entry[field], wantEntries[i][field])
			}
			entry[field] = wantEntries[i][field]
		}
	}
	rewriteBody(t, rr, got)
	assertRequirementResponse(t, pair, rr)
}

func agentEntriesOf(body map[string]any) []map[string]any {
	switch data := body["data"].(type) {
	case map[string]any:
		return []map[string]any{data}
	case []any:
		out := make([]map[string]any, len(data))
		for i, entry := range data {
			out[i], _ = entry.(map[string]any)
		}
		return out
	}
	return nil
}

// TestAgentLedgerExamples proves the pairs that need a state of their own, through
// the real router on a database of their own. The live run sends none of these:
// the ledger is append-only, so what it wrote would outlive the run's own resets.
func TestAgentLedgerExamples(t *testing.T) {
	pairs := clientSecretExamplePairs(t)
	routes := requirementRoutes(t)
	send := func(f *agentFixture, op string, pair oapifly.ExamplePair, authorization string) *httptest.ResponseRecorder {
		f.t.Helper()
		return sendAccountExample(f.credentialFixture, routes[op], withPathID(pair, f.accountID), authorization)
	}
	entries := func(f *agentFixture) int {
		return len(f.ledgerRows())
	}

	for _, tc := range []struct {
		op, name string
		window   time.Duration
		// before puts the account in the state the pair names.
		before func(*agentFixture)
		// session is who sends the pair; f.session when nil.
		session func(*agentFixture) string
		// entry says the answer is a ledger entry or a page of them.
		entry bool
		// after checks what the request changed.
		after func(*testing.T, *agentFixture)
	}{
		{op: "appendAgentLedgerEntry", name: "suggested", entry: true, after: func(t *testing.T, f *agentFixture) {
			if entries(f) != 1 {
				t.Fatalf("%d entries", entries(f))
			}
		}},
		{op: "appendAgentLedgerEntry", name: "automatic", window: time.Hour, entry: true,
			before: func(f *agentFixture) { f.setTrust(`{"file_newsletters":"auto"}`) }},
		{op: "appendAgentLedgerEntry", name: "undone", window: time.Hour, entry: true,
			before: func(f *agentFixture) { f.setTrust(`{"file_newsletters":"auto"}`); f.write(autoFile) }},
		{op: "appendAgentLedgerEntry", name: "already_undone", window: time.Hour,
			before: func(f *agentFixture) {
				f.setTrust(`{"file_newsletters":"auto"}`)
				f.write(autoFile)
				f.write(`{"undoes":1}`)
			}},
		{op: "appendAgentLedgerEntry", name: "undo_window_closed",
			before: func(f *agentFixture) { f.setTrust(`{"file_newsletters":"approve"}`); f.write(approveFile) }},
		{op: "appendAgentLedgerEntry", name: "trust_level_too_low", after: func(t *testing.T, f *agentFixture) {
			if entries(f) != 0 {
				t.Fatal("a refused entry was written")
			}
		}},
		{op: "appendAgentLedgerEntry", name: "no_undo_window",
			before: func(f *agentFixture) {
				f.db.Create(&models.AgentTrust{MailboxID: f.accountID, Capability: "file_newsletters", Level: "auto", UpdatedAt: time.Now()})
			}},
		{op: "appendAgentLedgerEntry", name: "unknown_entry"},
		{op: "updateAgentTrust", name: "updated", after: func(t *testing.T, f *agentFixture) {
			if levels := f.levels(); levels["file_newsletters"] != "approve" || levels["send_mail"] != "suggest" {
				t.Fatalf("levels: %v", levels)
			}
		}},
		{op: "updateAgentTrust", name: "automatic", window: time.Hour},
		{op: "updateAgentTrust", name: "unchanged"},
		{op: "updateAgentTrust", name: "no_undo_window", after: func(t *testing.T, f *agentFixture) {
			if levels := f.levels(); levels["file_newsletters"] != "suggest" {
				t.Fatalf("levels: %v", levels)
			}
		}},
		{op: "updateAgentTrust", name: "two_factor_required", session: func(f *agentFixture) string {
			pair, err := f.jwt.GenerateSession(f.accountID, f.address, 0, []string{auth.AMRPassword}, false)
			if err != nil {
				f.t.Fatal(err)
			}
			return "Bearer " + pair.AccessToken
		}},
		{op: "updateAgentTrust", name: "recent_two_factor_required", session: func(f *agentFixture) string { return f.staleSession() }},
		{op: "listAgentLedger", name: "empty", entry: true},
		{op: "listAgentLedger", name: "entries", window: time.Hour, entry: true,
			before: func(f *agentFixture) {
				f.setTrust(`{"file_newsletters":"auto"}`)
				f.write(autoFile)
				f.write(`{"undoes":1}`)
			}},
		{op: "listAgentLedger", name: "without_read", session: func(f *agentFixture) string {
			return f.apiKey(`[{"capability":"mail:write","scope":"mailbox:chris@example.test"}]`)
		}},
	} {
		t.Run(tc.op+"/"+tc.name, func(t *testing.T) {
			examplepairs.Proven(t, tc.op, tc.name)
			pair := pairs[tc.op][tc.name]
			f := newAgentFixture(t, tc.window)
			if tc.before != nil {
				tc.before(f)
			}
			session := f.session
			if tc.session != nil {
				session = tc.session(f)
			}
			rr := send(f, tc.op, pair, session)
			if tc.entry {
				assertAgentEntryExample(t, pair, rr)
			} else {
				assertRequirementResponse(t, pair, rr)
			}
			if tc.after != nil {
				tc.after(t, f)
			}
		})
	}

	t.Run("appendAgentLedgerEntry/own_session_required", func(t *testing.T) {
		examplepairs.Proven(t, "appendAgentLedgerEntry", "own_session_required")
		pair := pairs["appendAgentLedgerEntry"]["own_session_required"]
		f := newAgentFixture(t, 0)
		owned := f.seedMailbox(f.domainID(), "owned")
		f.own(owned, "owned@example.test")
		rr := sendAccountExample(f.credentialFixture, routes["appendAgentLedgerEntry"], withPathID(pair, owned), f.session)
		assertRequirementResponse(t, pair, rr)
		var count int64
		f.db.Model(&models.AgentLedgerEntry{}).Count(&count)
		if count != 0 {
			t.Fatalf("%d entries written", count)
		}
	})
}

// The routes the live run does not write through still answer the session it uses.
func TestAgentLedgerLiveExamplesFitAPasswordOnlySession(t *testing.T) {
	pairs := clientSecretExamplePairs(t)
	routes := requirementRoutes(t)
	f := newAgentFixture(t, 0)
	session, err := f.jwt.GenerateSession(f.accountID, f.address, 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	for op, named := range map[string][]string{
		"listAgentLedger":        {"invalid_capability", "invalid_id", "missing"},
		"appendAgentLedgerEntry": {"automatic_without_undo", "actor_in_body", "invalid_id", "missing"},
		"updateAgentTrust":       {"send_mail_automatic", "invalid_id", "missing"},
	} {
		for _, name := range named {
			t.Run(op+"/"+name, func(t *testing.T) {
				pair := pairs[op][name]
				rr := f.do(routes[op].method, messageCollectionReadURI(routes[op].path, pair), "Bearer "+session.AccessToken, string(pair.Request.Body))
				assertRequirementResponse(t, pair, rr)
				if rr.Code == http.StatusCreated {
					t.Fatal("the live run wrote to an append-only ledger")
				}
			})
		}
	}
}
