package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// TestHeldReleasePersistsThroughRouter uses real sign-in, enrollment, grants,
// reset, account creation and budget spending. No held row or MFA token is forged.
func TestHeldReleasePersistsThroughRouter(t *testing.T) {
	useRealGrants(t)
	db := createMigratedScratchDB(t, "held_release_proof")
	seedAdmins(t, db, []fixtureAdmin{{name: "proof", roles: []string{"superadmin"}}})
	cfg := &config.Config{Environment: "test", TOTP2FAEnabled: true, MasterKey: accessMasterKey, SendLimits: &config.SendLimitConfig{}}
	setTestMessageStorage(t, cfg)
	routers := NewRouters(db, auth.NewJWTService(routerTestSecret, 15*time.Minute, 24*time.Hour), cfg, nil)
	t.Cleanup(routers.Close)
	f := &freshInstall{t: t, db: db, router: routers.Public}
	plain := f.signIn("proof@access.test", accessPassword, "")
	var enrolled struct {
		Secret        string   `json:"secret"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	f.expect(f.do("POST", "/api/v1/auth/2fa/enroll", plain.bearer(), ""), 200, "enroll", &enrolled)
	code, err := auth.GenerateTOTPCode(enrolled.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.expect(f.do("POST", "/api/v1/auth/2fa/confirm", plain.bearer(), fmt.Sprintf(`{"totp_code":%q}`, code)), 204, "confirm", nil)
	var admin freshSession
	f.expect(f.do("POST", "/api/v1/auth/login", "", fmt.Sprintf(`{"email":"proof@access.test","password":%q,"recovery_code":%q,"client":"native"}`, accessPassword, enrolled.RecoveryCodes[0])), 200, "MFA sign-in", &admin)
	f.expect(f.do("POST", "/api/v1/admin/test/reset", admin.bearer(), ""), 200, "reset", nil)
	f.expect(f.do("POST", "/api/v1/admin/test/seed", admin.bearer(), ""), 200, "seed", nil)
	var sender freshAccount
	f.expect(f.do("POST", "/api/v1/accounts", admin.bearer(), fmt.Sprintf(`{"address":"held-proof@restmail.test","password":%q}`, credentialTestPassword)), 201, "create sender", &sender)
	senderSession := f.signIn(sender.Address, credentialTestPassword, "")
	f.expect(f.do("PUT", fmt.Sprintf("/api/v1/accounts/%d/send-limits", sender.ID), admin.bearer(), `{"messages_per_hour":1,"messages_per_day":100,"recipients_per_hour":100,"recipients_per_day":100}`), 200, "cap sender", nil)
	body := `{"from":"held-proof@restmail.test","to":["held-proof@nowhere.invalid"],"subject":"Held transition proof","body_text":"retained bytes"}`
	f.expect(f.do("POST", "/api/v1/messages/send", senderSession.bearer(), body), 201, "spend sender budget", nil)
	var held struct {
		ID, AccountID     uint
		Status, LastError string
	}
	// Decode wire names explicitly rather than relying on Go field matching.
	var heldData map[string]json.RawMessage
	f.expect(f.do("POST", "/api/v1/messages/send", senderSession.bearer(), body), 202, "retain actual hold", &heldData)
	_ = json.Unmarshal(heldData["id"], &held.ID)
	_ = json.Unmarshal(heldData["account_id"], &held.AccountID)
	_ = json.Unmarshal(heldData["status"], &held.Status)
	_ = json.Unmarshal(heldData["last_error"], &held.LastError)
	if held.ID == 0 || held.AccountID != sender.ID || held.Status != "held" || held.LastError != fmt.Sprintf("account:%d: messages_per_hour", sender.ID) {
		t.Fatalf("not an actual account-budget hold: %+v", held)
	}
	read := func() models.ScheduledSend {
		t.Helper()
		var row models.ScheduledSend
		if err := db.First(&row, held.ID).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	before := read()
	if before.SentMessageID != nil || string(before.Request) == "{}" {
		t.Fatalf("invalid retained request: %+v", before)
	}
	path := fmt.Sprintf("/api/v1/messages/scheduled/%d/release", held.ID)
	for _, c := range []struct {
		name, path, token string
		status            int
	}{
		{"invalid", "/api/v1/messages/scheduled/0/release", admin.bearer(), 422},
		{"absent", "/api/v1/messages/scheduled/2147483647/release", admin.bearer(), 404},
		{"anonymous", path, "", 401},
		{"password_only", path, plain.bearer(), 403},
	} {
		t.Run(c.name, func(t *testing.T) { f.expect(f.do(http.MethodPost, c.path, c.token, ""), c.status, c.name, nil) })
		if row := read(); row.Status != "held" || row.SentMessageID != nil {
			t.Fatalf("%s changed hold: %+v", c.name, row)
		}
	}
	// An unrelated real MFA principal has no mailbox:manage on the sender.
	var stranger freshAccount
	f.expect(f.do("POST", "/api/v1/accounts", admin.bearer(), fmt.Sprintf(`{"address":"stranger@restmail.test","password":%q}`, credentialTestPassword)), 201, "create stranger", &stranger)
	strangerPlain := f.signIn(stranger.Address, credentialTestPassword, "")
	f.expect(f.do("POST", "/api/v1/auth/2fa/enroll", strangerPlain.bearer(), ""), 200, "enroll stranger", &enrolled)
	code, err = auth.GenerateTOTPCode(enrolled.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.expect(f.do("POST", "/api/v1/auth/2fa/confirm", strangerPlain.bearer(), fmt.Sprintf(`{"totp_code":%q}`, code)), 204, "confirm stranger", nil)
	var wrong freshSession
	f.expect(f.do("POST", "/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":%q,"recovery_code":%q,"client":"native"}`, stranger.Address, credentialTestPassword, enrolled.RecoveryCodes[0])), 200, "stranger MFA", &wrong)
	f.expect(f.do("POST", path, wrong.bearer(), ""), 404, "wrong principal", nil)
	if row := read(); row.Status != "held" || row.SentMessageID != nil {
		t.Fatal("wrong principal changed hold")
	}
	var released struct {
		ID            uint   `json:"id"`
		Status        string `json:"status"`
		SentMessageID uint   `json:"sent_message_id"`
	}
	f.expect(f.do("POST", path, admin.bearer(), ""), 200, "release", &released)
	after := read()
	if after.ID != before.ID || after.AccountID != sender.ID || after.Status != "sent" || after.LastError != "" || after.SentMessageID == nil || *after.SentMessageID != released.SentMessageID || released.ID != held.ID || released.Status != "sent" {
		t.Fatalf("same hold did not persist as sent: response %+v, row %+v", released, after)
	}
	var sent models.Message
	if err := db.First(&sent, *after.SentMessageID).Error; err != nil {
		t.Fatal(err)
	}
	if sent.MailboxID != sender.ID || sent.Folder != "Sent" || sent.Subject != "Held transition proof" || sent.BodyText != "retained bytes" {
		t.Fatalf("wrong persisted Sent message: %+v", sent)
	}
	var queued int64
	if err := db.Model(&models.OutboundQueue{}).Count(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if queued != 2 {
		t.Fatalf("queued %d, want first send and released send", queued)
	}
	f.expect(f.do("POST", path, admin.bearer(), ""), 409, "non-held control", nil)
	var afterQueue int64
	if err := db.Model(&models.OutboundQueue{}).Count(&afterQueue).Error; err != nil {
		t.Fatal(err)
	}
	if afterQueue != queued || read().SentMessageID == nil || *read().SentMessageID != sent.ID {
		t.Fatal("non-held retry sent twice or changed the record")
	}
}
