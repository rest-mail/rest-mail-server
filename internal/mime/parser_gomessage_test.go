package mime

import (
	"encoding/base64"
	stdmime "mime"
	"strings"
	"testing"
)

// crlf joins lines with CRLF, the canonical MIME line ending.
func crlf(lines ...string) string {
	return strings.Join(lines, "\r\n")
}

// TestParse_EncodedWordSubjectAndFrom verifies RFC 2047 encoded-words in the
// Subject and in a From display-name are decoded to UTF-8.
func TestParse_EncodedWordSubjectAndFrom(t *testing.T) {
	encSubject := stdmime.BEncoding.Encode("utf-8", "Réunion café ☕")
	encName := stdmime.QEncoding.Encode("utf-8", "Édouard Léon")

	msg := crlf(
		"From: "+encName+" <edouard@example.com>",
		"To: recipient@example.com",
		"Subject: "+encSubject,
		"Date: Mon, 01 Jan 2024 12:00:00 +0000",
		"Message-ID: <ew001@example.com>",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"body",
		"",
	)

	email, err := Parse([]byte(msg))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if email.Headers.Subject != "Réunion café ☕" {
		t.Errorf("Subject = %q, want %q", email.Headers.Subject, "Réunion café ☕")
	}
	if len(email.Headers.From) != 1 {
		t.Fatalf("expected 1 From address, got %d", len(email.Headers.From))
	}
	if email.Headers.From[0].Name != "Édouard Léon" {
		t.Errorf("From name = %q, want %q", email.Headers.From[0].Name, "Édouard Léon")
	}
	if email.Headers.From[0].Address != "edouard@example.com" {
		t.Errorf("From address = %q, want %q", email.Headers.From[0].Address, "edouard@example.com")
	}

	// The Raw header map must keep the still-encoded value (DKIM/AR logic reads it).
	if raw := email.Headers.Raw["Subject"]; len(raw) != 1 || raw[0] != encSubject {
		t.Errorf("Raw[Subject] = %v, want [%q]", raw, encSubject)
	}
}

// TestParse_NonUTF8CharsetBody verifies a non-UTF-8 (ISO-8859-1) body is
// transcoded to UTF-8.
func TestParse_NonUTF8CharsetBody(t *testing.T) {
	// "Café déjà" encoded in ISO-8859-1: é = 0xE9, à = 0xE0.
	latin1Body := "Caf\xe9 d\xe9j\xe0"

	msg := crlf(
		"From: sender@example.com",
		"To: recipient@example.com",
		"Subject: Latin1",
		"Content-Type: text/plain; charset=iso-8859-1",
		"Content-Transfer-Encoding: 8bit",
		"",
		latin1Body,
		"",
	)

	email, err := Parse([]byte(msg))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if email.Body.ContentType != "text/plain" {
		t.Errorf("Body ContentType = %q, want %q", email.Body.ContentType, "text/plain")
	}
	if !strings.Contains(email.Body.Content, "Café déjà") {
		t.Errorf("Body Content = %q, want it to contain the UTF-8 %q", email.Body.Content, "Café déjà")
	}
}

// TestParse_QuotedPrintableBody verifies quoted-printable transfer-encoding is
// decoded.
func TestParse_QuotedPrintableBody(t *testing.T) {
	// "Héllo = wörld" with é/ö as UTF-8 QP and a literal '=' as =3D.
	msg := crlf(
		"From: sender@example.com",
		"To: recipient@example.com",
		"Subject: QP",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"H=C3=A9llo =3D w=C3=B6rld",
		"",
	)

	email, err := Parse([]byte(msg))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if !strings.Contains(email.Body.Content, "Héllo = wörld") {
		t.Errorf("Body Content = %q, want it to contain %q", email.Body.Content, "Héllo = wörld")
	}
}

// TestParse_Base64Body verifies base64 transfer-encoding of a text body.
func TestParse_Base64Body(t *testing.T) {
	plain := "Base64-encoded body content."
	b64 := base64.StdEncoding.EncodeToString([]byte(plain))

	msg := crlf(
		"From: sender@example.com",
		"To: recipient@example.com",
		"Subject: B64",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: base64",
		"",
		b64,
		"",
	)

	email, err := Parse([]byte(msg))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if email.Body.Content != plain {
		t.Errorf("Body Content = %q, want %q", email.Body.Content, plain)
	}
}

// TestParse_NestedMultipartWithAttachmentAndInline exercises a realistic tree:
// multipart/mixed containing a multipart/alternative (text + html), an inline
// image referenced by Content-ID, and a base64 attachment.
func TestParse_NestedMultipartWithAttachmentAndInline(t *testing.T) {
	pngBytes := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x01, 0x02, 0x03}
	pdfBytes := []byte("%PDF-1.7 minimal pdf bytes")
	pngB64 := base64.StdEncoding.EncodeToString(pngBytes)
	pdfB64 := base64.StdEncoding.EncodeToString(pdfBytes)

	msg := crlf(
		"From: sender@example.com",
		"To: recipient@example.com",
		"Subject: Rich message",
		"Date: Tue, 02 Jan 2024 10:00:00 +0000",
		"Message-ID: <nested001@example.com>",
		"Content-Type: multipart/mixed; boundary=\"MIXED\"",
		"",
		"--MIXED",
		"Content-Type: multipart/alternative; boundary=\"ALT\"",
		"",
		"--ALT",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Plain body text.",
		"--ALT",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>HTML body text.</p>",
		"--ALT--",
		"--MIXED",
		"Content-Type: image/png",
		"Content-Disposition: inline; filename=\"logo.png\"",
		"Content-ID: <logo@example.com>",
		"Content-Transfer-Encoding: base64",
		"",
		pngB64,
		"--MIXED",
		"Content-Type: application/pdf; name=\"doc.pdf\"",
		"Content-Disposition: attachment; filename=\"doc.pdf\"",
		"Content-Transfer-Encoding: base64",
		"",
		pdfB64,
		"--MIXED--",
		"",
	)

	email, err := Parse([]byte(msg))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	// Top-level tree.
	if email.Body.ContentType != "multipart/mixed" {
		t.Errorf("Body ContentType = %q, want %q", email.Body.ContentType, "multipart/mixed")
	}
	if len(email.Body.Parts) != 1 {
		t.Fatalf("expected 1 top-level body part (the alternative subtree), got %d", len(email.Body.Parts))
	}

	// Nested multipart/alternative preserved as a subtree.
	alt := email.Body.Parts[0]
	if alt.ContentType != "multipart/alternative" {
		t.Errorf("nested part ContentType = %q, want %q", alt.ContentType, "multipart/alternative")
	}
	if len(alt.Parts) != 2 {
		t.Fatalf("expected 2 alternative parts, got %d", len(alt.Parts))
	}
	if alt.Parts[0].ContentType != "text/plain" || !strings.Contains(alt.Parts[0].Content, "Plain body text.") {
		t.Errorf("alt.Parts[0] = %+v, want text/plain with plain body", alt.Parts[0])
	}
	if alt.Parts[1].ContentType != "text/html" || !strings.Contains(alt.Parts[1].Content, "HTML body text.") {
		t.Errorf("alt.Parts[1] = %+v, want text/html with html body", alt.Parts[1])
	}

	// Inline image.
	if len(email.Inline) != 1 {
		t.Fatalf("expected 1 inline part, got %d", len(email.Inline))
	}
	inl := email.Inline[0]
	if inl.ContentType != "image/png" {
		t.Errorf("inline ContentType = %q, want %q", inl.ContentType, "image/png")
	}
	if inl.Disposition != "inline" {
		t.Errorf("inline Disposition = %q, want %q", inl.Disposition, "inline")
	}
	if inl.ContentID != "<logo@example.com>" {
		t.Errorf("inline ContentID = %q, want %q", inl.ContentID, "<logo@example.com>")
	}
	if inl.Filename != "logo.png" {
		t.Errorf("inline Filename = %q, want %q", inl.Filename, "logo.png")
	}
	if decoded, decErr := base64.StdEncoding.DecodeString(inl.Content); decErr != nil || string(decoded) != string(pngBytes) {
		t.Errorf("inline content did not round-trip: err=%v", decErr)
	}
	if inl.Size != int64(len(pngBytes)) {
		t.Errorf("inline Size = %d, want %d", inl.Size, len(pngBytes))
	}

	// Attachment.
	if len(email.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(email.Attachments))
	}
	att := email.Attachments[0]
	if att.ContentType != "application/pdf" {
		t.Errorf("attachment ContentType = %q, want %q", att.ContentType, "application/pdf")
	}
	if att.Disposition != "attachment" {
		t.Errorf("attachment Disposition = %q, want %q", att.Disposition, "attachment")
	}
	if att.Filename != "doc.pdf" {
		t.Errorf("attachment Filename = %q, want %q", att.Filename, "doc.pdf")
	}
	if decoded, decErr := base64.StdEncoding.DecodeString(att.Content); decErr != nil || string(decoded) != string(pdfBytes) {
		t.Errorf("attachment content did not round-trip: err=%v", decErr)
	}
	if att.Size != int64(len(pdfBytes)) {
		t.Errorf("attachment Size = %d, want %d", att.Size, len(pdfBytes))
	}
}

// TestParse_CalendarPartPreserved verifies that a text/calendar part inside a
// multipart message is both parsed into CalendarEvents and preserved as a raw
// body part (the wiring into the existing iCalendar parser).
func TestParse_CalendarPartPreserved(t *testing.T) {
	ics := crlf(
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"METHOD:REQUEST",
		"BEGIN:VEVENT",
		"UID:invite-1@example.com",
		"DTSTART:20240601T100000Z",
		"DTEND:20240601T110000Z",
		"SUMMARY:Planning",
		"ORGANIZER:mailto:org@example.com",
		"END:VEVENT",
		"END:VCALENDAR",
	)

	msg := crlf(
		"From: org@example.com",
		"To: attendee@example.com",
		"Subject: Invitation",
		"Content-Type: multipart/mixed; boundary=\"CAL\"",
		"",
		"--CAL",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"You are invited.",
		"--CAL",
		"Content-Type: text/calendar; method=REQUEST; charset=utf-8",
		"",
		ics,
		"--CAL--",
		"",
	)

	email, err := Parse([]byte(msg))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	if len(email.CalendarEvents) != 1 {
		t.Fatalf("expected 1 calendar event, got %d", len(email.CalendarEvents))
	}
	if email.CalendarEvents[0].UID != "invite-1@example.com" {
		t.Errorf("event UID = %q, want %q", email.CalendarEvents[0].UID, "invite-1@example.com")
	}
	if email.CalendarEvents[0].Summary != "Planning" {
		t.Errorf("event Summary = %q, want %q", email.CalendarEvents[0].Summary, "Planning")
	}

	// The raw .ics must remain available as a body part.
	foundICS := false
	for _, p := range email.Body.Parts {
		if p.ContentType == "text/calendar" && strings.Contains(p.Content, "BEGIN:VCALENDAR") {
			foundICS = true
		}
	}
	if !foundICS {
		t.Error("expected the text/calendar part to be preserved in Body.Parts")
	}
}
