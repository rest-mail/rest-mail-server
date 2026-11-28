package server

import (
	"strings"
	"testing"

	"github.com/rest-mail/go-pop3/pkg/pop3/internal/wire"
)

// TestValidUID exercises the RFC 1939 §7 unique-id grammar directly: 1 to 70
// characters, each byte in 0x21..0x7E inclusive.
func TestValidUID(t *testing.T) {
	cases := []struct {
		name string
		uid  string
		want bool
	}{
		{"simple", "abc123", true},
		{"low bound !", "!", true},
		{"high bound ~", "~", true},
		{"boundary chars", "!~", true},
		{"exactly 70", strings.Repeat("a", 70), true},
		{"empty", "", false},
		{"71 chars", strings.Repeat("a", 71), false},
		{"space", "ab cd", false},
		{"leading space", " abc", false},
		{"CR", "ab\rcd", false},
		{"LF", "ab\ncd", false},
		{"CRLF", "ab\r\ncd", false},
		{"tab", "ab\tcd", false},
		{"NUL", "ab\x00cd", false},
		{"DEL 0x7F", "ab\x7fcd", false},
		{"high bit", "ab\x80cd", false},
		{"utf8 é", "abcé", false},
	}
	for _, c := range cases {
		if got := wire.ValidUID(c.uid); got != c.want {
			t.Errorf("validUID(%q) = %v, want %v", c.uid, got, c.want)
		}
	}
}

// TestPOP3_UidlSingleRejectsInvalid asserts that the single-message form
// (UIDL n) answers -ERR when the backend supplied a non-conforming unique-id,
// rather than emitting it verbatim. On the pre-fix engine the id is
// interpolated straight into the "+OK n uid" line, which either splits the pair
// (a space) or injects a new protocol line (CR/LF).
func TestPOP3_UidlSingleRejectsInvalid(t *testing.T) {
	bad := []struct {
		name string
		uid  string
	}{
		{"space", "ab cd"},
		{"cr", "ab\rcd"},
		{"lf", "ab\ncd"},
		{"empty", ""},
		{"toolong", strings.Repeat("x", 71)},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			m := newMockBackend()
			m.seed(c.uid, 100, rawMsg5)
			h := newPOP3Harness(t, m)
			h.login()

			if got := h.cmd("UIDL 1"); !strings.HasPrefix(got, "-ERR") {
				t.Errorf("UIDL 1 with invalid uid %q = %q, want -ERR...", c.uid, got)
			}
			// The session must not be desynchronized by a rejected id: the next
			// command still gets its own reply and not leftover injected bytes.
			if got := h.cmd("NOOP"); !strings.HasPrefix(got, "+OK") {
				t.Fatalf("session desynced after rejected UIDL: NOOP = %q", got)
			}
		})
	}
}

// TestPOP3_UidlListRejectsInvalid asserts the all-messages form refuses the
// whole command with -ERR (before starting the dot-terminated block) when any
// listed message carries a malformed unique-id. Committing to the multi-line
// response and then emitting a malformed line mid-list would corrupt the
// framing for the client.
func TestPOP3_UidlListRejectsInvalid(t *testing.T) {
	m := newMockBackend()
	m.seed("5", 100, rawMsg5)
	m.seed("bad id", 200, rawMsg9) // space in the middle id
	m.seed("20", 300, rawMsg20)
	h := newPOP3Harness(t, m)
	h.login()

	if got := h.cmd("UIDL"); !strings.HasPrefix(got, "-ERR") {
		t.Errorf("UIDL (full list) with an invalid id = %q, want -ERR...", got)
	}
	// No dot-terminated block should have been emitted; the next command must
	// get its own reply.
	if got := h.cmd("NOOP"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("session desynced after rejected UIDL list: NOOP = %q", got)
	}
}

// TestPOP3_UidlListRejectsDuplicate asserts the all-messages form refuses the
// command when two messages share a unique-id: RFC 1939 §7 requires ids unique
// within a maildrop, and a duplicate breaks leave-on-server de-duplication.
func TestPOP3_UidlListRejectsDuplicate(t *testing.T) {
	m := newMockBackend()
	m.seed("dup", 100, rawMsg5)
	m.seed("dup", 200, rawMsg9)
	h := newPOP3Harness(t, m)
	h.login()

	if got := h.cmd("UIDL"); !strings.HasPrefix(got, "-ERR") {
		t.Errorf("UIDL (full list) with duplicate ids = %q, want -ERR...", got)
	}
	if got := h.cmd("NOOP"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("session desynced after rejected UIDL list: NOOP = %q", got)
	}
}

// TestPOP3_UidlValidStillWorks guards against over-rejection: conforming
// unique-ids — including the RFC boundary characters and a full 70-byte id —
// must continue to be served by both UIDL forms.
func TestPOP3_UidlValidStillWorks(t *testing.T) {
	long := strings.Repeat("A", 70)
	m := newMockBackend()
	m.seed("!", 100, rawMsg5)
	m.seed("~", 200, rawMsg9)
	m.seed(long, 300, rawMsg20)
	h := newPOP3Harness(t, m)
	h.login()

	// Single-message form.
	if got := h.cmd("UIDL 1"); got != "+OK 1 !" {
		t.Errorf("UIDL 1 = %q, want %q", got, "+OK 1 !")
	}
	if got := h.cmd("UIDL 3"); got != "+OK 3 "+long {
		t.Errorf("UIDL 3 = %q, want %q", got, "+OK 3 "+long)
	}

	// Full-list form.
	if got := h.cmd("UIDL"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("UIDL header = %q", got)
	}
	body := h.readDotBody()
	want := []string{"1 !", "2 ~", "3 " + long}
	if len(body) != len(want) {
		t.Fatalf("UIDL body = %v, want %v", body, want)
	}
	for i := range want {
		if body[i] != want[i] {
			t.Errorf("UIDL body[%d] = %q, want %q", i, body[i], want[i])
		}
	}
}
