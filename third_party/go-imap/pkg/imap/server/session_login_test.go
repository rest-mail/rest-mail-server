package server

import (
	"strings"
	"testing"
)

// A password containing a double quote and a backslash is sent as an IMAP
// quoted-string with those characters backslash-escaped (RFC 3501 §4.3). The
// server must decode `\"` -> `"` and `\\` -> `\` before authenticating; without
// that decoding the backend receives the wrong password and LOGIN fails
// silently (issue #25).
func TestLogin_QuotedPasswordWithEscapes(t *testing.T) {
	mock := newMockBackend()
	mock.pass = `p"a\ss` // literal password: p " a \ s s
	h := newIMAPHarnessWith(t, mock, mock.user, mock.pass)

	// Wire form: the " is escaped as \" and the \ as \\ inside the quoted-string.
	_, status := h.command("a1", `LOGIN %s "p\"a\\ss"`, mock.user)
	if !strings.Contains(status, " OK") {
		t.Fatalf("LOGIN with escaped quoted password: status = %q, want OK", status)
	}
}

// The escaped quote inside the quoted password must not terminate the argument
// early: a wrong (truncated) password must NOT authenticate. This guards the
// paired risk that a lenient parser accepts a shorter string.
func TestLogin_TruncatedEscapedPasswordRejected(t *testing.T) {
	mock := newMockBackend()
	mock.pass = `p\` // what the buggy parser would have decoded the password to
	h := newIMAPHarnessWith(t, mock, mock.user, mock.pass)

	// Client intends the password p"ass, but the account's password is p\ .
	_, status := h.command("a1", `LOGIN %s "p\"ass"`, mock.user)
	if !strings.Contains(status, " NO") {
		t.Fatalf("LOGIN with mismatched password: status = %q, want NO", status)
	}
}
