package server

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/go-pop3/pkg/pop3"
)

// These tests pin the RETR/TOP wire framing (RFC 1939 §5.1/§7/§11): the message
// octets are emitted exactly — headers, the single blank line separating headers
// from body, then body — followed by the ".CRLF" terminator, with no extra blank
// line inserted. A well-formed message ends in CRLF; naive strings.Split on
// "\r\n" yields a trailing "" (the last line's terminator) that must not be
// transmitted as a separate empty line.

// TestPOP3_RetrNoSpuriousBlankLine proves RETR delivers exactly the stored
// octets, with no trailing blank line before "." and a delivered size equal to
// the advertised octet count.
func TestPOP3_RetrNoSpuriousBlankLine(t *testing.T) {
	const raw = "From: a@example.com\r\n" +
		"Subject: Hi\r\n" +
		"\r\n" +
		"Body line 1\r\n" +
		"Body line 2\r\n"

	m := newMockBackend()
	m.seed("1", len(raw), raw)
	h := newPOP3Harness(t, m)
	h.login()

	header := h.cmd("RETR 1")
	if want := "+OK " + strconv.Itoa(len(raw)) + " octets"; header != want {
		t.Fatalf("RETR header = %q, want %q", header, want)
	}

	body := h.readDotBody()

	// Reconstruct the transmitted message: every line was CRLF-terminated.
	var wire string
	if len(body) > 0 {
		wire = strings.Join(body, "\r\n") + "\r\n"
	}
	if wire != raw {
		t.Errorf("RETR delivered %q, want %q (spurious blank line?)", wire, raw)
	}
	// Delivered octets must equal the advertised count (RFC 1939 §11).
	if len(wire) != len(raw) {
		t.Errorf("RETR delivered %d octets, advertised %d", len(wire), len(raw))
	}
	// The final content line must not be an empty (spurious) line.
	if n := len(body); n > 0 && body[n-1] == "" {
		t.Errorf("RETR emitted a trailing blank line before '.': %v", body)
	}
}

// TestPOP3_RetrEmptyMessage proves a 0-octet message delivers an empty body
// (just the "." terminator), not a phantom blank line.
func TestPOP3_RetrEmptyMessage(t *testing.T) {
	m := newMockBackend()
	m.mbox.msgs = append(m.mbox.msgs, pop3.Message{UID: "1", Size: 0})
	m.mbox.raws["1"] = []byte{}
	h := newPOP3Harness(t, m)
	h.login()

	if got := h.cmd("RETR 1"); got != "+OK 0 octets" {
		t.Fatalf("RETR header = %q, want %q", got, "+OK 0 octets")
	}
	if body := h.readDotBody(); len(body) != 0 {
		t.Errorf("RETR of empty message delivered %v, want no lines", body)
	}
}

// TestPOP3_TopNoSpuriousBlankLine proves TOP with a line count exceeding the
// body length delivers the whole message exactly, with no trailing blank line.
func TestPOP3_TopNoSpuriousBlankLine(t *testing.T) {
	const raw = "From: a@example.com\r\n" +
		"Subject: Hi\r\n" +
		"\r\n" +
		"Body line 1\r\n" +
		"Body line 2\r\n"

	m := newMockBackend()
	m.seed("1", len(raw), raw)
	h := newPOP3Harness(t, m)
	h.login()

	if got := h.cmd("TOP 1 100"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("TOP header = %q", got)
	}
	body := h.readDotBody()

	var wire string
	if len(body) > 0 {
		wire = strings.Join(body, "\r\n") + "\r\n"
	}
	if wire != raw {
		t.Errorf("TOP delivered %q, want %q (spurious blank line?)", wire, raw)
	}
	if n := len(body); n > 0 && body[n-1] == "" {
		t.Errorf("TOP emitted a trailing blank line before '.': %v", body)
	}
}

// TestPOP3_TopHeadersOnlyNoPhantomLine proves TOP of a headers-only message
// (no body, no separating blank line stored) emits the headers followed by the
// single header/body separator, and not the phantom body line produced by
// splitting the headers' trailing CRLF.
func TestPOP3_TopHeadersOnlyNoPhantomLine(t *testing.T) {
	const raw = "From: a@example.com\r\n" +
		"Subject: Hi\r\n"

	m := newMockBackend()
	m.seed("1", len(raw), raw)
	h := newPOP3Harness(t, m)
	h.login()

	if got := h.cmd("TOP 1 0"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("TOP header = %q", got)
	}
	body := h.readDotBody()

	want := []string{"From: a@example.com", "Subject: Hi", ""}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("TOP headers-only body = %v, want %v (phantom line?)", body, want)
	}
}
