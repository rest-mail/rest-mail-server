package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/rest-mail/rest-mail-server/internal/agentledger"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

type agentFixture struct {
	*credentialFixture
	ledger, trust string
}

func newAgentFixture(t *testing.T, window time.Duration) *agentFixture {
	t.Helper()
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test", AgentUndoWindow: window})
	return &agentFixture{f, fmt.Sprintf("/api/v1/accounts/%d/agent/ledger", f.accountID), fmt.Sprintf("/api/v1/accounts/%d/agent/trust", f.accountID)}
}

// setTrust changes the levels through the route, and fails if it is refused.
func (f *agentFixture) setTrust(body string) {
	f.t.Helper()
	if rr := f.do(http.MethodPatch, f.trust, f.session, body); rr.Code != http.StatusOK {
		f.t.Fatalf("set trust %s: %d %s", body, rr.Code, rr.Body.String())
	}
}

type agentEntryAnswer struct {
	Data handlers.AgentLedgerEntry `json:"data"`
}

// write posts an entry, and fails unless it is written.
func (f *agentFixture) write(body string) handlers.AgentLedgerEntry {
	f.t.Helper()
	rr := f.do(http.MethodPost, f.ledger, f.session, body)
	if rr.Code != http.StatusCreated {
		f.t.Fatalf("write %s: %d %s", body, rr.Code, rr.Body.String())
	}
	var answer agentEntryAnswer
	decodeInto(f.t, rr, &answer)
	return answer.Data
}

func (f *agentFixture) levels() map[string]string {
	f.t.Helper()
	rr := f.do(http.MethodPatch, f.trust, f.session, `{}`)
	if rr.Code != http.StatusOK {
		f.t.Fatalf("read trust: %d %s", rr.Code, rr.Body.String())
	}
	var answer struct {
		Data handlers.AgentTrust `json:"data"`
	}
	decodeInto(f.t, rr, &answer)
	out := map[string]string{}
	for _, capability := range answer.Data.Capabilities {
		out[capability.Capability] = capability.Level
	}
	return out
}

func (f *agentFixture) ledgerRows() []models.AgentLedgerEntry {
	f.t.Helper()
	var rows []models.AgentLedgerEntry
	if err := f.db.Where("mailbox_id = ?", f.accountID).Order("seq").Find(&rows).Error; err != nil {
		f.t.Fatal(err)
	}
	return rows
}

// wantRefusal checks the status and the error code of a refusal.
func wantRefusal(t *testing.T, name string, got interface {
	Result() *http.Response
}, status int, code string) {
	t.Helper()
	response := got.Result()
	defer response.Body.Close()
	var answer struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		t.Fatalf("%s: decode: %v", name, err)
	}
	if response.StatusCode != status || answer.Error.Code != code {
		t.Errorf("%s: got %d %s, want %d %s", name, response.StatusCode, answer.Error.Code, status, code)
	}
}

const (
	suggestSend  = `{"capability":"send_mail","level":"suggest","action":"reply","target":{"message_id":"7"}}`
	approveSend  = `{"capability":"send_mail","level":"approve","action":"reply","target":{"message_id":"7"}}`
	approveFile  = `{"capability":"file_newsletters","level":"approve","action":"archive","target":{"message_id":"8"},"undo":{"folder":"INBOX","message_id":"8"}}`
	autoFile     = `{"capability":"file_newsletters","level":"auto","action":"archive","target":{"message_id":"9"},"undo":{"folder":"INBOX","message_id":"9"}}`
	autoFileBare = `{"capability":"file_newsletters","level":"auto","action":"archive","target":{"message_id":"9"}}`
)

func TestAgentTrust_EveryCapabilityStartsAtSuggestAndEachMovesAlone(t *testing.T) {
	f := newAgentFixture(t, time.Hour)
	levels := f.levels()
	if levels["send_mail"] != "suggest" || levels["file_newsletters"] != "suggest" || len(levels) != 2 {
		t.Fatalf("levels at the start: %v", levels)
	}
	wantRefusal(t, "approve before it is allowed", f.do(http.MethodPost, f.ledger, f.session, approveSend), 403, "trust_level_too_low")
	wantRefusal(t, "auto before it is allowed", f.do(http.MethodPost, f.ledger, f.session, autoFile), 403, "trust_level_too_low")

	f.setTrust(`{"file_newsletters":"auto"}`)
	if levels := f.levels(); levels["send_mail"] != "suggest" || levels["file_newsletters"] != "auto" {
		t.Fatalf("one capability moved the other: %v", levels)
	}
	f.write(autoFile)
	wantRefusal(t, "another capability's level", f.do(http.MethodPost, f.ledger, f.session, approveSend), 403, "trust_level_too_low")
	f.write(suggestSend)

	f.setTrust(`{"file_newsletters":"approve","send_mail":"approve"}`)
	f.write(approveFile)
	f.write(approveSend)
	wantRefusal(t, "auto after it was lowered", f.do(http.MethodPost, f.ledger, f.session, autoFile), 403, "trust_level_too_low")
}

func TestAgentTrust_WhatCannotBeUndoneCannotBeAutomatic(t *testing.T) {
	t.Run("a capability that cannot be undone", func(t *testing.T) {
		f := newAgentFixture(t, time.Hour)
		wantRefusal(t, "set", f.do(http.MethodPatch, f.trust, f.session, `{"send_mail":"auto"}`), 422, "validation_failed")
		if levels := f.levels(); levels["send_mail"] != "suggest" {
			t.Errorf("levels: %v", levels)
		}
		wantRefusal(t, "all or none", f.do(http.MethodPatch, f.trust, f.session, `{"send_mail":"auto","file_newsletters":"auto"}`), 422, "validation_failed")
		if levels := f.levels(); levels["file_newsletters"] != "suggest" {
			t.Errorf("a refused change moved another capability: %v", levels)
		}
		// Written straight to the table: the entry is held to the rule itself.
		if err := f.db.Create(&models.AgentTrust{MailboxID: f.accountID, Capability: "send_mail", Level: "auto", UpdatedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
		wantRefusal(t, "write", f.do(http.MethodPost, f.ledger, f.session, `{"capability":"send_mail","level":"auto","action":"reply"}`), 422, "validation_failed")
		if entry := f.write(`{"capability":"send_mail","level":"approve","action":"reply","undo":{"a":"b"}}`); entry.UndoUntil != nil {
			t.Errorf("a capability that cannot be undone got a window: %v", entry.UndoUntil)
		}
	})
	t.Run("an automatic action that does not say what reverses it", func(t *testing.T) {
		f := newAgentFixture(t, time.Hour)
		f.setTrust(`{"file_newsletters":"auto"}`)
		wantRefusal(t, "write", f.do(http.MethodPost, f.ledger, f.session, autoFileBare), 422, "validation_failed")
		if rows := f.ledgerRows(); len(rows) != 0 {
			t.Errorf("a refused entry was written: %+v", rows)
		}
	})
	t.Run("no undo window configured", func(t *testing.T) {
		f := newAgentFixture(t, 0)
		wantRefusal(t, "set", f.do(http.MethodPatch, f.trust, f.session, `{"file_newsletters":"auto"}`), 422, "validation_failed")
		f.setTrust(`{"file_newsletters":"approve"}`)
		if entry := f.write(approveFile); entry.UndoUntil != nil {
			t.Errorf("an entry got a window from none: %v", entry.UndoUntil)
		}
		// A level set while a window was configured, which has since gone.
		f.db.Exec("UPDATE agent_trust SET level = 'auto' WHERE mailbox_id = ?", f.accountID)
		wantRefusal(t, "write", f.do(http.MethodPost, f.ledger, f.session, autoFile), 422, "validation_failed")
	})
}

func TestAgentLedger_OrderedAttributedAndChained(t *testing.T) {
	f := newAgentFixture(t, time.Hour)
	stranger := f.seedMailbox(f.domainID(), "stranger")
	strangerSession := f.sessionFor(stranger, "stranger@example.test")

	empty := f.do(http.MethodGet, f.ledger, f.session, "")
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"data":[]`) {
		t.Fatalf("an empty ledger: %d %s", empty.Code, empty.Body.String())
	}
	first, second := f.write(suggestSend), f.write(suggestSend)
	other := f.do(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/agent/ledger", stranger), strangerSession, suggestSend)
	if other.Code != http.StatusCreated {
		t.Fatalf("another account's entry: %d %s", other.Code, other.Body.String())
	}
	var otherEntry agentEntryAnswer
	decodeInto(t, other, &otherEntry)
	if first.Seq != 1 || second.Seq != 2 || otherEntry.Data.Seq != 1 {
		t.Errorf("each account counts its own: %d, %d, %d", first.Seq, second.Seq, otherEntry.Data.Seq)
	}
	if first.ActorKind != "account" || first.ActorID == nil || *first.ActorID != f.accountID || first.ActorName != f.address || first.CredentialID != nil {
		t.Errorf("attribution: %+v", first)
	}
	if first.PrevHash != agentledger.GenesisHash || second.PrevHash != first.Hash || otherEntry.Data.PrevHash != agentledger.GenesisHash {
		t.Errorf("each account has its own chain: %+v %+v %+v", first, second, otherEntry.Data)
	}
	for _, row := range f.ledgerRows() {
		if got := agentledger.Hash(&row); got != row.Hash {
			t.Errorf("entry %d read back hashes to %s, not %s", row.Seq, got, row.Hash)
		}
	}

	wantRefusal(t, "the body names the actor", f.do(http.MethodPost, f.ledger, f.session,
		`{"capability":"send_mail","level":"suggest","action":"reply","actor_id":99}`), 422, "validation_failed")
	wantRefusal(t, "neither kind of entry", f.do(http.MethodPost, f.ledger, f.session, `{"action":"reply"}`), 422, "validation_failed")
	wantRefusal(t, "an unknown capability", f.do(http.MethodPost, f.ledger, f.session, `{"capability":"delete_everything","level":"suggest","action":"x"}`), 422, "validation_failed")
	wantRefusal(t, "NUL in a target", f.do(http.MethodPost, f.ledger, f.session, `{"capability":"send_mail","level":"suggest","action":"x","target":{"a":"\u0000"}}`), 422, "validation_failed")

	f.write(suggestSend)
	f.setTrust(`{"file_newsletters":"approve"}`)
	f.write(approveFile)
	type page struct {
		Data       []handlers.AgentLedgerEntry `json:"data"`
		Pagination struct {
			HasMore bool   `json:"has_more"`
			Cursor  string `json:"cursor"`
		} `json:"pagination"`
	}
	var one page
	rr := f.do(http.MethodGet, f.ledger+"?limit=2", f.session, "")
	decodeInto(t, rr, &one)
	if len(one.Data) != 2 || one.Data[0].Seq != 4 || one.Data[1].Seq != 3 || !one.Pagination.HasMore || one.Pagination.Cursor != "3" {
		t.Fatalf("newest first, a page: %s", rr.Body.String())
	}
	var two page
	decodeInto(t, f.do(http.MethodGet, f.ledger+"?limit=2&before_seq="+one.Pagination.Cursor, f.session, ""), &two)
	if len(two.Data) != 2 || two.Data[0].Seq != 2 || two.Data[1].Seq != 1 || two.Pagination.HasMore {
		t.Errorf("the next page: %+v", two)
	}
	var filed page
	decodeInto(t, f.do(http.MethodGet, f.ledger+"?capability=file_newsletters", f.session, ""), &filed)
	if len(filed.Data) != 1 || filed.Data[0].Seq != 4 || filed.Data[0].Undo["folder"] != "INBOX" {
		t.Errorf("filtered: %+v", filed)
	}
	wantRefusal(t, "an unknown capability filter", f.do(http.MethodGet, f.ledger+"?capability=nope", f.session, ""), 422, "validation_failed")
}

func TestAgentLedger_NothingCanChangeAnEntry(t *testing.T) {
	f := newAgentFixture(t, time.Hour)
	f.write(suggestSend)
	for _, statement := range []string{
		"UPDATE agent_ledger SET action = 'changed'",
		"DELETE FROM agent_ledger",
		"TRUNCATE agent_ledger",
	} {
		err := f.db.Exec(statement).Error
		if err == nil || !strings.Contains(err.Error(), "agent_ledger is append-only") {
			t.Errorf("%s: %v", statement, err)
		}
	}
}

func TestAgentLedger_OutlivesItsAccountAndTrustDoesNot(t *testing.T) {
	f := newAgentFixture(t, time.Hour)
	f.write(suggestSend)
	f.setTrust(`{"file_newsletters":"approve"}`)
	f.db.Exec("DELETE FROM grants WHERE holder_id = ?", f.accountID)
	f.db.Exec("DELETE FROM credentials WHERE account_id = ?", f.accountID)
	if err := f.db.Exec("DELETE FROM mailboxes WHERE id = ?", f.accountID).Error; err != nil {
		t.Fatal(err)
	}
	var trust, entries int64
	f.db.Model(&models.AgentTrust{}).Where("mailbox_id = ?", f.accountID).Count(&trust)
	f.db.Model(&models.AgentLedgerEntry{}).Where("mailbox_id = ?", f.accountID).Count(&entries)
	if trust != 0 || entries != 1 {
		t.Errorf("after the account went: %d trust rows, %d entries", trust, entries)
	}
}

func TestAgentLedger_Undo(t *testing.T) {
	f := newAgentFixture(t, time.Hour)
	f.setTrust(`{"file_newsletters":"auto","send_mail":"approve"}`)
	action := f.write(autoFile)
	if action.UndoUntil == nil || action.UndoUntil.Sub(action.OccurredAt) != time.Hour {
		t.Fatalf("the window: %+v", action)
	}
	undo := f.write(fmt.Sprintf(`{"undoes":%d}`, action.Seq))
	if undo.Undoes == nil || *undo.Undoes != action.Seq || undo.Action != "undo" || undo.Capability != action.Capability || undo.Target["message_id"] != "9" {
		t.Errorf("the undo: %+v", undo)
	}
	var listed struct {
		Data []handlers.AgentLedgerEntry `json:"data"`
	}
	decodeInto(t, f.do(http.MethodGet, f.ledger, f.session, ""), &listed)
	if len(listed.Data) != 2 || listed.Data[1].UndoneBy == nil || *listed.Data[1].UndoneBy != undo.Seq || listed.Data[0].UndoneBy != nil {
		t.Errorf("listed: %+v", listed.Data)
	}

	wantRefusal(t, "twice", f.do(http.MethodPost, f.ledger, f.session, fmt.Sprintf(`{"undoes":%d}`, action.Seq)), 409, "already_undone")
	wantRefusal(t, "an undo", f.do(http.MethodPost, f.ledger, f.session, fmt.Sprintf(`{"undoes":%d}`, undo.Seq)), 422, "validation_failed")
	wantRefusal(t, "no such entry", f.do(http.MethodPost, f.ledger, f.session, `{"undoes":4000}`), 422, "validation_failed")
	wantRefusal(t, "with anything else", f.do(http.MethodPost, f.ledger, f.session, `{"undoes":1,"action":"x"}`), 422, "validation_failed")
	suggestion := f.write(suggestSend)
	wantRefusal(t, "a suggestion", f.do(http.MethodPost, f.ledger, f.session, fmt.Sprintf(`{"undoes":%d}`, suggestion.Seq)), 422, "validation_failed")
	sent := f.write(approveSend)
	if sent.UndoUntil != nil {
		t.Errorf("an action nothing reverses got a window: %v", sent.UndoUntil)
	}
	wantRefusal(t, "an entry with no window", f.do(http.MethodPost, f.ledger, f.session, fmt.Sprintf(`{"undoes":%d}`, sent.Seq)), 409, "undo_window_closed")

	// Lowering the level does not take back an undo already open.
	second := f.write(autoFile)
	f.setTrust(`{"file_newsletters":"suggest"}`)
	f.write(fmt.Sprintf(`{"undoes":%d}`, second.Seq))
}

func TestAgentLedger_UndoWindowCloses(t *testing.T) {
	f := newAgentFixture(t, time.Millisecond)
	f.setTrust(`{"file_newsletters":"auto"}`)
	action := f.write(autoFile)
	time.Sleep(20 * time.Millisecond)
	wantRefusal(t, "after the window", f.do(http.MethodPost, f.ledger, f.session, fmt.Sprintf(`{"undoes":%d}`, action.Seq)), 409, "undo_window_closed")
}

func TestAgentLedger_AnActionNeedsTheGrantItsLevelDoesNotGive(t *testing.T) {
	f := newAgentFixture(t, time.Hour)
	f.setTrust(`{"send_mail":"approve"}`)
	// The server's ceiling holds every mail capability but sending.
	if err := grants.ConfigureServerOverride(f.db, "mail:read,mail:write,mailbox:manage,mailbox:session", true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = grants.ConfigureServerOverride(nil, "", false) })
	wantRefusal(t, "an approved send without mail:send", f.do(http.MethodPost, f.ledger, f.session, approveSend), 403, "forbidden")
	// A suggestion changes nothing, so it does not need the grant.
	f.write(suggestSend)
}

func TestAgentRoutes_Authorization(t *testing.T) {
	f := newAgentFixture(t, time.Hour)
	f.setTrust(`{"file_newsletters":"approve"}`)
	f.write(approveFile)
	domain := f.domainID()
	stranger := f.seedMailbox(domain, "stranger")
	owned := f.seedMailbox(domain, "owned")
	f.own(owned, "owned@example.test")
	const missing = uint(4_000_000)

	passwordOnly, err := f.jwt.GenerateSession(f.accountID, f.address, 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	passwordSession := "Bearer " + passwordOnly.AccessToken
	appPassword := basic(f.address, f.createAppPassword("laptop").Password)
	readKey := f.apiKey(`[{"capability":"mail:read","scope":"mailbox:chris@example.test"}]`)
	writeKey := f.apiKey(`[{"capability":"mail:write","scope":"mailbox:chris@example.test"}]`)
	manageKey := f.apiKey(`[{"capability":"mailbox:manage","scope":"mailbox:chris@example.test"},{"capability":"mail:read","scope":"mailbox:chris@example.test"},{"capability":"mail:write","scope":"mailbox:chris@example.test"},{"capability":"mail:send","scope":"mailbox:chris@example.test"}]`)
	ownedKey := f.apiKey(`[{"capability":"mail:read","scope":"mailbox:owned@example.test"}]`)

	ledgerOf := func(id uint) string { return fmt.Sprintf("/api/v1/accounts/%d/agent/ledger", id) }
	trustOf := func(id uint) string { return fmt.Sprintf("/api/v1/accounts/%d/agent/trust", id) }

	t.Run("nobody is told another account exists", func(t *testing.T) {
		for _, route := range []struct {
			name, method string
			path         func(uint) string
			body         string
		}{
			{"read", http.MethodGet, ledgerOf, ""},
			{"write", http.MethodPost, ledgerOf, suggestSend},
			{"trust", http.MethodPatch, trustOf, `{"send_mail":"approve"}`},
		} {
			someoneElses := f.do(route.method, route.path(stranger), f.session, route.body)
			absent := f.do(route.method, route.path(missing), f.session, route.body)
			if someoneElses.Code != 404 || absent.Code != 404 || someoneElses.Body.String() != absent.Body.String() {
				t.Errorf("%s: someone else's %d %s; absent %d %s", route.name, someoneElses.Code, someoneElses.Body.String(), absent.Code, absent.Body.String())
			}
		}
		var count int64
		f.db.Model(&models.AgentLedgerEntry{}).Where("mailbox_id = ?", stranger).Count(&count)
		if count != 0 {
			t.Error("an entry reached another account's ledger")
		}
	})

	t.Run("reading takes mail:read on the account", func(t *testing.T) {
		for name, as := range map[string]string{"own session": f.session, "app password": appPassword, "key holding mail:read": readKey} {
			if rr := f.do(http.MethodGet, f.ledger, as, ""); rr.Code != 200 {
				t.Errorf("%s: %d %s", name, rr.Code, rr.Body.String())
			}
		}
		wantRefusal(t, "a key without mail:read", f.do(http.MethodGet, f.ledger, writeKey, ""), 403, "forbidden")
		wantRefusal(t, "a key on another account", f.do(http.MethodGet, f.ledger, ownedKey, ""), 403, "forbidden")
		wantRefusal(t, "a key's own scope does not reach a stranger", f.do(http.MethodGet, ledgerOf(stranger), readKey, ""), 404, "not_found")
		if rr := f.do(http.MethodGet, ledgerOf(owned), f.session, ""); rr.Code != 200 {
			t.Errorf("an owner reads its account's ledger: %d %s", rr.Code, rr.Body.String())
		}
		if rr := f.do(http.MethodGet, f.ledger, "", ""); rr.Code != 401 {
			t.Errorf("no credential: %d", rr.Code)
		}
	})

	t.Run("writing takes the account's own session", func(t *testing.T) {
		before := len(f.ledgerRows())
		for name, as := range map[string]string{"app password": appPassword, "key holding everything": manageKey, "no credential": ""} {
			if rr := f.do(http.MethodPost, f.ledger, as, suggestSend); rr.Code != 401 {
				t.Errorf("%s: %d %s", name, rr.Code, rr.Body.String())
			}
		}
		wantRefusal(t, "an owner of the account", f.do(http.MethodPost, ledgerOf(owned), f.session, suggestSend), 403, "own_session_required")
		if got := len(f.ledgerRows()); got != before {
			t.Errorf("a refused writer wrote: %d entries, was %d", got, before)
		}
		f.write(suggestSend)
		if rr := f.do(http.MethodPost, f.ledger, passwordSession, suggestSend); rr.Code != 201 {
			t.Errorf("a session without a second factor: %d %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("changing a level takes mailbox:manage and a recent second factor", func(t *testing.T) {
		for name, as := range map[string]string{"app password": appPassword, "key holding everything": manageKey, "no credential": ""} {
			if rr := f.do(http.MethodPatch, f.trust, as, `{"send_mail":"approve"}`); rr.Code != 401 {
				t.Errorf("%s: %d %s", name, rr.Code, rr.Body.String())
			}
		}
		wantRefusal(t, "no second factor", f.do(http.MethodPatch, f.trust, passwordSession, `{"send_mail":"approve"}`), 403, "two_factor_required")
		wantRefusal(t, "an old second factor", f.do(http.MethodPatch, f.trust, f.staleSession(), `{"send_mail":"approve"}`), 403, "recent_two_factor_required")
		if levels := f.levels(); levels["send_mail"] != "suggest" {
			t.Errorf("a refused change landed: %v", levels)
		}
		if rr := f.do(http.MethodPatch, trustOf(owned), f.session, `{"send_mail":"approve"}`); rr.Code != 200 {
			t.Errorf("an owner of the account: %d %s", rr.Code, rr.Body.String())
		}
		reader := f.seedMailbox(domain, "shared")
		f.grantRead("shared@example.test")
		if rr := f.do(http.MethodGet, ledgerOf(reader), f.session, ""); rr.Code != 200 {
			t.Errorf("a reader reads the ledger: %d %s", rr.Code, rr.Body.String())
		}
		wantRefusal(t, "a reader changing a level", f.do(http.MethodPatch, trustOf(reader), f.session, `{"send_mail":"approve"}`), 403, "forbidden")
	})
}

func TestAgentRoutes_AreAudited(t *testing.T) {
	f := newAgentFixture(t, time.Hour)
	f.setTrust(`{"file_newsletters":"approve"}`)
	changes := f.entries(handlers.ActionAgentTrustUpdate)
	if len(changes) != 1 || changes[0].Target["file_newsletters"] != "approve" || changes[0].Outcome != models.AuditOutcomeSuccess {
		t.Errorf("the change: %+v", changes)
	}
	f.write(suggestSend)
	if got := f.entries(handlers.ActionAgentLedgerAppend); len(got) != 0 {
		t.Errorf("a written entry is in its own ledger: %+v", got)
	}
	f.do(http.MethodPost, f.ledger, f.session, approveSend)
	refused := f.entries(handlers.ActionAgentLedgerAppend)
	if len(refused) != 1 || refused[0].Status != 403 || refused[0].ErrorCode != "trust_level_too_low" {
		t.Errorf("the refusal: %+v", refused)
	}
}

// grantRead gives chris@ a role on the account at address that reads its mail, and
// nothing else: mail:read, and the mailbox:session every mailbox role holds.
func (f *agentFixture) grantRead(address string) {
	f.t.Helper()
	role := models.Role{Name: "mail reader"}
	if err := f.db.Create(&role).Error; err != nil {
		f.t.Fatal(err)
	}
	for _, name := range []string{models.CapMailRead, models.CapMailboxSession} {
		var capability models.Capability
		if err := f.db.Where("name = ?", name).First(&capability).Error; err != nil {
			f.t.Fatal(err)
		}
		if err := f.db.Create(&models.RoleCapability{RoleID: role.ID, CapabilityID: capability.ID}).Error; err != nil {
			f.t.Fatal(err)
		}
	}
	if err := f.db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: f.mailboxID, RoleID: role.ID,
		Scope: "mailbox:" + address, Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f *agentFixture) domainID() uint {
	f.t.Helper()
	var domain models.Domain
	if err := f.db.Where("name = ?", "example.test").First(&domain).Error; err != nil {
		f.t.Fatal(err)
	}
	return domain.ID
}

func (f *agentFixture) sessionFor(id uint, address string) string {
	f.t.Helper()
	pair, err := f.jwt.GenerateSession(id, address, 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		f.t.Fatal(err)
	}
	return "Bearer " + pair.AccessToken
}

// staleSession is a session whose second factor is older than any fresh window.
func (f *agentFixture) staleSession() string {
	f.t.Helper()
	old := jwt.NewNumericDate(time.Now().Add(-time.Hour))
	pair, err := f.jwt.GenerateSession(f.accountID, f.address, 0, []string{auth.AMRPassword, auth.AMROTP}, false, auth.ContinueSession("stale-session"), auth.ContinueAuthentication(old))
	if err != nil {
		f.t.Fatal(err)
	}
	return "Bearer " + pair.AccessToken
}

// apiKey makes an API key with the permissions, and answers its Authorization header.
func (f *agentFixture) apiKey(permissions string) string {
	f.t.Helper()
	rr := f.do(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/api-keys", f.accountID), f.session,
		fmt.Sprintf(`{"name":"k","password":%q,"permissions":%s}`, f.password, permissions))
	if rr.Code != http.StatusCreated {
		f.t.Fatalf("make a key %s: %d %s", permissions, rr.Code, rr.Body.String())
	}
	var answer struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	decodeInto(f.t, rr, &answer)
	return "Bearer " + answer.Data.Token
}
