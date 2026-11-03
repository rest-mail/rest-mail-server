package message

import (
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

func TestBuildAddress_EmptyEmail(t *testing.T) {
	if result := buildAddress("John", ""); result != "NIL" {
		t.Errorf("got %q, want %q", result, "NIL")
	}
}

func TestBuildAddress_FullAddress(t *testing.T) {
	result := buildAddress("John Doe", "john@example.com")
	if !strings.Contains(result, `"John Doe"`) {
		t.Errorf("result %q missing name", result)
	}
	if !strings.Contains(result, `"john"`) {
		t.Errorf("result %q missing user part", result)
	}
	if !strings.Contains(result, `"example.com"`) {
		t.Errorf("result %q missing host part", result)
	}
	if !strings.HasPrefix(result, "((") || !strings.HasSuffix(result, "))") {
		t.Errorf("result %q should be wrapped in double parens", result)
	}
}

func TestBuildAddress_NoAtSign(t *testing.T) {
	result := buildAddress("Local", "localonly")
	if !strings.Contains(result, `"localonly"`) {
		t.Errorf("result %q missing user part", result)
	}
	if !strings.Contains(result, "NIL") {
		t.Errorf("result %q should have NIL for empty host", result)
	}
}

func TestBuildAddress_EmptyName(t *testing.T) {
	result := buildAddress("", "user@host.com")
	if !strings.Contains(result, "NIL NIL") {
		t.Errorf("result %q should have NIL for empty name", result)
	}
}

func TestBuildEnvelope_Basic(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := imap.Message{
		Subject: "Test Subject",
		From:    imap.Address{Name: "Alice", Email: "alice@example.com"},
		Date:    fixedTime,
	}
	result := BuildEnvelope(msg, "", false)

	if !strings.HasPrefix(result, "(") || !strings.HasSuffix(result, ")") {
		t.Errorf("envelope should be wrapped in parens: %s", result)
	}
	if !strings.Contains(result, "Mon, 15 Jan 2024") {
		t.Errorf("envelope missing date: %s", result)
	}
	if !strings.Contains(result, `"Test Subject"`) {
		t.Errorf("envelope missing subject: %s", result)
	}
	if !strings.Contains(result, "example.com") {
		t.Errorf("envelope missing sender host: %s", result)
	}
	if !strings.Contains(result, "Alice") {
		t.Errorf("envelope missing sender name: %s", result)
	}
	if !strings.HasSuffix(result, "NIL NIL NIL NIL NIL)") {
		t.Errorf("envelope should end with 5 NILs: %s", result)
	}
}

func TestBuildEnvelope_EmptySubject(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := imap.Message{
		Subject: "",
		From:    imap.Address{Name: "Bob", Email: "bob@test.com"},
		Date:    fixedTime,
	}
	result := BuildEnvelope(msg, "", false)
	if !strings.Contains(result, " NIL ") {
		t.Errorf("envelope should have NIL for empty subject: %s", result)
	}
}

// Direct unit test pinning the full ten-field structure and its ordering when the
// raw message is available (RFC 3501 §7.4.2: date subject from sender reply-to to
// cc bcc in-reply-to message-id).
func TestBuildEnvelope_FromHeaders(t *testing.T) {
	msg := imap.Message{
		Subject: "seed subject",
		From:    imap.Address{Name: "Seed", Email: "seed@example.com"},
		Date:    time.Date(2025, 3, 15, 10, 0, 0, 0, time.UTC),
	}
	env := BuildEnvelope(msg, envelopeRaw, true)

	if !parensBalanced(env) {
		t.Fatalf("envelope has unbalanced parens: %s", env)
	}
	// Sender and Reply-To come from their own headers here (distinct from From).
	for _, want := range []string{
		`"Mon, 01 Jan 2024 09:00:00 +0000"`,           // date (header, not arrival)
		`"Hello there"`,                               // subject (header, not summary)
		`"Alice" NIL "alice" "example.com"`,           // from
		`"Secretary" NIL "sec" "example.com"`,         // sender
		`"Alice Replies" NIL "replies" "example.com"`, // reply-to
		`"Bob" NIL "bob" "example.org"`,               // to[0]
		`"Dave" NIL "dave" "example.com"`,             // cc
		`"Eve" NIL "eve" "example.com"`,               // bcc
		`"<parent-123@example.com>"`,                  // in-reply-to
		`"<msg-456@example.com>"`,                     // message-id
	} {
		if !strings.Contains(env, want) {
			t.Errorf("envelope missing %s\n  got %s", want, env)
		}
	}
}

// When no raw message is available (rawOK=false), BuildEnvelope degrades to the
// Message model exactly as before: from/sender/reply-to from Message.From, To from
// Message.To, and cc/bcc/in-reply-to/message-id NIL. This pins the fallback
// legacy helper callers use without raw bytes; FETCH itself reports load errors.
func TestBuildEnvelope_NoRawFallsBackToModel(t *testing.T) {
	msg := imap.Message{
		Subject: "Hi",
		From:    imap.Address{Name: "Alice", Email: "alice@example.com"},
		To:      "Bob <bob@example.org>",
		Date:    time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
	}
	env := BuildEnvelope(msg, "", false)
	if !strings.Contains(env, `"bob" "example.org"`) {
		t.Errorf("To should come from Message.To: %s", env)
	}
	if !strings.HasSuffix(env, "NIL NIL NIL NIL)") {
		t.Errorf("cc/bcc/in-reply-to/message-id should be NIL without raw: %s", env)
	}
	if !strings.Contains(env, "Mon, 15 Jan 2024") {
		t.Errorf("date should come from Message.Date without raw: %s", env)
	}
}

// BuildEnvelope must populate the To field from Message.To (the 6th envelope
// field) rather than hardcoding it NIL. cc/bcc/in-reply-to/message-id have no
// source in the Message model and stay NIL (the 4 trailing fields).
func TestBuildEnvelope_ToAddress(t *testing.T) {
	msg := imap.Message{
		Subject: "Hi",
		From:    imap.Address{Name: "Alice", Email: "alice@example.com"},
		To:      "Bob <bob@example.org>",
		Date:    time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
	}
	result := BuildEnvelope(msg, "", false)

	if !parensBalanced(result) {
		t.Fatalf("envelope has unbalanced parens: %s", result)
	}
	for _, want := range []string{`"Bob"`, `"bob"`, `"example.org"`} {
		if !strings.Contains(result, want) {
			t.Errorf("envelope To not populated with %s: %s", want, result)
		}
	}
	// To is present, so the response must end in exactly 4 NILs
	// (cc bcc in-reply-to message-id), not 5.
	if !strings.HasSuffix(result, "NIL NIL NIL NIL)") {
		t.Errorf("envelope tail should be 4 NILs: %s", result)
	}
	if strings.HasSuffix(result, "NIL NIL NIL NIL NIL)") {
		t.Errorf("To field must not be NIL when Message.To is set: %s", result)
	}
}

// buildAddressList must render every address in a multi-recipient header as its
// own address structure within a single outer list.
func TestBuildAddressList_Multiple(t *testing.T) {
	got := buildAddressList("Bob <bob@example.org>, carol@example.com")
	want := `(("Bob" NIL "bob" "example.org")(NIL NIL "carol" "example.com"))`
	if got != want {
		t.Errorf("buildAddressList = %q, want %q", got, want)
	}
	if buildAddressList("") != "NIL" {
		t.Errorf("empty address list must be NIL")
	}
	if buildAddressList("not a valid @@ address") != "NIL" {
		t.Errorf("unparseable address list must be NIL")
	}
}
