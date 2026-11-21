package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

func personalMailboxKey(t *testing.T, db *gorm.DB, accountID uint, address, capability string) string {
	t.Helper()
	key, public, hash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	credential := models.Credential{
		AccountID: accountID, Kind: models.CredentialKindAPIKey, Name: capability, PublicID: public, SecretHash: hash,
		Scope: models.CredentialScope{Permissions: []auth.ScopedPermission{{Capability: capability, Scope: grants.Mailbox(address)}}},
	}
	if err := db.Create(&credential).Error; err != nil {
		t.Fatal(err)
	}
	return "Bearer " + key
}

func TestAC01LocalSubmissionPreservesSentStateAndRecipientOutcomes(t *testing.T) {
	fixture := newLocalRoute(t, spfOtherServer, true, pipeline.FilterConfig{
		Name: "extract_attachments", Type: pipeline.FilterTypeTransform, Enabled: true,
		Config: json.RawMessage(fmt.Sprintf(`{"storage_path":%q}`, t.TempDir())),
	})
	var sender models.Mailbox
	if err := fixture.db.First(&sender, fixture.mailboxID).Error; err != nil {
		t.Fatal(err)
	}
	refusingID := fixture.seedMailbox(sender.DomainID, "refusing")
	const acceptingAddress = "recipient@example.test"
	const refusingAddress = "refusing@example.test"
	const privateRefusal = "recipient-private policy reason"
	script := fmt.Sprintf(`require ["ereject"]; ereject %q;`, privateRefusal)
	if err := fixture.db.Create(&models.SieveScript{
		MailboxID: refusingID, Script: script, ScriptHash: fmt.Sprintf("%x", sha256.Sum256([]byte(script))), Active: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	reader := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, models.CapMailRead)
	writer := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, models.CapMailWrite)
	_, address, roots := startSubmission(t, fixture.db, fixture.router, fixture.gatewayRouter())
	app := fixture.createAppPassword("Personal mail client")
	client := dialSubmission(t, address, roots, fixture.address, app.Password)
	if err := client.Mail(fixture.address); err != nil {
		t.Fatal(err)
	}
	for _, recipient := range []string{acceptingAddress, refusingAddress} {
		if err := client.Rcpt(recipient); err != nil {
			t.Fatalf("submission RCPT %s: %v", recipient, err)
		}
	}
	const subject = "Local-only submission with two outcomes"
	const attachment = "Personal mail proof"
	headers, _, _ := strings.Cut(submittedMessage(fixture.address, acceptingAddress+", "+refusingAddress, subject), "\r\n\r\n")
	raw := headers + "\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=proof\r\n\r\n" +
		"--proof\r\nContent-Type: text/plain\r\n\r\nSent over SMTP.\r\n" +
		"--proof\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=proof.txt\r\n\r\n" + attachment + "\r\n--proof--\r\n"
	data, err := client.Data()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(data, raw); err != nil {
		t.Fatal(err)
	}
	if err := data.Close(); err != nil {
		t.Fatalf("local-only submission DATA was not accepted: %v", err)
	}
	if err := client.Quit(); err != nil {
		t.Fatalf("submission QUIT: %v", err)
	}

	readSent := func() models.Message {
		t.Helper()
		var sent []models.Message
		if err := fixture.db.Where("mailbox_id = ? AND folder = ?", fixture.mailboxID, "Sent").Find(&sent).Error; err != nil {
			t.Fatal(err)
		}
		if len(sent) != 1 || sent[0].SubmittedAt == nil || sent[0].Subject != subject || sent[0].MsgID == "" {
			t.Fatalf("Sent has %d rows, want exactly one submitted copy of %q: %+v", len(sent), subject, sent)
		}
		source, err := io.ReadAll(sent[0].SourceReader())
		if err != nil || !strings.HasSuffix(string(source), raw) {
			t.Fatalf("Sent source does not contain the submitted bytes after the trace header: %q (%v)", source, err)
		}
		return sent[0]
	}
	sent := readSent()
	path := fmt.Sprintf("/api/v1/messages/%d", sent.ID)
	readOutcomes := func() []handlers.RecipientStatus {
		t.Helper()
		response := fixture.do(http.MethodGet, path+"/recipients", reader, "")
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), privateRefusal) {
			t.Fatalf("recipient status read: %d %s", response.Code, response.Body)
		}
		var reply struct{ Data handlers.MessageRecipients }
		decodeInto(t, response, &reply)
		recipients := reply.Data.Recipients
		if reply.Data.MessageID != sent.ID || len(recipients) != 2 ||
			recipients[0].Address != acceptingAddress || recipients[0].Delivery != "local" ||
			recipients[1].Address != refusingAddress || recipients[1].Delivery != "local" {
			t.Fatalf("want one local outcome per recipient of Sent message %d, in To order: %+v", sent.ID, reply.Data)
		}
		return recipients
	}
	for _, recipient := range readOutcomes() {
		if recipient.State != "queued" || recipient.Attempts != 0 || recipient.LastAttemptAt != nil {
			t.Fatalf("submission accepted but not yet delivered: %+v, want queued and no attempt", recipient)
		}
	}

	startMailRoute(t, fixture.db, fixture.router, fixture.gatewayRouter())
	accepted := queueRow(t, fixture.db, acceptingAddress, "delivered", "bounced")
	if accepted.Status != "delivered" || accepted.MessageID == nil || *accepted.MessageID != sent.ID {
		t.Fatalf("accepting recipient queue row = %+v, want delivered and linked to Sent message %d", accepted, sent.ID)
	}
	refused := queueRow(t, fixture.db, refusingAddress, "bounced", "delivered")
	if refused.Status != "bounced" || refused.LastErrorCode != 550 || refused.FailureCategory != models.FailurePolicy ||
		refused.MessageID == nil || *refused.MessageID != sent.ID {
		t.Fatalf("refusing recipient queue row = %+v, want permanent SMTP 550 policy failure linked to Sent message %d", refused, sent.ID)
	}
	for _, mailbox := range []struct {
		id   uint
		want int64
	}{{fixture.recipientID, 1}, {refusingID, 0}} {
		var copies int64
		err := fixture.db.Model(&models.Message{}).Where("mailbox_id = ? AND message_id = ?", mailbox.id, sent.MsgID).Count(&copies).Error
		if err != nil || copies != mailbox.want {
			t.Fatalf("mailbox %d has %d copies of submitted message, want %d (%v)", mailbox.id, copies, mailbox.want, err)
		}
	}
	assertBounced(t, fixture, refusingAddress)
	outcomes := readOutcomes()
	if recipient := outcomes[0]; recipient.State != "delivered" || recipient.Category != "" || recipient.Reason != "" {
		t.Errorf("accepting recipient API outcome = %+v, want delivered without a failure", recipient)
	}
	if recipient := outcomes[1]; recipient.State != "failed" || recipient.Category != "policy" || recipient.Reason != models.FailurePolicy.Description() ||
		recipient.Attempts != 1 || recipient.LastAttemptAt == nil {
		t.Errorf("refusing recipient API outcome = %+v, want failed (policy) after one attempt", recipient)
	}
	var received models.Message
	if err := fixture.db.Where("mailbox_id = ? AND message_id = ?", fixture.recipientID, sent.MsgID).First(&received).Error; err != nil {
		t.Fatal(err)
	}
	attachmentPath := fmt.Sprintf("/api/v1/messages/%d/attachments", received.ID)
	if response := fixture.do(http.MethodGet, attachmentPath, reader, ""); response.Code != http.StatusNotFound {
		t.Errorf("sender's key reading another mailbox's attachments = %d, want 404", response.Code)
	}
	recipientReader := personalMailboxKey(t, fixture.db, fixture.recipientID, acceptingAddress, models.CapMailRead)
	response := fixture.do(http.MethodGet, attachmentPath, recipientReader, "")
	if response.Code != http.StatusOK {
		t.Fatalf("list received attachments: %d %s", response.Code, response.Body)
	}
	var attachments struct{ Data []handlers.MessageAttachment }
	decodeInto(t, response, &attachments)
	if len(attachments.Data) != 1 || attachments.Data[0].Filename != "proof.txt" ||
		attachments.Data[0].Checksum != fmt.Sprintf("%x", sha256.Sum256([]byte(attachment))) {
		t.Fatalf("received attachments = %+v, want one proof.txt with the submitted part's hash", attachments.Data)
	}

	readStar := func() handlers.Message {
		t.Helper()
		response := fixture.do(http.MethodGet, path, reader, "")
		if response.Code != http.StatusOK {
			t.Fatalf("read Sent message: %d %s", response.Code, response.Body)
		}
		var reply struct{ Data handlers.Message }
		decodeInto(t, response, &reply)
		if reply.Data.ID != sent.ID || reply.Data.Folder != "Sent" {
			t.Fatalf("read returned message %d in %q, want %d in Sent", reply.Data.ID, reply.Data.Folder, sent.ID)
		}
		return reply.Data
	}
	before := readStar()
	if before.IsStarred {
		t.Fatal("submitted message is already starred")
	}
	const star = `{"is_starred":true}`
	if response := fixture.do(http.MethodPatch, path, reader, star); response.Code != http.StatusForbidden {
		t.Errorf("mail:read key patch status = %d, want 403", response.Code)
	}
	afterRefusal, storedAfterRefusal := readStar(), readSent()
	if afterRefusal.IsStarred || afterRefusal.ModSeq != before.ModSeq || storedAfterRefusal.IsStarred || storedAfterRefusal.ModSeq != before.ModSeq {
		t.Fatalf("refused patch changed the message: API starred=%t, mod_seq=%d; stored starred=%t, mod_seq=%d (was %d)",
			afterRefusal.IsStarred, afterRefusal.ModSeq, storedAfterRefusal.IsStarred, storedAfterRefusal.ModSeq, before.ModSeq)
	}
	if response := fixture.do(http.MethodPatch, path, writer, star); response.Code != http.StatusOK {
		t.Fatalf("mail:write key patch: %d %s", response.Code, response.Body)
	}
	after := readStar()
	stored := readSent()
	if !after.IsStarred || after.ModSeq <= before.ModSeq || stored.ID != sent.ID || !stored.IsStarred || stored.ModSeq != after.ModSeq {
		t.Fatalf("patch not visible in API and storage: API starred=%t, mod_seq=%d; stored id=%d, starred=%t, mod_seq=%d",
			after.IsStarred, after.ModSeq, stored.ID, stored.IsStarred, stored.ModSeq)
	}
}
