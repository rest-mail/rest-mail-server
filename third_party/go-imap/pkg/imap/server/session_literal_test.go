package server

import (
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// expectContinuation reads one response line and fails unless it is a command
// continuation request ("+ ..."), the reply the server must send before a
// synchronizing literal's octets are transmitted (RFC 3501 §4.3).
func expectContinuation(t *testing.T, h *imapHarness, what string) {
	t.Helper()
	line := h.readLine()
	if !strings.HasPrefix(line, "+") {
		t.Fatalf("%s: server reply = %q, want a \"+\" continuation request", what, line)
	}
}

// TestLiteral_LoginSynchronizing drives LOGIN with a synchronizing literal for
// BOTH arguments — the canonical case from issue #17:
//
//	a1 LOGIN {5}\r\nadmin {8}\r\npassword\r\n
//
// Each {n} must draw a "+" continuation, its octets must be read as the argument
// value, and the command must then authenticate. On the pre-fix code LOGIN did
// not understand a literal, so no continuation was sent and the octet line was
// misparsed as a fresh command — the desync this issue reports. RED there:
// expectContinuation fails because the first reply is a tagged BAD, not "+".
func TestLiteral_LoginSynchronizing(t *testing.T) {
	mock := newMockBackend()
	mock.user = "admin"
	mock.pass = "password"
	h := newIMAPHarnessWith(t, mock, mock.user, mock.pass)

	h.send("a1 LOGIN {5}")
	expectContinuation(t, h, "LOGIN username literal")
	h.send("admin {8}")
	expectContinuation(t, h, "LOGIN password literal")
	h.send("password")

	status := h.readLine()
	mustContain(t, status, "a1 ", "LOGIN response tag")
	mustContain(t, status, " OK", "LOGIN via synchronizing literals")
}

// TestLiteral_SelectMailbox drives SELECT with a synchronizing literal for the
// mailbox name — proving the generalized literal handling reaches a second,
// authenticated-state command (not just LOGIN). The literal is the terminal
// argument (nothing follows it on the command), which the reader must also
// handle. RED on the pre-fix code: SELECT never sent "+", so the "Work" octets
// desynced the parser.
func TestLiteral_SelectMailbox(t *testing.T) {
	mock := newMockBackend()
	mock.mbox.folders = append(mock.mbox.folders, imap.Folder{Name: "Work"})
	h := newIMAPHarness(t, mock)
	h.login("a1")

	h.send("a2 SELECT {4}")
	expectContinuation(t, h, "SELECT mailbox literal")
	h.send("Work")

	// Drain untagged responses up to the tagged completion line.
	var status string
	for {
		line := h.readLine()
		if strings.HasPrefix(line, "a2 ") {
			status = line
			break
		}
	}
	mustContain(t, status, " OK", "SELECT via synchronizing literal")
}

// TestLiteral_CreateMailbox drives CREATE with a synchronizing literal for the
// mailbox name, a third command through the generalized path.
func TestLiteral_CreateMailbox(t *testing.T) {
	mock := newMockBackend()
	h := newIMAPHarness(t, mock)
	h.login("a1")

	h.send("a2 CREATE {7}")
	expectContinuation(t, h, "CREATE mailbox literal")
	h.send("Archive")

	status := h.readLine()
	mustContain(t, status, "a2 ", "CREATE response tag")
	mustContain(t, status, " OK", "CREATE via synchronizing literal")
}

// TestLiteral_PreservesSpecialChars proves the literal octets are delivered
// verbatim — a value containing a space AND a double-quote survives intact.
// These are exactly the bytes a quoted-string argument could not carry without
// escaping, so this guards that the reader re-splices the literal without
// mis-terminating on the embedded quote. The password is matched byte-for-byte
// by the backend, so LOGIN only succeeds if `a "b` arrived unchanged.
func TestLiteral_PreservesSpecialChars(t *testing.T) {
	mock := newMockBackend()
	mock.user = "u"
	mock.pass = `a "b` // space + double-quote: 4 octets
	h := newIMAPHarnessWith(t, mock, mock.user, mock.pass)

	h.send("a1 LOGIN {1}")
	expectContinuation(t, h, "LOGIN username literal")
	h.send("u {4}")
	expectContinuation(t, h, "LOGIN password literal")
	h.send(`a "b`)

	status := h.readLine()
	mustContain(t, status, "a1 ", "LOGIN response tag")
	mustContain(t, status, " OK", "special-char literal delivered intact")
}

// TestLiteral_OversizeRejected proves a synchronizing literal larger than
// MaxLiteralSize is refused with a tagged BAD and WITHOUT a continuation, so the
// octets are never invited and cannot exhaust memory (the pre-auth unbounded
// literal guard). The connection must stay usable afterwards.
func TestLiteral_OversizeRejected(t *testing.T) {
	mock := newMockBackend()
	h := newIMAPHarness(t, mock)

	h.send("a1 LOGIN {%d}", MaxLiteralSize+1)

	line := h.readLine()
	if strings.HasPrefix(line, "+") {
		t.Fatalf("server invited an over-large literal with a continuation: %q", line)
	}
	mustContain(t, line, "a1 ", "over-large literal response tag")
	mustContain(t, line, "BAD", "over-large literal rejection")
	mustContain(t, strings.ToLower(line), "too large", "over-large literal reason")

	// No octets were sent, so the stream is still in sync: a normal command works.
	if _, status := h.command("a2", "CAPABILITY"); !strings.Contains(status, "OK") {
		t.Fatalf("CAPABILITY after rejected literal: status = %q, want OK", status)
	}
}

// TestLiteral_NonSynchronizingPlus accepts a LITERAL+ non-synchronizing literal
// ({n+}, RFC 2088): the client streams the octets immediately without waiting,
// so the server must NOT send a continuation. The whole command arrives in one
// write; the first line the client reads back must be the tagged completion.
func TestLiteral_NonSynchronizingPlus(t *testing.T) {
	mock := newMockBackend()
	mock.user = "admin"
	mock.pass = "password"
	h := newIMAPHarnessWith(t, mock, mock.user, mock.pass)

	// Octets follow each {n+} marker with no intervening continuation.
	h.send("a1 LOGIN {5+}\r\nadmin {8+}\r\npassword")

	status := h.readLine()
	mustContain(t, status, "a1 ", "LITERAL+ response tag")
	mustContain(t, status, " OK", "LOGIN via non-synchronizing literal")
}

// TestLiteral_LoginThenAppendCoexist proves the generalized literal path and
// APPEND's own message-literal path coexist: authenticate via synchronizing
// literals, then APPEND a message via its (unchanged) literal path and confirm
// the raw body is delivered verbatim.
func TestLiteral_LoginThenAppendCoexist(t *testing.T) {
	mock := newMockBackend()
	mock.user = "admin"
	mock.pass = "password"
	h := newIMAPHarnessWith(t, mock, mock.user, mock.pass)

	h.send("a1 LOGIN {5}")
	expectContinuation(t, h, "LOGIN username literal")
	h.send("admin {8}")
	expectContinuation(t, h, "LOGIN password literal")
	h.send("password")
	if status := h.readLine(); !strings.Contains(status, " OK") {
		t.Fatalf("LOGIN via literals: status = %q, want OK", status)
	}

	body := "Subject: Filed\r\n\r\nhello\r\n"
	_, status := h.appendMsg("a2", "Archive", "", body)
	mustContain(t, status, " OK", "APPEND after literal LOGIN")

	op, ok := mock.mbox.lastAppend()
	if !ok {
		t.Fatal("APPEND recorded nothing")
	}
	if op.dest != "Archive" {
		t.Errorf("APPEND delivered to %q, want Archive", op.dest)
	}
	if string(op.raw) != body {
		t.Errorf("APPEND body = %q, want %q", string(op.raw), body)
	}
}
