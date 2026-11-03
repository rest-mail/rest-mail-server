package server

import (
	"strings"
	"testing"
)

// TestSubscribe_Accepted pins RFC 3501 §6.3.6/§6.3.7: SUBSCRIBE and UNSUBSCRIBE
// are mandatory IMAP4rev1 commands. Before the fix neither was in the command
// switch, so a client that issued them against a server advertising IMAP4rev1 got
// a tagged BAD "Unknown command". They must be accepted (this engine keeps no
// separate subscription list — LSUB reports every folder — so they no-op).
func TestSubscribe_Accepted(t *testing.T) {
	b := newMockBackend()
	h := newIMAPHarness(t, b)
	h.login("a1")

	for _, tc := range []struct {
		tag, cmd string
	}{
		{"a2", "SUBSCRIBE INBOX"},
		{"a3", "UNSUBSCRIBE INBOX"},
		{"a4", `SUBSCRIBE "INBOX"`},    // quoted mailbox
		{"a5", "SUBSCRIBE Some/Other"}, // subscribing a non-existent name is allowed
	} {
		_, status := h.command(tc.tag, "%s", tc.cmd)
		if !strings.Contains(status, " OK") {
			t.Errorf("%q status = %q, want OK (mandatory command must not answer BAD)", tc.cmd, status)
		}
	}
}

// TestSubscribe_MissingMailboxIsBad checks that a bare SUBSCRIBE/UNSUBSCRIBE with
// no mailbox argument is a client error (tagged BAD), not silently accepted.
func TestSubscribe_MissingMailboxIsBad(t *testing.T) {
	b := newMockBackend()
	h := newIMAPHarness(t, b)
	h.login("a1")

	for _, tc := range []struct {
		tag, cmd string
	}{
		{"a2", "SUBSCRIBE"},
		{"a3", "UNSUBSCRIBE"},
	} {
		_, status := h.command(tc.tag, "%s", tc.cmd)
		if !strings.Contains(status, " BAD") {
			t.Errorf("%q status = %q, want BAD (missing mailbox name)", tc.cmd, status)
		}
	}
}

// TestSubscribe_RequiresAuth checks the commands are refused before authentication.
func TestSubscribe_RequiresAuth(t *testing.T) {
	b := newMockBackend()
	h := newIMAPHarness(t, b)

	_, status := h.command("a1", "SUBSCRIBE INBOX")
	if !strings.Contains(status, " NO") {
		t.Errorf("SUBSCRIBE before login = %q, want NO (not authenticated)", status)
	}
}
