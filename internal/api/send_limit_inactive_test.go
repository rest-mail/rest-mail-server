package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/sendlimits"
)

// inactiveChain is the shared@ account chris@ manages, owned by boss@ and that
// by top@, each with its own address, all active until a test deactivates one.
type inactiveChain struct{ shared, boss, top uint }

func newInactiveChain(f *credentialFixture) inactiveChain {
	f.t.Helper()
	var chris models.Mailbox
	if err := f.db.First(&chris, f.mailboxID).Error; err != nil {
		f.t.Fatal(err)
	}
	c := inactiveChain{shared: f.seedMailbox(chris.DomainID, "shared"), boss: f.seedMailbox(chris.DomainID, "boss"), top: f.seedMailbox(chris.DomainID, "top")}
	for _, link := range [][2]uint{{c.shared, c.boss}, {c.boss, c.top}} {
		if err := f.db.Model(&models.Mailbox{}).Where("id = ?", link[0]).Update("owner_id", link[1]).Error; err != nil {
			f.t.Fatal(err)
		}
	}
	f.own(c.shared, "shared@example.test")
	return c
}

func deactivate(f *credentialFixture, id uint) {
	f.t.Helper()
	if err := f.db.Model(&models.Mailbox{}).Where("id = ?", id).Update("active", false).Error; err != nil {
		f.t.Fatal(err)
	}
}

// count is how many rows of model the database holds.
func count(f *credentialFixture, model any) int64 {
	f.t.Helper()
	var n int64
	if err := f.db.Model(model).Count(&n).Error; err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestInactiveAccountSendLimitExamples(t *testing.T) {
	pairs := clientSecretExamplePairs(t)
	routes := requirementRoutes(t)

	t.Run("getAccountSendLimits/inactive_account", func(t *testing.T) {
		examplepairs.Proven(t, "getAccountSendLimits", "inactive_account")
		pair := pairs["getAccountSendLimits"]["inactive_account"]
		f := newSendLimitFixture(t)
		c := newInactiveChain(f)
		deactivate(f, c.shared)
		assertRequirementResponse(t, pair, sendSendLimitExample(f, routes["getAccountSendLimits"], withPathID(pair, c.shared), f.session))
	})

	for name, inactive := range map[string]func(inactiveChain) uint{
		"its owner":         func(c inactiveChain) uint { return c.boss },
		"its owner's owner": func(c inactiveChain) uint { return c.top },
	} {
		t.Run("getAccountSendLimits answers the same for "+name, func(t *testing.T) {
			pair := pairs["getAccountSendLimits"]["inactive_account"]
			f := newSendLimitFixture(t)
			c := newInactiveChain(f)
			deactivate(f, inactive(c))
			assertRequirementResponse(t, pair, sendSendLimitExample(f, routes["getAccountSendLimits"], withPathID(pair, c.shared), f.session))
		})
	}

	t.Run("getAccountSendLimits still answers an active chain", func(t *testing.T) {
		pair := pairs["getAccountSendLimits"]["inactive_account"]
		f := newSendLimitFixture(t)
		c := newInactiveChain(f)
		rr := sendSendLimitExample(f, routes["getAccountSendLimits"], withPathID(pair, c.shared), f.session)
		if rr.Code != http.StatusOK {
			t.Fatalf("got %d %s, want 200", rr.Code, rr.Body.String())
		}
	})

	t.Run("setAccountSendLimits/inactive_owner", func(t *testing.T) {
		examplepairs.Proven(t, "setAccountSendLimits", "inactive_owner")
		pair := pairs["setAccountSendLimits"]["inactive_owner"]
		f := newSendLimitFixture(t)
		c := newInactiveChain(f)
		deactivate(f, c.boss)
		assertRequirementResponse(t, pair, sendSendLimitExample(f, routes["setAccountSendLimits"], withPathID(pair, c.shared), twoFactorSession(f)))
		var n int64
		if err := f.db.Model(&models.SendLimitPolicy{}).Where("scope = ?", sendlimits.AccountScope(c.shared)).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("the refused limits were stored")
		}
	})
}

// A send from an inactive identity is the send route's documented refusal of a
// sender, with nothing queued, held or charged. It was a 503 saying to try again.
func TestSendFromInactiveAccountIsRefused(t *testing.T) {
	pairs := clientSecretExamplePairs(t)
	want := pairs["sendMessage"]["read_only_send_as"]
	for name, inactive := range map[string]func(inactiveChain) uint{
		"the sender":         func(c inactiveChain) uint { return c.shared },
		"the sender's owner": func(c inactiveChain) uint { return c.boss },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSendLimitFixture(t)
			c := newInactiveChain(f)
			deactivate(f, inactive(c))
			body := `{"from":"shared@example.test","to":["far@remote.test"],"subject":"Hello","body_text":"Body"}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", f.session)
			rr := httptest.NewRecorder()
			f.router.ServeHTTP(rr, req)
			assertRequirementResponse(t, want, rr)
			for model, n := range map[string]int64{"queued": outbound(f), "held": count(f, &models.ScheduledSend{}), "charged": count(f, &models.SendLimitCounter{})} {
				if n != 0 {
					t.Errorf("%s %d after the refusal", model, n)
				}
			}
		})
	}
}

// A hold whose sender has since gone inactive stays held, and the release says
// why it cannot go; a held message is never lost to the refusal.
func TestReleaseFromInactiveAccountLeavesTheHold(t *testing.T) {
	pairs := clientSecretExamplePairs(t)
	routes := requirementRoutes(t)
	want := pairs["sendMessage"]["read_only_send_as"]
	f := newSendLimitFixture(t)
	c := newInactiveChain(f)
	request, err := json.Marshal(map[string]any{"from": "shared@example.test", "to": []string{"far@remote.test"}, "subject": "Held", "body_text": "Body"})
	if err != nil {
		t.Fatal(err)
	}
	held := models.ScheduledSend{AccountID: f.accountID, From: "shared@example.test", To: models.JSONB(`["far@remote.test"]`), Subject: "Held",
		SendAt: time.Now().Add(time.Hour), Status: "held", LastError: sendlimits.AccountScope(f.accountID) + ": messages_per_hour", Request: models.JSONB(request)}
	if err := f.db.Create(&held).Error; err != nil {
		t.Fatal(err)
	}
	deactivate(f, c.shared)
	rr := sendSendLimitExample(f, routes["releaseHeldMessage"], withPathID(pairs["releaseHeldMessage"]["released"], held.ID), twoFactorSession(f))
	assertRequirementResponse(t, want, rr)
	var after models.ScheduledSend
	if err := f.db.First(&after, held.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != "held" || outbound(f) != 0 {
		t.Fatalf("the refused release left %q with %d queued", after.Status, outbound(f))
	}
}
