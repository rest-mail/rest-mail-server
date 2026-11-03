package server

import (
	"strings"
	"testing"
)

// The ENVELOPE recipient and reference fields — to, cc, bcc, in-reply-to and
// message-id — must be parsed from the message's own headers rather than being
// hard-coded NIL (RFC 3501 §7.4.2, item 1). The envelope date must come from the
// message's Date: header, not the arrival time that backs INTERNALDATE (§7.4.2,
// item 2).

// envelopeRaw is a message whose headers carry every field the ENVELOPE reports,
// with a Date: header deliberately different from the arrival time the mock seeds
// (2025-03-15) so the two dates can be told apart.
const envelopeRaw = "From: Alice <alice@example.com>\r\n" +
	"Sender: Secretary <sec@example.com>\r\n" +
	"Reply-To: Alice Replies <replies@example.com>\r\n" +
	"To: Bob <bob@example.org>, carol@example.net\r\n" +
	"Cc: Dave <dave@example.com>\r\n" +
	"Bcc: Eve <eve@example.com>\r\n" +
	"In-Reply-To: <parent-123@example.com>\r\n" +
	"Message-ID: <msg-456@example.com>\r\n" +
	"Date: Mon, 01 Jan 2024 09:00:00 +0000\r\n" +
	"Subject: Hello there\r\n" +
	"\r\n" +
	"body text\r\n"

// fetchEnvelope logs in, selects INBOX and FETCHes (ENVELOPE INTERNALDATE) for
// sequence 1, returning the single untagged FETCH line.
func fetchEnvelope(t *testing.T, m *mockBackend) string {
	t.Helper()
	h := newIMAPHarness(t, m)
	h.login("a1")
	h.selectInbox("a2")
	untagged, status := h.command("a3", "FETCH 1 (ENVELOPE INTERNALDATE)")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH ENVELOPE status = %q, want OK", status)
	}
	for _, l := range untagged {
		if strings.Contains(l, "FETCH") {
			return l
		}
	}
	t.Fatalf("no untagged FETCH line in %v", untagged)
	return ""
}

// Item 1: To, Cc, In-Reply-To and Message-ID are populated from the headers, each
// as a correctly-structured, non-NIL envelope field.
func TestIMAP_Envelope_RecipientAndReferenceFields(t *testing.T) {
	m := newMockBackend()
	m.seed("INBOX", 1, len(envelopeRaw), envelopeRaw)
	line := fetchEnvelope(t, m)

	// To: two addresses — "Bob <bob@example.org>" and "carol@example.net".
	for _, want := range []string{`"Bob" NIL "bob" "example.org"`, `NIL NIL "carol" "example.net"`} {
		if !strings.Contains(line, want) {
			t.Errorf("ENVELOPE To missing address %s\n  got %s", want, line)
		}
	}
	// Cc: Dave <dave@example.com>.
	if want := `"Dave" NIL "dave" "example.com"`; !strings.Contains(line, want) {
		t.Errorf("ENVELOPE Cc missing %s\n  got %s", want, line)
	}
	// In-Reply-To and Message-ID are quoted verbatim (angle brackets kept).
	for _, want := range []string{`"<parent-123@example.com>"`, `"<msg-456@example.com>"`} {
		if !strings.Contains(line, want) {
			t.Errorf("ENVELOPE reference field missing %s\n  got %s", want, line)
		}
	}
	// The tail must NOT be the all-NIL "NIL NIL NIL NIL)" that the pre-fix engine
	// emitted for to/cc/in-reply-to/message-id.
	if strings.Contains(line, "NIL NIL NIL NIL)") {
		t.Errorf("ENVELOPE recipient/reference fields still NIL: %s", line)
	}
}

// Item 1: Bcc, when present in the stored message, is populated too.
func TestIMAP_Envelope_BccPopulatedWhenPresent(t *testing.T) {
	m := newMockBackend()
	m.seed("INBOX", 1, len(envelopeRaw), envelopeRaw)
	line := fetchEnvelope(t, m)
	if want := `"Eve" NIL "eve" "example.com"`; !strings.Contains(line, want) {
		t.Errorf("ENVELOPE Bcc missing %s\n  got %s", want, line)
	}
}

// Item 1: an absent Bcc header yields NIL (the common case for stored mail).
func TestIMAP_Envelope_BccNilWhenAbsent(t *testing.T) {
	raw := "From: Alice <alice@example.com>\r\n" +
		"To: Bob <bob@example.org>\r\n" +
		"Date: Mon, 01 Jan 2024 09:00:00 +0000\r\n" +
		"Subject: Hi\r\n\r\nbody\r\n"
	m := newMockBackend()
	m.seed("INBOX", 1, len(raw), raw)
	line := fetchEnvelope(t, m)
	// No Bcc header, no Cc, no In-Reply-To, no Message-ID: those four fields are
	// NIL, so the envelope ends "NIL NIL NIL NIL)" (cc bcc in-reply-to message-id).
	if !strings.Contains(line, "NIL NIL NIL NIL)") {
		t.Errorf("absent cc/bcc/in-reply-to/message-id should be NIL: %s", line)
	}
	// But To IS present, so it must not be NIL.
	if !strings.Contains(line, `"bob" "example.org"`) {
		t.Errorf("ENVELOPE To should be populated: %s", line)
	}
}

// Item 2: the ENVELOPE date is the Date: header; INTERNALDATE stays the arrival
// time (2025-03-15, seeded by the mock). The two must differ in the response.
func TestIMAP_Envelope_DateFromHeaderNotArrival(t *testing.T) {
	m := newMockBackend()
	m.seed("INBOX", 1, len(envelopeRaw), envelopeRaw)
	line := fetchEnvelope(t, m)

	// ENVELOPE date comes from "Date: Mon, 01 Jan 2024 09:00:00 +0000".
	if want := `"Mon, 01 Jan 2024 09:00:00 +0000"`; !strings.Contains(line, want) {
		t.Errorf("ENVELOPE date should be the Date: header %s\n  got %s", want, line)
	}
	// INTERNALDATE stays the arrival time the mock seeds (15-Mar-2025 10:00:00).
	if want := `INTERNALDATE "15-Mar-2025 10:00:00 +0000"`; !strings.Contains(line, want) {
		t.Errorf("INTERNALDATE should be the arrival time %s\n  got %s", want, line)
	}
	// The arrival date must NOT appear inside the ENVELOPE as the envelope date.
	if strings.Contains(line, `"Sat, 15 Mar 2025 10:00:00 +0000"`) {
		t.Errorf("ENVELOPE date wrongly used the arrival time: %s", line)
	}
}

// A missing Date header is NIL, rather than a fabricated arrival timestamp.
func TestIMAP_Envelope_MissingDateIsNil(t *testing.T) {
	raw := "From: Alice <alice@example.com>\r\n" +
		"To: Bob <bob@example.org>\r\n" +
		"Subject: No date header\r\n\r\nbody\r\n"
	m := newMockBackend()
	m.seed("INBOX", 1, len(raw), raw)
	line := fetchEnvelope(t, m)
	if !strings.Contains(line, `ENVELOPE (NIL "No date header"`) {
		t.Errorf("missing envelope date must be NIL: %s", line)
	}
}
