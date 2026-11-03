package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// A forward supplies the unsubscribe details a bulk domain's mail needs, as a send does
// (#144): without them a forward from a bulk domain could only be refused.
func TestForward_TakesListUnsubscribe(t *testing.T) {
	var in forwardRequest
	params := func(where, name string) (string, bool) { return "1", where == "path" && name == "id" }
	body := `{"from":"news@example.test","to":["reader@remote.test"],"list_unsubscribe":{"mailto":"unsub@example.test"}}`
	if err := oapifly.Bind(&in, params, []byte(body)); err != nil {
		t.Fatalf("a forward with list_unsubscribe did not bind: %v", err)
	}
	if in.Body.ListUnsubscribe == nil || in.Body.ListUnsubscribe.Mailto != "unsub@example.test" {
		t.Fatalf("list_unsubscribe = %+v", in.Body.ListUnsubscribe)
	}
}

// On a bulk domain a forward without list_unsubscribe is refused, and one with it is sent
// carrying the header, as a send is.
func TestForward_FollowsTheDomainSendingPolicy(t *testing.T) {
	h, _, _ := seedSigningDomain(t, "forward-policy")
	// The forward size limit the server sets at start (config.DefaultSMTPMaxMessageSize).
	h.SetForwardStorage(0, nil)
	domain := fmt.Sprintf("forward-bulk-%d.test", time.Now().UnixNano())
	if err := h.db.Create(&models.Domain{Name: domain, SendingPolicy: models.SendingPolicyBulk}).Error; err != nil {
		t.Fatalf("seed the domain: %v", err)
	}
	sender := seedSendingMailbox(t, h.db, domain, "news")
	// The test seeds every table, role and grant it reads, so it runs alone (#1385).
	if err := h.db.AutoMigrate(accountModels...); err != nil {
		t.Skipf("forward test skipped: migrate failed (%v)", err)
	}
	// A forward reads the original's attachments.
	if err := h.db.AutoMigrate(&models.Attachment{}); err != nil {
		t.Skipf("forward test skipped: migrate failed (%v)", err)
	}
	if err := rmdb.MigrateAuditLog(h.db); err != nil {
		t.Skipf("forward test skipped: audit log migrate failed (%v)", err)
	}
	if err := bootstrap.EnsureRBAC(h.db); err != nil {
		t.Fatalf("seed the roles: %v", err)
	}
	// The account owns its mailbox, so it may read its mail and send from it.
	grantOwnerRole(t, h.db, sender.ID, sender.Address)
	// A forward's Sent copy is charged to the sender's quota row, which every account has.
	if err := h.db.Create(&models.QuotaUsage{MailboxID: sender.ID}).Error; err != nil {
		t.Fatalf("seed the sender's quota usage: %v", err)
	}
	original := models.Message{MailboxID: sender.ID, Folder: "INBOX", Subject: "receipt", BodyText: "hello",
		Sender: "shop@remote.test", RecipientsTo: models.JSONB(`["news@` + domain + `"]`), RecipientsCc: models.JSONB(`[]`)}
	if err := h.db.Create(&original).Error; err != nil {
		t.Fatalf("seed the message: %v", err)
	}
	claims := &auth.Claims{AccountID: sender.ID, CredentialKind: "app_password"}
	forward := func(extra string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"from":%q,"to":["reader@remote.test"]%s}`, sender.Address, extra)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/messages/%d/forward", original.ID), strings.NewReader(body))
		rr := httptest.NewRecorder()
		composeRoute(h)(rr, withClaims(req, claims))
		return rr
	}

	if rr := forward(""); rr.Code != http.StatusUnprocessableEntity || errorCodeOf(rr) != "list_unsubscribe_required" {
		t.Fatalf("forward without list_unsubscribe: %d %s", rr.Code, rr.Body.String())
	}
	rr := forward(fmt.Sprintf(`,"list_unsubscribe":{"mailto":"unsub@%s"}`, domain))
	if rr.Code != http.StatusCreated {
		t.Fatalf("forward with list_unsubscribe: %d %s", rr.Code, rr.Body.String())
	}
	var sent models.Message
	if err := json.Unmarshal(dataOf(t, rr), &sent); err != nil {
		t.Fatal(err)
	}
	var queued models.OutboundQueue
	if err := h.db.Where("message_id = ?", sent.ID).First(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if got := headerFields(queued.RawMessage)["list-unsubscribe"]; len(got) != 1 || got[0] != "<mailto:unsub@"+domain+">" {
		t.Fatalf("List-Unsubscribe = %q", got)
	}
}
