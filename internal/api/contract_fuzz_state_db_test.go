package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// Three rows of the contract-fuzz board (#2051) are refusals the server was right to make,
// of requests the fuzz run's own configuration made stale. tests/contract/probes.py no longer
// makes them (test_probes.py holds that); these hold the server's side, through the production
// router on PostgreSQL, so the refusals the nightly saw stay what they are.

// fuzzProbeRawMessage is tests/contract/probes.py's RAW_MESSAGE, the message every pinned
// RESTMAIL probe carries.
var fuzzProbeRawMessage = base64.StdEncoding.EncodeToString([]byte("Message-ID: <contract-probe@example.test>\r\n" +
	"From: Original <old@example.test>\r\nSubject: imported\r\n\r\nhistory\r\n"))

// 9b7fd3ab5ec46f4b: the documented create answered 409 already_exists in the fuzz run, because a
// fuzz.pin held its address at the fixture recipient, which already has an account. The
// documented body creates the account; the same address again is the 409 the description names.
func TestFuzzFinding9b7fd3ab5ec46f4bOnlyAnExistingAddressConflicts(t *testing.T) {
	fixture := newGrantsFixture(t)
	owner := fixture.token(t, "owner")
	body := `{"address":"new-account@a.test","display_name":"New Account","password":"Qv7#mZ2!pL9k"}`
	created := doRequest(fixture.router, http.MethodPost, "/api/v1/accounts", owner, body)
	if created.Code != http.StatusCreated {
		t.Fatalf("the documented create: %d %s, want 201", created.Code, created.Body)
	}
	again := doRequest(fixture.router, http.MethodPost, "/api/v1/accounts", owner, body)
	if again.Code != http.StatusConflict || !strings.Contains(again.Body.String(), `"code":"already_exists"`) {
		t.Fatalf("the same address again: %d %s, want 409 already_exists", again.Code, again.Body)
	}
}

// 5972296353a77f68: every field of the nightly's RESTMAIL probe was pinned, and it was refused
// 422 undeliverable because the run's own updateDomain probes had taken restmail.test, the
// pinned recipient's domain, out of service. The same body is delivered while the domain is
// live, and refused once it is not, as a domain out of service receives nothing.
func TestFuzzFinding5972296353a77f68ADomainOutOfServiceReceivesNothing(t *testing.T) {
	useRealGrants(t)
	fixture := newGrantsFixture(t)
	owner := fixture.token(t, "owner")
	mail := retiredMail{test: t, router: fixture.router}
	domain := createLiveDomain(t, fixture, owner, "fuzz-recipient.test")
	recipient := "postmaster@fuzz-recipient.test"
	body, err := json.Marshal(map[string]any{"date": "Sat, 17 Oct 2026 12:00:00 +0000",
		"raw_message": fuzzProbeRawMessage, "recipients": []string{recipient}})
	if err != nil {
		t.Fatal(err)
	}
	if response := mail.send(http.MethodPost, "/restmail/messages", string(body)); response.Code != http.StatusCreated {
		t.Fatalf("the probe to a live domain: %d %s, want 201", response.Code, response.Body)
	}
	// What the nightly's updateDomain probe sent before the RESTMAIL probes.
	if response := doRequest(fixture.router, http.MethodPatch, fmt.Sprintf("/api/v1/admin/domains/%d", domain.ID), owner, `{"active":false}`); response.Code != http.StatusOK {
		t.Fatalf("take the domain out of service: %d %s", response.Code, response.Body)
	}
	response := mail.send(http.MethodPost, "/restmail/messages", string(body))
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"undeliverable"`) {
		t.Fatalf("the probe once its domain is out of service: %d %s, want 422 undeliverable", response.Code, response.Body)
	}
}

// e3900209d733750b: {"is_starred":false,"unchanged_since":1} was refused 409 message_modified,
// rightly, because the message had changed since mod-sequence 1. The fuzz run now holds a valid
// probe's unchanged_since at 2^63-1 (tests/contract/probes.py UNCHANGED_SINCE), which no stored
// mod_seq passes: the conditional write is made.
func TestFuzzFindingE3900209d733750bStaleModSequence(t *testing.T) {
	useRealGrants(t)
	f := newCredentialFixture(t)
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	message := models.Message{ID: 1, MailboxID: f.mailboxID, Folder: "INBOX", MsgID: "<message@example.net>",
		Sender: "sender@example.net", SenderName: "Sender", RecipientsTo: models.JSONB(`["recipient@example.com"]`),
		RecipientsCc: models.JSONB(`[]`), Subject: "Subject", SizeBytes: 80, IsStarred: true,
		ReceivedAt: stamp, CreatedAt: stamp, UpdatedAt: stamp}
	if err := f.db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	// Another client flags it, so its mod_seq passes 1.
	if err := f.db.Model(&models.Message{}).Where("id = ?", 1).Update("is_flagged", true).Error; err != nil {
		t.Fatal(err)
	}
	var before models.Message
	if err := f.db.First(&before, 1).Error; err != nil {
		t.Fatal(err)
	}
	stale := f.do(http.MethodPatch, "/api/v1/messages/1", f.session, `{"is_starred":false,"unchanged_since":1}`)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"code":"message_modified"`) {
		t.Fatalf("the nightly's body: %d %s, want 409 message_modified", stale.Code, stale.Body)
	}
	var after models.Message
	if err := f.db.First(&after, 1).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a stale conditional write changed the message: before=%+v after=%+v", before, after)
	}
	held := f.do(http.MethodPatch, "/api/v1/messages/1", f.session, `{"is_starred":false,"unchanged_since":9223372036854775807}`)
	if held.Code != http.StatusOK {
		t.Fatalf("the held mod-sequence: %d %s, want 200", held.Code, held.Body)
	}
	if err := f.db.First(&after, 1).Error; err != nil {
		t.Fatal(err)
	}
	if after.IsStarred || after.ModSeq <= before.ModSeq {
		t.Fatalf("the held conditional write: is_starred %t, mod_seq %d after %d; want false and a higher mod_seq", after.IsStarred, after.ModSeq, before.ModSeq)
	}
}
