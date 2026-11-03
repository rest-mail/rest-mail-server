package message

import (
	"strings"
	"testing"
)

func TestFilterHeaders_Basic(t *testing.T) {
	raw := "From: alice@example.com\r\nSubject: Test\r\nDate: Mon, 01 Jan 2024\r\n\r\nBody text"
	result := filterHeaders(raw, []string{"From", "Subject"})

	if !strings.Contains(result, "From: alice@example.com\r\n") {
		t.Errorf("missing From header in: %q", result)
	}
	if !strings.Contains(result, "Subject: Test\r\n") {
		t.Errorf("missing Subject header in: %q", result)
	}
	if strings.Contains(result, "Date:") {
		t.Errorf("should not contain Date header in: %q", result)
	}
	if !strings.HasSuffix(result, "\r\n") {
		t.Errorf("should end with CRLF: %q", result)
	}
}

func TestFilterHeaders_CaseInsensitive(t *testing.T) {
	raw := "From: alice@example.com\r\nSUBJECT: Test\r\n\r\nBody"
	result := filterHeaders(raw, []string{"subject"})
	if !strings.Contains(result, "SUBJECT: Test\r\n") {
		t.Errorf("case-insensitive match failed in: %q", result)
	}
}

func TestFilterHeaders_NoMatch(t *testing.T) {
	raw := "From: alice@example.com\r\n\r\nBody"
	result := filterHeaders(raw, []string{"Subject"})
	if result != "\r\n" {
		t.Errorf("got %q, want just terminating CRLF", result)
	}
}

func TestFilterHeaders_NoBodySeparator(t *testing.T) {
	raw := "From: alice@example.com\r\nSubject: Test"
	result := filterHeaders(raw, []string{"From"})
	if !strings.Contains(result, "From: alice@example.com\r\n") {
		t.Errorf("missing From header in: %q", result)
	}
}

func TestFilterHeaders_EmptyFields(t *testing.T) {
	raw := "From: alice@example.com\r\nSubject: Test\r\n\r\nBody"
	result := filterHeaders(raw, []string{})
	if result != "\r\n" {
		t.Errorf("got %q, want just terminating CRLF for no requested fields", result)
	}
}

func TestFilterHeaders_AllFields(t *testing.T) {
	raw := "From: alice@example.com\r\nSubject: Test\r\n\r\nBody"
	result := filterHeaders(raw, []string{"From", "Subject"})
	if !strings.Contains(result, "From: alice@example.com") {
		t.Errorf("missing From in: %q", result)
	}
	if !strings.Contains(result, "Subject: Test") {
		t.Errorf("missing Subject in: %q", result)
	}
}

// ---------------------------------------------------------------------------
// BODY[HEADER.FIELDS.NOT (...)] exclusion and folded-header handling (issue #19)
// ---------------------------------------------------------------------------

// HEADER.FIELDS.NOT (RFC 3501 §6.4.5) returns every header EXCEPT the listed
// ones. Previously it was matched by the HEADER.FIELDS prefix and returned only
// the listed header — the exact inverse — leaking a header the client asked to
// omit.
func TestBodySection_HeaderFieldsNot_ExcludesListed(t *testing.T) {
	raw := "From: alice@example.com\r\nSubject: Hello\r\nDate: Mon, 01 Jan 2024\r\n\r\nBody"
	load := func() (string, bool) { return raw, true }

	name, payload, _, ok := BodySection("BODY[HEADER.FIELDS.NOT (Subject)]", load)
	if !ok {
		t.Fatalf("BodySection ok = false")
	}
	if name != "BODY[HEADER.FIELDS.NOT (Subject)]" {
		t.Errorf("response item name = %q, want BODY[HEADER.FIELDS.NOT (Subject)]", name)
	}
	if strings.Contains(payload, "Subject:") {
		t.Errorf(".NOT (Subject) must EXCLUDE the Subject header, but payload contained it: %q", payload)
	}
	if !strings.Contains(payload, "From: alice@example.com") {
		t.Errorf(".NOT (Subject) must include From; got: %q", payload)
	}
	if !strings.Contains(payload, "Date: Mon, 01 Jan 2024") {
		t.Errorf(".NOT (Subject) must include Date; got: %q", payload)
	}
}

// HEADER.FIELDS (the non-NOT form) must keep returning ONLY the listed header —
// the fix for .NOT must not disturb this.
func TestBodySection_HeaderFields_OnlyListed(t *testing.T) {
	raw := "From: alice@example.com\r\nSubject: Hello\r\nDate: Mon, 01 Jan 2024\r\n\r\nBody"
	load := func() (string, bool) { return raw, true }

	name, payload, _, ok := BodySection("BODY[HEADER.FIELDS (Subject)]", load)
	if !ok {
		t.Fatalf("BodySection ok = false")
	}
	if name != "BODY[HEADER.FIELDS (Subject)]" {
		t.Errorf("response item name = %q, want BODY[HEADER.FIELDS (Subject)]", name)
	}
	if !strings.Contains(payload, "Subject: Hello") {
		t.Errorf("HEADER.FIELDS (Subject) must include Subject; got: %q", payload)
	}
	if strings.Contains(payload, "From:") || strings.Contains(payload, "Date:") {
		t.Errorf("HEADER.FIELDS (Subject) must return ONLY Subject; got: %q", payload)
	}
}

// A folded header value (RFC 5322 §2.2.3 — continuation lines begin with SP/HT)
// must be returned in full, not truncated to its first line.
func TestFilterHeaders_FoldedValueIncluded(t *testing.T) {
	raw := "From: alice@example.com\r\n" +
		"Subject: This is a very long subject line that has been\r\n" +
		" folded onto a second continuation line\r\n" +
		"Date: Mon, 01 Jan 2024\r\n\r\nBody"

	result := filterHeaders(raw, []string{"Subject"})

	if !strings.Contains(result, "Subject: This is a very long subject line that has been") {
		t.Errorf("missing first Subject line; got: %q", result)
	}
	if !strings.Contains(result, " folded onto a second continuation line") {
		t.Errorf("folded continuation line dropped (truncated header); got: %q", result)
	}
	// The excluded headers must still be excluded.
	if strings.Contains(result, "From:") || strings.Contains(result, "Date:") {
		t.Errorf("HEADER.FIELDS (Subject) leaked another header; got: %q", result)
	}
}

// Under .NOT, a non-excluded header that is folded must be kept in full, and a
// folded EXCLUDED header must be dropped entirely (including its continuations).
func TestBodySection_HeaderFieldsNot_FoldedHandling(t *testing.T) {
	raw := "Subject: line one\r\n" +
		" continued line two\r\n" +
		"From: alice@example.com\r\n" +
		" name continuation\r\n" +
		"\r\nBody"
	load := func() (string, bool) { return raw, true }

	_, payload, _, ok := BodySection("BODY[HEADER.FIELDS.NOT (From)]", load)
	if !ok {
		t.Fatalf("BodySection ok = false")
	}
	if !strings.Contains(payload, "Subject: line one") || !strings.Contains(payload, " continued line two") {
		t.Errorf(".NOT (From) must include the full folded Subject; got: %q", payload)
	}
	if strings.Contains(payload, "From:") || strings.Contains(payload, " name continuation") {
		t.Errorf(".NOT (From) must drop From and its folded continuation; got: %q", payload)
	}
}
