package api

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/examplepairs"
	"github.com/restmail/restmail/internal/sendlimits"
)

// The send limit pairs that need a two-factor session run the real router, with
// send limits configured, on a scratch database holding exactly the state each
// pair names, and check what the request changed as well as the answer. They
// skip, and prove nothing, when no database is reachable.

// newSendLimitFixture is the credential fixture with persistent send limits on;
// without them both operations answer 503 before reading anything.
func newSendLimitFixture(t *testing.T) *credentialFixture {
	t.Helper()
	return newCredentialFixtureWith(t, &config.Config{Environment: "test", SendLimits: &config.SendLimitConfig{}})
}

// withPathID is pair with its {id} replaced by id.
func withPathID(pair oapifly.ExamplePair, id uint) oapifly.ExamplePair {
	path := maps.Clone(pair.Request.Path)
	path["id"] = float64(id)
	pair.Request.Path = path
	return pair
}

// sendSendLimitExample sends the pair's request as it is, with authorization.
func sendSendLimitExample(f *credentialFixture, route requirementRoute, pair oapifly.ExamplePair, authorization string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := requirementRequest(route, pair)
	req.Header.Set("Authorization", authorization)
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

// spendSendLimit caps scope at one message an hour and spends it, this hour and
// the next, so a request crossing the hour still finds the budget spent.
func spendSendLimit(f *credentialFixture, scope string) {
	f.t.Helper()
	if err := f.db.Create(&models.SendLimitPolicy{Scope: scope, SendLimitValues: models.SendLimitValues{MessagesPerHour: 1}}).Error; err != nil {
		f.t.Fatalf("cap %s: %v", scope, err)
	}
	hour := sendlimits.Bucket(time.Now(), time.Hour)
	for _, start := range []time.Time{hour, hour.Add(time.Hour)} {
		if err := f.db.Create(&models.SendLimitCounter{Scope: scope, WindowSeconds: int(time.Hour / time.Second), Start: start, Messages: 1, Recipients: 1}).Error; err != nil {
			f.t.Fatalf("spend %s: %v", scope, err)
		}
	}
}

// seedHeld stores chris@'s message held by its account budget, as a send over
// the limit leaves it.
func seedHeld(f *credentialFixture) models.ScheduledSend {
	f.t.Helper()
	held := models.ScheduledSend{AccountID: f.accountID, From: f.address, To: models.JSONB(`["bob@example.org"]`), Subject: "Quarterly report",
		SendAt: time.Now().Add(time.Hour), Status: "held", LastError: sendlimits.AccountScope(f.accountID) + ": messages_per_hour",
		Request: models.JSONB(`{"from":"` + f.address + `","to":["bob@example.org"],"subject":"Quarterly report","body_text":"Figures attached."}`)}
	if err := f.db.Create(&held).Error; err != nil {
		f.t.Fatalf("seed a held send: %v", err)
	}
	return held
}

// assertHeldExample checks the answer is the stored row, then compares it with
// the pair after putting the pair's values in place of what the database
// allocated: the row and sent message ids, the account, the times, and the
// scope lastError names. It returns the stored row.
func assertHeldExample(t *testing.T, f *credentialFixture, pair oapifly.ExamplePair, rr *httptest.ResponseRecorder, lastError string) models.ScheduledSend {
	t.Helper()
	if rr.Code != pair.Response.Status {
		t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
	}
	var got, want map[string]map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
		t.Fatal(err)
	}
	data := got["data"]
	id, _ := data["id"].(float64)
	var row models.ScheduledSend
	if err := f.db.First(&row, uint(id)).Error; err != nil {
		t.Fatalf("the answer names no stored message: %v", err)
	}
	if data["account_id"] != float64(f.accountID) || row.AccountID != f.accountID {
		t.Fatalf("account %v, stored %d, want %d", data["account_id"], row.AccountID, f.accountID)
	}
	if row.Status != data["status"] || row.LastError != lastError || (lastError != "" && data["last_error"] != lastError) {
		t.Fatalf("answered %v %v, stored %q %q, want last error %q", data["status"], data["last_error"], row.Status, row.LastError, lastError)
	}
	if row.SentMessageID != nil {
		if data["sent_message_id"] != float64(*row.SentMessageID) {
			t.Fatalf("sent message %v, stored %d", data["sent_message_id"], *row.SentMessageID)
		}
		data["sent_message_id"] = want["data"]["sent_message_id"]
	}
	for key, stored := range map[string]time.Time{"scheduled_at": row.SendAt, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt} {
		at, err := time.Parse(time.RFC3339Nano, fmt.Sprint(data[key]))
		if err != nil || !at.Equal(stored) {
			t.Fatalf("%s is %v, stored %v", key, data[key], stored)
		}
		data[key] = want["data"][key]
	}
	data["id"], data["account_id"] = want["data"]["id"], want["data"]["account_id"]
	if lastError != "" {
		data["last_error"] = want["data"]["last_error"]
	}
	rewriteBody(t, rr, got)
	assertRequirementResponse(t, pair, rr)
	return row
}

// outbound is how many deliveries are queued.
func outbound(f *credentialFixture) int64 {
	f.t.Helper()
	var n int64
	if err := f.db.Model(&models.OutboundQueue{}).Count(&n).Error; err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestSendLimitTwoFactorExamples(t *testing.T) {
	pairs := clientSecretExamplePairs(t)
	routes := requirementRoutes(t)

	t.Run("setAccountSendLimits/updated", func(t *testing.T) {
		examplepairs.Proven(t, "setAccountSendLimits", "updated")
		pair := pairs["setAccountSendLimits"]["updated"]
		f := newSendLimitFixture(t)
		rr := sendSendLimitExample(f, routes["setAccountSendLimits"], withPathID(pair, f.accountID), twoFactorSession(f)) // two_factor_session, on chris@'s own account
		assertRequirementResponse(t, pair, rr)
		var stored models.SendLimitPolicy
		if err := f.db.Where("scope = ?", sendlimits.AccountScope(f.accountID)).First(&stored).Error; err != nil {
			t.Fatalf("no account policy stored: %v", err)
		}
		want := models.SendLimitValues{MessagesPerHour: 50, MessagesPerDay: 500, RecipientsPerHour: 200, RecipientsPerDay: 2000}
		if stored.SendLimitValues != want {
			t.Fatalf("stored %+v, want %+v", stored.SendLimitValues, want)
		}
	})

	t.Run("setAccountSendLimits/missing", func(t *testing.T) {
		examplepairs.Proven(t, "setAccountSendLimits", "missing")
		pair := pairs["setAccountSendLimits"]["missing"]
		f := newSendLimitFixture(t)
		rr := sendSendLimitExample(f, routes["setAccountSendLimits"], pair, twoFactorSession(f))
		assertRequirementResponse(t, pair, rr)
		var n int64
		if err := f.db.Model(&models.SendLimitPolicy{}).Where("scope LIKE ?", "account:%").Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("refused change stored %d account policies", n)
		}
	})

	t.Run("setAccountSendLimits/owner_limit_exceeded", func(t *testing.T) {
		examplepairs.Proven(t, "setAccountSendLimits", "owner_limit_exceeded")
		pair := pairs["setAccountSendLimits"]["owner_limit_exceeded"]
		f := newSendLimitFixture(t)
		ownerLimits := models.SendLimitValues{MessagesPerHour: 100, MessagesPerDay: 1000, RecipientsPerHour: 1000, RecipientsPerDay: 10000}
		if err := f.db.Create(&models.SendLimitPolicy{Scope: sendlimits.AccountScope(f.mailboxID), SendLimitValues: ownerLimits}).Error; err != nil {
			t.Fatalf("cap chris@: %v", err)
		}
		var chris models.Mailbox
		if err := f.db.First(&chris, f.mailboxID).Error; err != nil {
			t.Fatal(err)
		}
		team := f.seedMailbox(chris.DomainID, "team")
		if err := f.db.Model(&models.Mailbox{}).Where("id = ?", team).Update("owner_id", f.mailboxID).Error; err != nil {
			t.Fatalf("make chris@ team@'s owner: %v", err)
		}
		f.own(team, "team@example.test")
		rr := sendSendLimitExample(f, routes["setAccountSendLimits"], withPathID(pair, team), twoFactorSession(f)) // two_factor_session, above chris@'s own limits
		assertRequirementResponse(t, pair, rr)
		var n int64
		if err := f.db.Model(&models.SendLimitPolicy{}).Where("scope = ?", sendlimits.AccountScope(team)).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("the refused limits were stored for team@")
		}
	})

	t.Run("setAccountSendLimits/malformed_json", func(t *testing.T) {
		examplepairs.Proven(t, "setAccountSendLimits", "malformed_json")
		pair := pairs["setAccountSendLimits"]["malformed_json"]
		var raw string
		if err := json.Unmarshal(pair.Request.Body, &raw); err != nil {
			t.Fatal(err)
		}
		pair.Request.Body = []byte(raw) // body=malformed: the bytes themselves, not a JSON string
		f := newSendLimitFixture(t)
		rr := sendSendLimitExample(f, routes["setAccountSendLimits"], withPathID(pair, f.accountID), f.session)
		assertRequirementResponse(t, pair, rr)
	})

	t.Run("releaseHeldMessage/missing", func(t *testing.T) {
		examplepairs.Proven(t, "releaseHeldMessage", "missing")
		pair := pairs["releaseHeldMessage"]["missing"]
		f := newSendLimitFixture(t)
		rr := sendSendLimitExample(f, routes["releaseHeldMessage"], pair, twoFactorSession(f))
		assertRequirementResponse(t, pair, rr)
	})

	t.Run("releaseHeldMessage/not_held", func(t *testing.T) {
		examplepairs.Proven(t, "releaseHeldMessage", "not_held")
		pair := pairs["releaseHeldMessage"]["not_held"]
		f := newSendLimitFixture(t)
		scheduled := models.ScheduledSend{AccountID: f.mailboxID, From: f.address, To: models.JSONB(`["bob@example.org"]`),
			Subject: "Later", SendAt: time.Now().Add(time.Hour), Status: "pending", Request: models.JSONB(`{}`)}
		if err := f.db.Create(&scheduled).Error; err != nil {
			t.Fatalf("seed a scheduled send: %v", err)
		}
		rr := sendSendLimitExample(f, routes["releaseHeldMessage"], withPathID(pair, scheduled.ID), twoFactorSession(f))
		assertRequirementResponse(t, pair, rr)
		var after models.ScheduledSend
		if err := f.db.First(&after, scheduled.ID).Error; err != nil {
			t.Fatal(err)
		}
		if after.Status != "pending" {
			t.Fatalf("refused release left the send %q, want pending", after.Status)
		}
	})

	t.Run("releaseHeldMessage/released", func(t *testing.T) {
		examplepairs.Proven(t, "releaseHeldMessage", "released")
		pair := pairs["releaseHeldMessage"]["released"]
		f := newSendLimitFixture(t)
		spendSendLimit(f, sendlimits.AccountScope(f.accountID)) // a release bypasses account budgets
		held := seedHeld(f)
		rr := sendSendLimitExample(f, routes["releaseHeldMessage"], withPathID(pair, held.ID), twoFactorSession(f)) // held_message, two-factor session
		row := assertHeldExample(t, f, pair, rr, "")
		if row.SentMessageID == nil || outbound(f) != 1 {
			t.Fatalf("released message not sent: sent message %v, %d queued", row.SentMessageID, outbound(f))
		}
	})

	t.Run("releaseHeldMessage/still_held", func(t *testing.T) {
		examplepairs.Proven(t, "releaseHeldMessage", "still_held")
		pair := pairs["releaseHeldMessage"]["still_held"]
		f := newSendLimitFixture(t)
		var chris models.Mailbox
		if err := f.db.First(&chris, f.mailboxID).Error; err != nil {
			t.Fatal(err)
		}
		domain := "domain:" + strconv.FormatUint(uint64(chris.DomainID), 10)
		spendSendLimit(f, domain)
		held := seedHeld(f)
		rr := sendSendLimitExample(f, routes["releaseHeldMessage"], withPathID(pair, held.ID), twoFactorSession(f)) // held_by_domain_limit, two-factor session
		row := assertHeldExample(t, f, pair, rr, domain+": messages_per_hour")
		if row.ID != held.ID || outbound(f) != 0 {
			t.Fatalf("release past the domain budget: row %d, want %d; %d queued", row.ID, held.ID, outbound(f))
		}
	})

	t.Run("sendDraft/held", func(t *testing.T) {
		examplepairs.Proven(t, "sendDraft", "held")
		pair := pairs["sendDraft"]["held"]
		f := newSendLimitFixture(t)
		spendSendLimit(f, sendlimits.AccountScope(f.accountID))
		draft := models.Message{MailboxID: f.mailboxID, Folder: "Drafts", IsDraft: true, IsRead: true, Sender: f.address,
			RecipientsTo: models.JSONB(`["bob@example.org"]`), RecipientsCc: models.JSONB(`[]`),
			Subject: "Quarterly report", BodyText: "Figures attached.", SizeBytes: len("Quarterly report") + len("Figures attached.")}
		if err := f.db.Create(&draft).Error; err != nil {
			t.Fatal(err)
		}
		rr := sendSendLimitExample(f, routes["sendDraft"], withPathID(pair, draft.ID), f.session) // spent_send_limit
		assertHeldExample(t, f, pair, rr, sendlimits.AccountScope(f.accountID)+": messages_per_hour")
		if outbound(f) != 0 {
			t.Fatal("a held draft queued mail")
		}
	})

	t.Run("forwardMessage/held", func(t *testing.T) {
		examplepairs.Proven(t, "forwardMessage", "held")
		pair := pairs["forwardMessage"]["held"]
		f := newSendLimitFixture(t)
		spendSendLimit(f, sendlimits.AccountScope(f.accountID))
		original := models.Message{MailboxID: f.mailboxID, Folder: "INBOX", Sender: "alice@example.org",
			RecipientsTo: models.JSONB(`["` + f.address + `"]`), RecipientsCc: models.JSONB(`[]`),
			Subject: "Quarterly report", BodyText: "Figures attached.", SizeBytes: len("Quarterly report") + len("Figures attached.")}
		if err := f.db.Create(&original).Error; err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal(pair.Request.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body["from"] != f.address {
			t.Fatalf("the pair forwards from %v, not chris@", body["from"])
		}
		rr := sendSendLimitExample(f, routes["forwardMessage"], withPathID(pair, original.ID), f.session) // spent_send_limit
		assertHeldExample(t, f, pair, rr, sendlimits.AccountScope(f.accountID)+": messages_per_hour")
		if outbound(f) != 0 {
			t.Fatal("a held forward queued mail")
		}
	})
}
