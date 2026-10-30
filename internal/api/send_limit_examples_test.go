package api

import (
	"encoding/json"
	"maps"
	"net/http/httptest"
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
}
