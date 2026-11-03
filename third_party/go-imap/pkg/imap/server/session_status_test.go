package server

import (
	"strings"
	"testing"
)

// RFC 3501 §6.3.10: STATUS takes a parenthesized list of the data items the
// client wants (MESSAGES, RECENT, UIDNEXT, UIDVALIDITY, UNSEEN) and the untagged
// "* STATUS mailbox (...)" response MUST contain exactly those items, with
// correct values. STATUS must not select the mailbox or set \Seen.

// statusLine returns the untagged "* STATUS ..." line from a command response,
// or "" if none was sent.
func statusLine(untagged []string) string {
	for _, l := range untagged {
		if strings.HasPrefix(l, "* STATUS") {
			return l
		}
	}
	return ""
}

// TestStatusReturnsRequestedItems: the response returns exactly the requested
// items, in request order, and none of the un-requested ones. On the pre-fix
// code STATUS ignored the item list and always emitted MESSAGES/RECENT/UNSEEN
// with no UIDNEXT or UIDVALIDITY — so this fails (RED) there.
func TestStatusReturnsRequestedItems(t *testing.T) {
	mock := newMockBackend()
	mock.seed("INBOX", 5, 100, "")
	mock.seed("INBOX", 9, 100, "")
	h := newIMAPHarness(t, mock)
	h.login("a1")

	untagged, status := h.command("s1", "STATUS INBOX (MESSAGES UIDNEXT UIDVALIDITY)")
	if !hasStatus(status, "OK") {
		t.Fatalf("STATUS status = %q, want OK", status)
	}
	line := statusLine(untagged)
	// Base backend has no UIDPLUS support -> UIDVALIDITY falls back to 1.
	// Highest UID is 9, so UIDNEXT is 10.
	if line != `* STATUS "INBOX" (MESSAGES 2 UIDNEXT 10 UIDVALIDITY 1)` {
		t.Errorf("STATUS response = %q, want exactly the requested items", line)
	}
	// Un-requested items must not appear.
	mustNotContain(t, line, "RECENT", "STATUS response")
	mustNotContain(t, line, "UNSEEN", "STATUS response")
}

// TestStatusUIDNextUIDValidityFromBackend: with a UIDPLUS backend, UIDVALIDITY
// is the mailbox's real value and UIDNEXT is highest-UID + 1.
func TestStatusUIDNextUIDValidityFromBackend(t *testing.T) {
	b := newUIDPlusBackend() // INBOX UIDs 5,9,20; UIDVALIDITY 42
	h := newIMAPHarnessWith(t, b, b.user, b.pass)
	h.login("a1")

	untagged, status := h.command("s1", "STATUS INBOX (UIDVALIDITY UIDNEXT MESSAGES)")
	if !hasStatus(status, "OK") {
		t.Fatalf("STATUS status = %q, want OK", status)
	}
	line := statusLine(untagged)
	mustContain(t, line, "UIDVALIDITY 42", "STATUS response")
	mustContain(t, line, "UIDNEXT 21", "STATUS response")
	mustContain(t, line, "MESSAGES 3", "STATUS response")
}

// TestStatusUnknownItemRejected: an unrecognised status item is a syntax error
// and must be rejected with BAD, with no untagged STATUS response emitted.
func TestStatusUnknownItemRejected(t *testing.T) {
	mock := newMockBackend()
	mock.seed("INBOX", 5, 100, "")
	h := newIMAPHarness(t, mock)
	h.login("a1")

	untagged, status := h.command("s1", "STATUS INBOX (MESSAGES BOGUS)")
	if !hasStatus(status, "BAD") || hasStatus(status, "OK") {
		t.Errorf("STATUS with unknown item: status = %q, want BAD", status)
	}
	if line := statusLine(untagged); line != "" {
		t.Errorf("STATUS with unknown item emitted a response line %q, want none", line)
	}
}

// TestStatusMissingItemListRejected: STATUS without a parenthesized item list is
// a syntax error (BAD).
func TestStatusMissingItemListRejected(t *testing.T) {
	mock := newMockBackend()
	mock.seed("INBOX", 5, 100, "")
	h := newIMAPHarness(t, mock)
	h.login("a1")

	for _, cmd := range []string{"STATUS INBOX", "STATUS INBOX ()", "STATUS INBOX MESSAGES"} {
		_, status := h.command("s1", "%s", cmd)
		if !hasStatus(status, "BAD") || hasStatus(status, "OK") {
			t.Errorf("%q: status = %q, want BAD", cmd, status)
		}
	}
}

// TestStatusBasicMessages: the simplest single-item request keeps working.
func TestStatusBasicMessages(t *testing.T) {
	mock := newMockBackend()
	mock.seed("INBOX", 5, 100, "")
	mock.seed("INBOX", 9, 100, "")
	h := newIMAPHarness(t, mock)
	h.login("a1")

	untagged, status := h.command("s1", "STATUS INBOX (MESSAGES)")
	if !hasStatus(status, "OK") {
		t.Fatalf("STATUS status = %q, want OK", status)
	}
	line := statusLine(untagged)
	if line != `* STATUS "INBOX" (MESSAGES 2)` {
		t.Errorf("STATUS response = %q, want just MESSAGES 2", line)
	}
}

// TestStatusUnseenAndRecent: UNSEEN and RECENT are returned when requested.
func TestStatusUnseenAndRecent(t *testing.T) {
	mock := newMockBackend()
	mock.seed("INBOX", 5, 100, "") // unseen
	mock.seed("INBOX", 9, 100, "") // unseen
	h := newIMAPHarness(t, mock)
	h.login("a1")

	untagged, status := h.command("s1", "STATUS INBOX (UNSEEN RECENT)")
	if !hasStatus(status, "OK") {
		t.Fatalf("STATUS status = %q, want OK", status)
	}
	line := statusLine(untagged)
	mustContain(t, line, "UNSEEN 2", "STATUS response")
	mustContain(t, line, "RECENT 2", "STATUS response")
	mustNotContain(t, line, "MESSAGES", "STATUS response")
}

// TestStatusDoesNotSelectMailbox: STATUS must not change the selected state
// (RFC 3501 §6.3.10) — a FETCH afterwards must still fail for want of a SELECT.
func TestStatusDoesNotSelectMailbox(t *testing.T) {
	mock := newMockBackend()
	mock.seed("INBOX", 5, 100, "Subject: a\r\n\r\nx\r\n")
	h := newIMAPHarness(t, mock)
	h.login("a1")

	if _, status := h.command("s1", "STATUS INBOX (MESSAGES)"); !hasStatus(status, "OK") {
		t.Fatalf("STATUS status = %q, want OK", status)
	}
	// No mailbox is selected, so FETCH must be refused.
	_, status := h.command("f1", "FETCH 1 (FLAGS)")
	if !hasStatus(status, "NO") {
		t.Errorf("FETCH after STATUS: status = %q, want NO (STATUS must not select)", status)
	}
	// STATUS must not have marked the message \Seen.
	if mock.mbox.wasMarkedRead(5) {
		t.Errorf("STATUS marked UID 5 \\Seen; it must not touch flags")
	}
}
