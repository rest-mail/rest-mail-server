package server

import (
	"strings"
	"testing"
)

// CHECK (RFC 3501 §6.4.1) and CLOSE (§6.4.2) are "Selected State" commands: they
// are valid only when a mailbox is selected. Issued in the non-authenticated or
// authenticated (no mailbox selected) state they must be rejected with a tagged
// BAD ("command not allowed in current state"), not answered OK. These tests pin
// that guard and keep the happy-path behaviour (CHECK → OK, CLOSE → expunge +
// deselect) intact.

// TestIMAP_Check_BadWhenNotAuthenticated: CHECK before LOGIN must be BAD.
func TestIMAP_Check_BadWhenNotAuthenticated(t *testing.T) {
	m := newMockBackend()
	h := newIMAPHarness(t, m)

	if _, status := h.command("a1", "CHECK"); !strings.Contains(status, " BAD") {
		t.Errorf("CHECK pre-auth status = %q, want BAD (not allowed in current state)", status)
	}
}

// TestIMAP_Check_BadWhenNoMailboxSelected: CHECK after LOGIN but before SELECT
// must be BAD.
func TestIMAP_Check_BadWhenNoMailboxSelected(t *testing.T) {
	m := newMockBackend()
	h := newIMAPHarness(t, m)
	h.login("a1")

	if _, status := h.command("a2", "CHECK"); !strings.Contains(status, " BAD") {
		t.Errorf("CHECK with no mailbox selected status = %q, want BAD", status)
	}
}

// TestIMAP_Check_OkWhenSelected: CHECK in the selected state still completes OK.
func TestIMAP_Check_OkWhenSelected(t *testing.T) {
	m := newMockBackend()
	m.seed("INBOX", 5, 100, "Subject: Msg 5\r\n\r\nbody 5\r\n")
	h := newIMAPHarness(t, m)
	h.login("a1")
	h.selectInbox("a2")

	if _, status := h.command("a3", "CHECK"); !strings.Contains(status, " OK") {
		t.Errorf("CHECK when selected status = %q, want OK", status)
	}
}

// TestIMAP_Close_BadWhenNotAuthenticated: CLOSE before LOGIN must be BAD.
func TestIMAP_Close_BadWhenNotAuthenticated(t *testing.T) {
	m := newMockBackend()
	h := newIMAPHarness(t, m)

	if _, status := h.command("a1", "CLOSE"); !strings.Contains(status, " BAD") {
		t.Errorf("CLOSE pre-auth status = %q, want BAD (not allowed in current state)", status)
	}
}

// TestIMAP_Close_BadWhenNoMailboxSelected: CLOSE after LOGIN but before SELECT
// must be BAD, not OK.
func TestIMAP_Close_BadWhenNoMailboxSelected(t *testing.T) {
	m := newMockBackend()
	h := newIMAPHarness(t, m)
	h.login("a1")

	if _, status := h.command("a2", "CLOSE"); !strings.Contains(status, " BAD") {
		t.Errorf("CLOSE with no mailbox selected status = %q, want BAD", status)
	}
}

// TestIMAP_Close_ExpungesAndDeselectsWhenSelected keeps the existing CLOSE
// behaviour: with a read-write mailbox selected, CLOSE implicitly expunges
// \Deleted messages, returns OK, and returns to the authenticated (unselected)
// state — so a following FETCH is refused.
func TestIMAP_Close_ExpungesAndDeselectsWhenSelected(t *testing.T) {
	m := newMockBackend()
	m.seed("INBOX", 5, 100, "Subject: Msg 5\r\n\r\nbody 5\r\n")
	h := newIMAPHarness(t, m)
	h.login("a1")
	h.selectInbox("a2")

	// Mark the message \Deleted so CLOSE has something to expunge.
	if _, status := h.command("a3", "STORE 1 +FLAGS (\\Deleted)"); !strings.Contains(status, " OK") {
		t.Fatalf("STORE \\Deleted status = %q, want OK", status)
	}

	// CLOSE completes OK and does not emit untagged EXPUNGE responses.
	untagged, status := h.command("a4", "CLOSE")
	if !strings.Contains(status, " OK") {
		t.Fatalf("CLOSE when selected status = %q, want OK", status)
	}
	for _, l := range untagged {
		if strings.Contains(l, "EXPUNGE") {
			t.Errorf("CLOSE emitted %q; CLOSE must not send untagged EXPUNGE responses", l)
		}
	}

	// The \Deleted message was expunged via the backend.
	if !m.mbox.wasDeleted(5) {
		t.Errorf("CLOSE did not expunge \\Deleted UID 5")
	}

	// CLOSE returned to the authenticated state: no mailbox is selected, so a
	// following FETCH is refused.
	if _, status := h.command("a5", "FETCH 1 (FLAGS)"); !strings.Contains(status, " NO") {
		t.Errorf("FETCH after CLOSE status = %q, want NO (no mailbox selected)", status)
	}
}
