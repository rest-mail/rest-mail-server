package queue

import (
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// dsnTestItem is a representative failed outbound row for the DSN builder tests.
func dsnTestItem() models.OutboundQueue {
	created := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	return models.OutboundQueue{
		ID:         42,
		Sender:     "alice@local.test",
		Recipient:  "bob@remote.test",
		Domain:     "remote.test",
		Attempts:   3,
		CreatedAt:  created,
		RawMessage: "From: alice@local.test\r\nTo: bob@remote.test\r\nSubject: hi\r\n\r\nbody\r\n",
	}
}

// TestBuildDSNMessage_RequiredHeaders is the red-green guard for the "DSN missing
// headers" backlog item: a generated DSN must carry Auto-Submitted (RFC 3834 §5,
// so auto-responders do not reply and create backscatter), and its per-recipient
// status block must carry Last-Attempt-Date (RFC 3464 §2.3.7) and Remote-MTA
// (RFC 3464 §2.3.5) naming the failing next hop.
func TestBuildDSNMessage_RequiredHeaders(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 30, 0, 0, time.UTC)
	lastAttempt := time.Date(2026, 7, 25, 12, 29, 0, 0, time.UTC)
	smtpErr := &SMTPError{Code: 550, Enhanced: "5.1.1", Message: "user unknown", RemoteMTA: "mx1.remote.test"}

	dsn := buildDSNMessage("mail.local.test", dsnTestItem(), smtpErr, now, lastAttempt)

	mustContain := []string{
		"Auto-Submitted: auto-generated\r\n",
		"Last-Attempt-Date: " + lastAttempt.Format(time.RFC1123Z) + "\r\n",
		"Remote-MTA: dns; mx1.remote.test\r\n",
		"Status: 5.1.1\r\n",
		"Final-Recipient: rfc822; bob@remote.test\r\n",
		"Action: failed\r\n",
	}
	for _, s := range mustContain {
		if !strings.Contains(dsn.raw, s) {
			t.Errorf("DSN missing required content %q\n---\n%s", s, dsn.raw)
		}
	}
}

// TestBuildDSNMessage_NoRemoteMTAWhenAbsent verifies Remote-MTA is emitted only
// when the failure named a specific peer; a code-less failure (connect/DNS error)
// has no next hop to report.
func TestBuildDSNMessage_NoRemoteMTAWhenAbsent(t *testing.T) {
	now := time.Now()
	dsn := buildDSNMessage("mail.local.test", dsnTestItem(), nil, now, now)
	if strings.Contains(dsn.raw, "Remote-MTA:") {
		t.Errorf("DSN emitted Remote-MTA for a failure with no identified next hop\n%s", dsn.raw)
	}
	// Auto-Submitted and Last-Attempt-Date are unconditional.
	if !strings.Contains(dsn.raw, "Auto-Submitted: auto-generated") {
		t.Error("DSN missing Auto-Submitted even without an SMTP error")
	}
	if !strings.Contains(dsn.raw, "Last-Attempt-Date:") {
		t.Error("DSN missing Last-Attempt-Date even without an SMTP error")
	}
}

// TestBuildDSNMessage_EscapesInjection is the red-green guard for the "DSN
// unescaped remote text" backlog item. A CR/LF in the remote reply text or in the
// (poisoned) recipient address must not inject new header lines or fake
// message/delivery-status fields into this trusted mailer-daemon message.
func TestBuildDSNMessage_EscapesInjection(t *testing.T) {
	item := dsnTestItem()
	// Poisoned recipient smuggling a forged header and a forged DSN field.
	item.Recipient = "victim@remote.test>\r\nX-Evil-Recipient: injected\r\nAction: delivered"
	// Multiline remote reply smuggling a forged header.
	smtpErr := &SMTPError{
		Code:      550,
		Message:   "rejected\r\nX-Evil-Diag: injected",
		RemoteMTA: "mx1.remote.test\r\nX-Evil-MTA: injected",
	}

	now := time.Now()
	dsn := buildDSNMessage("mail.local.test", item, smtpErr, now, now)

	// None of the injected control sequences may survive as real line breaks.
	forbidden := []string{
		"\r\nX-Evil-Recipient:",
		"\r\nX-Evil-Diag:",
		"\r\nX-Evil-MTA:",
		"\r\nAction: delivered",
	}
	for _, f := range forbidden {
		if strings.Contains(dsn.raw, f) {
			t.Errorf("DSN allowed header/field injection via %q\n---\n%s", f, dsn.raw)
		}
	}

	// The content is still present, neutralized inline (CRLF collapsed to spaces),
	// so the diagnostic remains legible rather than silently dropped.
	if !strings.Contains(dsn.raw, "X-Evil-Diag: injected") {
		t.Error("sanitized remote text should still appear inline, just neutralized")
	}
	// The Subject header (built from the recipient) must remain a single line.
	if strings.Contains(dsn.subject, "\r") || strings.Contains(dsn.subject, "\n") {
		t.Errorf("bounce subject contains a raw line break: %q", dsn.subject)
	}
}
