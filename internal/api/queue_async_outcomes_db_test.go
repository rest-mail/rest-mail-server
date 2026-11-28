package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

func TestAC02LocalAndRemoteSubmissionReportWorkerOutcomes(t *testing.T) {
	const (
		accepting = "recipient@example.test"
		refusing  = "refusing@example.test"
		remote    = "someone@elsewhere.test"
	)
	for _, route := range []string{"send", "draft", "smtp"} {
		t.Run(route, func(t *testing.T) {
			fixture := newLocalRoute(t, spfOtherServer, true, pipeline.FilterConfig{
				Name: "greylist", Type: pipeline.FilterTypeAction, Enabled: true,
				Config: json.RawMessage(`{"delay_minutes":5,"ttl_days":36}`),
			})
			var sender models.Mailbox
			if err := fixture.db.First(&sender, fixture.mailboxID).Error; err != nil {
				t.Fatal(err)
			}
			refusingID := fixture.seedMailbox(sender.DomainID, "refusing")
			script := `require ["ereject"]; ereject "Local policy refusal";`
			if err := fixture.db.Create(&models.SieveScript{MailboxID: refusingID, Script: script,
				ScriptHash: fmt.Sprintf("%x", sha256.Sum256([]byte(script))), Active: true}).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Create(&models.GreylistEntry{Sender: fixture.address, Recipient: refusing, SourceIP: "127.0.0.1",
				FirstSeen: time.Now().Add(-time.Hour), Passed: true, ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
				t.Fatal(err)
			}

			recipients := []string{accepting, refusing, remote}
			if route == "smtp" {
				submitQueueOutcomeSMTP(t, fixture, recipients)
			} else {
				wantSent(t, route, fixture.send(t, route, fixture.session, recipients...))
			}

			var sent []models.Message
			if err := fixture.db.Where("mailbox_id = ? AND folder = 'Sent'", fixture.mailboxID).Find(&sent).Error; err != nil {
				t.Fatal(err)
			}
			if len(sent) != 1 {
				t.Fatalf("submission acknowledged but has %d durable Sent copies, want 1", len(sent))
			}
			assertOutcome := func(address, delivery, state string, attempts int, category models.FailureCategory) {
				t.Helper()
				response := fixture.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/recipients", sent[0].ID), fixture.session, "")
				if response.Code != http.StatusOK {
					t.Fatalf("read sender's recipient outcomes: %d %s", response.Code, response.Body)
				}
				var reply struct{ Data handlers.MessageRecipients }
				decodeInto(t, response, &reply)
				if reply.Data.MessageID != sent[0].ID || len(reply.Data.Recipients) != len(recipients) {
					t.Fatalf("want one outcome per recipient for Sent %d: %+v", sent[0].ID, reply.Data)
				}
				for _, recipient := range reply.Data.Recipients {
					if recipient.Address != address {
						continue
					}
					if recipient.Delivery != delivery || recipient.State != state || recipient.Attempts != attempts ||
						(attempts == 0) != (recipient.LastAttemptAt == nil) || recipient.Category != string(category) ||
						recipient.Reason != category.Description() {
						t.Fatalf("sender's outcome for %s: %+v; want %s %s after %d attempts (category %q)", address, recipient, delivery, state, attempts, category)
					}
					return
				}
				t.Fatalf("sent message has no outcome for %s: %+v", address, reply.Data)
			}
			assertCopies := func(mailboxID uint, want int64) {
				t.Helper()
				var got int64
				if err := fixture.db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = 'INBOX'", mailboxID).Count(&got).Error; err != nil || got != want {
					t.Fatalf("mailbox %d has %d inbox copies, want %d (%v)", mailboxID, got, want, err)
				}
			}
			var queued []models.OutboundQueue
			if err := fixture.db.Where("message_id = ?", sent[0].ID).Find(&queued).Error; err != nil || len(queued) != len(recipients) {
				t.Fatalf("submission acknowledged with %d durable queue rows, want %d (%v)", len(queued), len(recipients), err)
			}
			for _, row := range queued {
				if row.Status != "pending" || row.Attempts != 0 || row.LastAttempt != nil {
					t.Fatalf("submission acknowledged with queue row %d in %s after %d attempts, want pending and unattempted", row.ID, row.Status, row.Attempts)
				}
			}
			assertOutcome(accepting, "local", "queued", 0, "")
			assertOutcome(refusing, "local", "queued", 0, "")
			assertOutcome(remote, "remote", "queued", 0, "")
			assertCopies(fixture.recipientID, 0)
			assertCopies(refusingID, 0)
			assertCopies(fixture.mailboxID, 0)
			if scans := fixture.scans.Load(); scans != 0 {
				t.Fatalf("inbound scanner ran %d times before the queue worker started", scans)
			}

			startMailRoute(t, fixture.db, fixture.router, fixture.gatewayRouter(), testMessageSources(t, fixture.db, fixture.cfg))
			deferred := queueRow(t, fixture.db, accepting, "deferred", "bounced", "delivered")
			if deferred.Status != "deferred" || deferred.LastErrorCode != 450 || !deferred.NextAttempt.After(time.Now()) {
				t.Fatalf("first local attempt = %s (SMTP %d, retry %s), want greylist deferral: %s", deferred.Status, deferred.LastErrorCode, deferred.NextAttempt, deferred.LastError)
			}
			assertOutcome(accepting, "local", "retrying", 1, models.FailureGreylisted)
			assertCopies(fixture.recipientID, 0)
			refused := queueRow(t, fixture.db, refusing, "deferred", "bounced", "delivered")
			if refused.Status != "bounced" || refused.LastErrorCode != 550 {
				t.Fatalf("permanent local refusal = %s (SMTP %d), want bounce: %s", refused.Status, refused.LastErrorCode, refused.LastError)
			}
			assertOutcome(refusing, "local", "failed", 1, models.FailurePolicy)
			assertCopies(refusingID, 0)
			if row := queueRow(t, fixture.db, remote, "deferred", "bounced", "delivered"); row.Status != "bounced" {
				t.Fatalf("remote NXDOMAIN = %s, want a permanent failure: %s", row.Status, row.LastError)
			}
			assertOutcome(remote, "remote", "failed", 1, models.FailureBadDomain)
			assertQueueOutcomeDSNs(t, fixture, refusing, remote)

			retry := fixture.db.Model(&models.GreylistEntry{}).Where("sender = ? AND recipient = ?", fixture.address, accepting).
				UpdateColumn("retry_after", time.Now().Add(-time.Minute))
			if retry.Error != nil || retry.RowsAffected != 1 {
				t.Fatalf("advance one persisted greylist window: rows=%d, err=%v", retry.RowsAffected, retry.Error)
			}
			if err := fixture.db.Model(&deferred).UpdateColumn("next_attempt", time.Now().Add(-time.Second)).Error; err != nil {
				t.Fatal(err)
			}
			if row := queueRow(t, fixture.db, accepting, "delivered", "bounced"); row.Status != "delivered" || row.ID != deferred.ID {
				t.Fatalf("greylist retry = row %d %s, want original row %d delivered: %s", row.ID, row.Status, deferred.ID, row.LastError)
			}
			assertOutcome(accepting, "local", "delivered", 2, "")
			assertCopies(fixture.recipientID, 1)
			assertQueueOutcomeDSNs(t, fixture, refusing, remote)
		})
	}
}

func submitQueueOutcomeSMTP(t *testing.T, fixture *localRoute, recipients []string) {
	t.Helper()
	_, addr, roots := startSubmission(t, fixture.db, fixture.router, fixture.gatewayRouter())
	client := dialSubmission(t, addr, roots, fixture.address, fixture.createAppPassword("queue outcomes").Password)
	if err := client.Mail(fixture.address); err != nil {
		t.Fatal(err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			t.Fatalf("submission RCPT %s: %v", recipient, err)
		}
	}
	data, err := client.Data()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(data, submittedMessage(fixture.address, strings.Join(recipients, ", "), "Queue outcome proof")); err != nil {
		t.Fatal(err)
	}
	if err := data.Close(); err != nil {
		t.Fatalf("submission did not acknowledge DATA before the worker started: %v", err)
	}
	if err := client.Quit(); err != nil {
		t.Fatal(err)
	}
}

func assertQueueOutcomeDSNs(t *testing.T, fixture *localRoute, recipients ...string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var notices []models.Message
		if err := fixture.db.Where("mailbox_id = ? AND folder = 'INBOX' AND is_dsn", fixture.mailboxID).Find(&notices).Error; err != nil {
			t.Fatal(err)
		}
		if len(notices) < len(recipients) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if len(notices) != len(recipients) {
			t.Fatalf("sender's inbox has %d DSNs, want %d (only permanent failures)", len(notices), len(recipients))
		}
		for _, address := range recipients {
			matches := 0
			for _, notice := range notices {
				raw, err := io.ReadAll(notice.SourceReader())
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), "Final-Recipient: rfc822; "+address+"\r\nAction: failed\r\n") {
					matches++
				}
			}
			if matches != 1 {
				t.Errorf("sender has %d failure DSNs for %s, want 1", matches, address)
			}
		}
		return
	}
}
