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

// The bounce says why in plain words, from the failure's category, next to the
// remote's own reply.
func TestBuildDSNMessage_SaysWhyInPlainWords(t *testing.T) {
	item := dsnTestItem()
	item.FailureCategory = models.FailureMailboxFull
	smtpErr := &SMTPError{Code: 452, Enhanced: "4.2.2", Message: "The recipient's inbox is out of storage space."}
	now := time.Now()
	dsn := buildDSNMessage("mail.local.test", item, smtpErr, now, now)
	if !strings.Contains(dsn.humanPart, "The recipient's mailbox is full.\r\n") {
		t.Errorf("bounce text gives no reason\n---\n%s", dsn.humanPart)
	}
	if !strings.Contains(dsn.humanPart, "Error: smtp; 452 The recipient's inbox is out of storage space.") {
		t.Errorf("bounce text lost the remote's reply\n---\n%s", dsn.humanPart)
	}
}

// TestBuildDSNMessage_InternationalizedRecipient covers RFC 6533 (#153): a
// UTF-8 recipient is reported with the utf-8 address type inside a
// message/global-delivery-status part, and UTF-8 original headers are returned
// as message/global-headers.
func TestBuildDSNMessage_InternationalizedRecipient(t *testing.T) {
	item := dsnTestItem()
	item.Recipient = "jörg+news@bücher.test"
	item.RawMessage = "From: alice@local.test\r\nTo: jörg+news@bücher.test\r\nSubject: grüße\r\n\r\nbody\r\n"
	now := time.Date(2026, 7, 25, 12, 30, 0, 0, time.UTC)

	dsn := buildDSNMessage("mail.local.test", item, &SMTPError{Code: 553, Enhanced: "5.6.7", Message: "no SMTPUTF8"}, now, now)

	for _, s := range []string{
		"Content-Type: multipart/report; report-type=global-delivery-status;",
		"Content-Type: message/global-delivery-status\r\n",
		`Final-Recipient: utf-8; jörg\x{2B}news@bücher.test` + "\r\n",
		"Content-Type: message/global-headers\r\n",
		"Subject: grüße\r\n",
	} {
		if !strings.Contains(dsn.raw, s) {
			t.Errorf("DSN missing %q\n---\n%s", s, dsn.raw)
		}
	}
	for _, s := range []string{"message/delivery-status", "text/rfc822-headers", "rfc822; "} {
		if strings.Contains(dsn.raw, s) {
			t.Errorf("internationalized DSN still contains ASCII-only %q\n---\n%s", s, dsn.raw)
		}
	}
}

// TestBuildDSNMessage_ASCIIKeepsRFC3464Types verifies an all-ASCII failure keeps
// the RFC 3464 media types and rfc822 address type, plus signs included.
func TestBuildDSNMessage_ASCIIKeepsRFC3464Types(t *testing.T) {
	item := dsnTestItem()
	item.Recipient = "bob+tag@remote.test"
	now := time.Now()

	dsn := buildDSNMessage("mail.local.test", item, nil, now, now)

	for _, s := range []string{
		"report-type=delivery-status;",
		"Content-Type: message/delivery-status\r\n",
		"Final-Recipient: rfc822; bob+tag@remote.test\r\n",
		"Content-Type: text/rfc822-headers\r\n",
	} {
		if !strings.Contains(dsn.raw, s) {
			t.Errorf("DSN missing %q\n---\n%s", s, dsn.raw)
		}
	}
	if strings.Contains(dsn.raw, "global-") {
		t.Errorf("ASCII DSN used an RFC 6533 global type\n%s", dsn.raw)
	}
}

// TestBuildDSNMessage_UTF8HeadersOnly checks each part is typed on its own: an
// ASCII recipient with UTF-8 original headers keeps message/delivery-status but
// returns the headers as message/global-headers.
func TestBuildDSNMessage_UTF8HeadersOnly(t *testing.T) {
	item := dsnTestItem()
	item.RawMessage = "From: alice@local.test\r\nSubject: café\r\n\r\nbody\r\n"
	now := time.Now()

	dsn := buildDSNMessage("mail.local.test", item, nil, now, now)

	if !strings.Contains(dsn.raw, "Content-Type: message/delivery-status\r\n") {
		t.Errorf("ASCII status block not typed message/delivery-status\n%s", dsn.raw)
	}
	if !strings.Contains(dsn.raw, "Content-Type: message/global-headers\r\n") {
		t.Errorf("UTF-8 headers not typed message/global-headers\n%s", dsn.raw)
	}
}

// TestDSNRecipientField checks the RFC 6533 unitext escaping: "+", "=", "\" and
// space become \x{HEX}; other printable ASCII and UTF-8 pass through.
func TestDSNRecipientField(t *testing.T) {
	cases := map[string]string{
		"bob@remote.test":       "rfc822; bob@remote.test",
		"用户@例え.jp":              "utf-8; 用户@例え.jp",
		`"a b=c\d"@bücher.test`: `utf-8; "a\x{20}b\x{3D}c\x{5C}d"@bücher.test`,
		"x+y@bücher.test":       `utf-8; x\x{2B}y@bücher.test`,
	}
	for in, want := range cases {
		if got := dsnRecipientField(in); got != want {
			t.Errorf("dsnRecipientField(%q) = %q, want %q", in, got, want)
		}
	}
}
