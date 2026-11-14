package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// A sender sees each recipient's outcome: one bounced and one delivered remote
// recipient, a local recipient whose copy arrived, and nothing of the remote
// server's own reply; another account gets 404 (#1568).
func TestMessageRecipientsShowEachOutcome(t *testing.T) {
	database, err := connectTestDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSchema(database); err != nil {
		t.Fatal(err)
	}
	tx := database.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedAuthzFixture(t, tx)
	seedReceiveFixture(t, tx, fixture.ownerMailbox, fixture.attackerMailbox)
	owner, other := fixture.ownerMailbox, fixture.attackerMailbox

	const rawReply = "550 5.1.1 <gone@remote.test>: internal host mx7.secret.remote.test rejected, user unknown"
	to, _ := json.Marshal([]string{"gone@remote.test", "ok@remote.test", other.Address})
	sent := models.Message{MailboxID: owner.ID, Folder: "Sent", MsgID: "<1568@idor.test>", Sender: owner.Address,
		RecipientsTo: models.JSONB(to), Subject: "outcomes", ReceivedAt: time.Now()}
	if err := tx.Create(&sent).Error; err != nil {
		t.Fatal(err)
	}
	attempted := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	rows := []models.OutboundQueue{
		{MessageID: &sent.ID, Sender: owner.Address, Recipient: "gone@remote.test", Domain: "remote.test", Status: "bounced", Attempts: 1,
			LastAttempt: &attempted, LastError: rawReply, LastErrorCode: 550, FailureCategory: models.FailureBadMailbox},
		{MessageID: &sent.ID, Sender: owner.Address, Recipient: "ok@remote.test", Domain: "remote.test", Status: "delivered", Attempts: 1, LastAttempt: &attempted},
		{MessageID: &sent.ID, Sender: owner.Address, Recipient: "hidden@remote.test", Domain: "remote.test", Status: "deferred", Attempts: 2,
			LastAttempt: &attempted, LastError: "421 4.7.0 try later from 10.0.0.9", LastErrorCode: 421, FailureCategory: models.FailureRateLimited},
	}
	if err := tx.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	local := models.Message{MailboxID: other.ID, Folder: "INBOX", MsgID: sent.MsgID, Sender: owner.Address, Subject: "outcomes", ReceivedAt: time.Now()}
	if err := tx.Create(&local).Error; err != nil {
		t.Fatal(err)
	}

	handler := NewMessageHandler(tx, nil, nil, "", nil)
	get := func(actor uint, id uint) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		messageRoutes(handler).ServeHTTP(response, authzRequest(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/recipients", id), actor, nil, ""))
		return response
	}

	response := get(owner.ID, sent.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("sender: %d %s", response.Code, response.Body)
	}
	for _, leak := range []string{"mx7.secret", "5.1.1", "10.0.0.9", "last_error", "550"} {
		if strings.Contains(response.Body.String(), leak) {
			t.Fatalf("the remote reply reached the sender (%q): %s", leak, response.Body)
		}
	}
	var body struct {
		Data MessageRecipients `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := map[string]RecipientStatus{}
	var order []string
	for _, r := range body.Data.Recipients {
		got[r.Address] = r
		order = append(order, r.Address)
	}
	if want := []string{"gone@remote.test", "ok@remote.test", other.Address, "hidden@remote.test"}; strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("recipients %v, want %v", order, want)
	}
	if r := got["gone@remote.test"]; r.State != "failed" || r.Reason != models.FailureBadMailbox.Description() || r.Category != "bad_mailbox" ||
		r.Attempts != 1 || r.LastAttemptAt == nil || !r.LastAttemptAt.Equal(attempted) || r.Delivery != "remote" {
		t.Fatalf("bounced recipient: %+v", r)
	}
	if r := got["ok@remote.test"]; r.State != "delivered" || r.Reason != "" || r.Attempts != 1 {
		t.Fatalf("delivered recipient: %+v", r)
	}
	if r := got[other.Address]; r.State != "delivered" || r.Delivery != "local" || r.Attempts != 0 {
		t.Fatalf("local recipient: %+v", r)
	}
	if r := got["hidden@remote.test"]; r.State != "retrying" || r.Reason != models.FailureRateLimited.Description() || r.Attempts != 2 {
		t.Fatalf("bcc recipient: %+v", r)
	}

	// Another account cannot learn anything about the message.
	if response := get(other.ID, sent.ID); response.Code != http.StatusNotFound {
		t.Fatalf("other account: %d %s", response.Code, response.Body)
	}

	// A local recipient whose copy is gone, or never arrived, is unknown, not delivered.
	if err := tx.Delete(&local).Error; err != nil {
		t.Fatal(err)
	}
	response = get(owner.ID, sent.ID)
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, r := range body.Data.Recipients {
		if r.Address == other.Address && (r.State != "unknown" || r.Reason == "") {
			t.Fatalf("local recipient without a copy: %+v", r)
		}
	}

	// A message the account received, not sent, has no recipients to report.
	inbox := models.Message{MailboxID: owner.ID, Folder: "INBOX", MsgID: "<in@x>", Sender: "x@remote.test", RecipientsTo: models.JSONB(to), ReceivedAt: time.Now()}
	if err := tx.Create(&inbox).Error; err != nil {
		t.Fatal(err)
	}
	response = get(owner.ID, inbox.ID)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"recipients":[]`) {
		t.Fatalf("received message: %d %s", response.Code, response.Body)
	}
}

// Every send queues every recipient (#1564). A queued recipient on this server is read
// from its queue row, with no copy needed yet, and stays local; so does a Bcc recipient,
// who is in no header. An address not hosted here stays remote.
func TestMessageRecipientsReadAQueuedLocalRecipientFromItsRow(t *testing.T) {
	database, err := connectTestDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSchema(database); err != nil {
		t.Fatal(err)
	}
	tx := database.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedAuthzFixture(t, tx)
	seedReceiveFixture(t, tx, fixture.ownerMailbox, fixture.attackerMailbox)
	owner, other := fixture.ownerMailbox, fixture.attackerMailbox

	to, _ := json.Marshal([]string{other.Address, "out@remote.test"})
	sent := models.Message{MailboxID: owner.ID, Folder: "Sent", MsgID: "<1564@idor.test>", Sender: owner.Address,
		RecipientsTo: models.JSONB(to), Subject: "queued", ReceivedAt: time.Now()}
	if err := tx.Create(&sent).Error; err != nil {
		t.Fatal(err)
	}
	attempted := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	rows := []models.OutboundQueue{
		{MessageID: &sent.ID, Sender: owner.Address, Recipient: other.Address, Domain: "idor.test", Status: "deferred", Attempts: 1,
			LastAttempt: &attempted, LastError: "451 4.3.0 Try again later", LastErrorCode: 451, FailureCategory: models.FailureOtherTransient},
		{MessageID: &sent.ID, Sender: owner.Address, Recipient: "out@remote.test", Domain: "remote.test", Status: "pending"},
		{MessageID: &sent.ID, Sender: owner.Address, Recipient: owner.Address, Domain: "idor.test", Status: "delivered", Attempts: 1, LastAttempt: &attempted},
	}
	if err := tx.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	messageRoutes(NewMessageHandler(tx, nil, nil, "", nil)).ServeHTTP(response,
		authzRequest(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/recipients", sent.ID), owner.ID, nil, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
	var body struct {
		Data MessageRecipients `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := map[string]RecipientStatus{}
	for _, r := range body.Data.Recipients {
		got[r.Address] = r
	}
	if len(got) != 3 {
		t.Fatalf("recipients: %+v", body.Data.Recipients)
	}
	if r := got[other.Address]; r.Delivery != "local" || r.State != "retrying" || r.Attempts != 1 || r.Reason != models.FailureOtherTransient.Description() {
		t.Fatalf("queued local recipient: %+v", r)
	}
	if r := got["out@remote.test"]; r.Delivery != "remote" || r.State != "queued" || r.Attempts != 0 {
		t.Fatalf("queued remote recipient: %+v", r)
	}
	if r := got[owner.Address]; r.Delivery != "local" || r.State != "delivered" || r.Attempts != 1 {
		t.Fatalf("local Bcc recipient: %+v", r)
	}
}
