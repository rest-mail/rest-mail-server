package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestHeldExampleRequiresHeldState(t *testing.T) {
	useRealGrants(t)
	fixture := newCredentialFixtureWith(t, &config.Config{Environment: "test", TOTP2FAEnabled: true,
		MasterKey: accessMasterKey, SendLimits: &config.SendLimitConfig{}})
	person := &freshInstall{t: t, db: fixture.db, router: fixture.router}
	plain := person.signIn(fixture.address, fixture.password, "")
	var enrolled struct {
		Secret        string   `json:"secret"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	person.expect(person.do(http.MethodPost, "/api/v1/auth/2fa/enroll", plain.bearer(), ""), 200, "enroll", &enrolled)
	code, err := auth.GenerateTOTPCode(enrolled.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	person.expect(person.do(http.MethodPost, "/api/v1/auth/2fa/confirm", plain.bearer(), fmt.Sprintf(`{"totp_code":%q}`, code)), 204, "confirm", nil)
	var session freshSession
	person.expect(person.do(http.MethodPost, "/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":%q,"recovery_code":%q,"client":"native"}`,
		fixture.address, fixture.password, enrolled.RecoveryCodes[0])), 200, "MFA sign-in", &session)
	pair := publishedDescriptionPair(t, readPublishedDescriptionPairs(t), "releaseHeldMessage", "released")
	if requirementOf(pair) != "state=held_message" {
		t.Fatalf("released lost its precondition: %v", pair.Requires)
	}
	if err := fixture.db.Create(&models.QuotaUsage{MailboxID: fixture.mailboxID}).Error; err != nil {
		t.Fatal(err)
	}
	held := seedHeld(fixture)
	pair = withPathID(pair, held.ID)
	route := requirementRoutes(t)["releaseHeldMessage"]
	token := session.bearer()
	if err := fixture.db.Model(&held).Update("status", "pending").Error; err != nil {
		t.Fatal(err)
	}
	without := sendSendLimitExample(fixture, route, pair, token)
	if without.Code != http.StatusConflict || grantErrorCode(without) != "not_held" || outbound(fixture) != 0 {
		t.Fatalf("setup-removed control: %d %s, queued %d", without.Code, without.Body, outbound(fixture))
	}
	if without.Code == pair.Response.Status {
		t.Fatal("released example passed without held state")
	}
	t.Logf("setup-removed control rejected: %d %s", without.Code, without.Body)
	if err := fixture.db.Model(&held).Update("status", "held").Error; err != nil {
		t.Fatal(err)
	}
	response := sendSendLimitExample(fixture, route, pair, token)
	row := assertHeldExample(t, fixture, pair, response, "")
	if row.ID != held.ID || row.Status != "sent" || row.SentMessageID == nil || outbound(fixture) != 1 {
		t.Fatalf("held setup did not persist the claimed transition: %+v", row)
	}
	var sent models.Message
	if err := fixture.db.First(&sent, *row.SentMessageID).Error; err != nil {
		t.Fatal(err)
	}
	if sent.MailboxID != fixture.mailboxID || sent.Folder != "Sent" || sent.Subject != held.Subject || sent.BodyText != "Figures attached." {
		t.Fatalf("release did not persist the held content: %+v", sent)
	}
}

func TestQuotaExampleRequiresStoredUsage(t *testing.T) {
	fixture := newSendLimitFixture(t)
	pair := publishedDescriptionPair(t, readPublishedDescriptionPairs(t), "getQuota", "found")
	pair = withPathID(pair, fixture.mailboxID)
	route := requirementRoutes(t)["getQuota"]
	if err := fixture.db.Model(&models.Mailbox{}).Where("id = ?", fixture.mailboxID).Update("quota_bytes", 1024).Error; err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := fixture.db.Create(&models.Message{MailboxID: fixture.mailboxID, Folder: "INBOX", Subject: "quota setup"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	without := sendSendLimitExample(fixture, route, pair, fixture.session)
	if without.Code != http.StatusOK {
		t.Fatalf("setup-removed control: %d %s", without.Code, without.Body)
	}
	var expected, absent any
	if err := json.Unmarshal(pair.Response.Body, &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(without.Body.Bytes(), &absent); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(absent, expected) {
		t.Fatal("quota example passed without its stored usage")
	}
	t.Logf("setup-removed control rejected: %s", without.Body)
	if err := fixture.db.Model(&models.Mailbox{}).Where("id = ?", fixture.mailboxID).Update("quota_used_bytes", 256).Error; err != nil {
		t.Fatal(err)
	}
	assertRequirementResponse(t, pair, sendSendLimitExample(fixture, route, pair, fixture.session))
}
