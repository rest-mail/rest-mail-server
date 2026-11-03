package server

import (
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// These tests pin the IDLE change-notification contract (issue #32). RFC 2177
// requires the server to push mailbox changes to an idling client: an untagged
// "* n EXPUNGE" when a message is removed, "* n EXISTS" when one arrives, and a
// "* n FETCH (FLAGS ...)" when flags change. The pre-fix poll only ever grew the
// count, so an external expunge was never reported and the client's cache went
// stale — the two EXPUNGE/flag tests below fail RED on that engine.

// The mock hands out a single shared mailbox, so mutating it here simulates a
// second session (or backend-side change) altering the mailbox while the first
// session idles. Each helper takes the mailbox lock the poll goroutine also
// holds via Messages, so the diff sees an atomic before/after.

func (m *mockMailbox) removeUID(folder string, uid uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := m.byFolder[folder]
	for i, msg := range msgs {
		if msg.UID == uid {
			m.byFolder[folder] = append(msgs[:i], msgs[i+1:]...)
			return
		}
	}
}

func (m *mockMailbox) addMessage(folder string, uid uint32, size int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byFolder[folder] = append(m.byFolder[folder], imap.Message{UID: uid, Size: size})
}

func (m *mockMailbox) setFlagged(folder string, uid uint32, flagged bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.byFolder[folder] {
		if m.byFolder[folder][i].UID == uid {
			m.byFolder[folder][i].Flagged = flagged
			return
		}
	}
}

// shortIdlePoll shortens the IDLE poll interval so the test does not wait the
// production 15s for a tick, restoring it on cleanup.
func shortIdlePoll(t *testing.T) {
	t.Helper()
	old := idlePollInterval
	idlePollInterval = 5 * time.Millisecond
	t.Cleanup(func() { idlePollInterval = old })
}

// TestIMAP_Idle_ReportsExternalExpunge drives a client into IDLE, then removes a
// message from the middle of the selected mailbox out-of-band. RFC 2177/3501 §7
// require an untagged "* 2 EXPUNGE" (the removed message's sequence number) to be
// pushed to the idling client. On the pre-fix poll no EXPUNGE is ever emitted, so
// the read below times out — RED. After the fix the report arrives — GREEN.
func TestIMAP_Idle_ReportsExternalExpunge(t *testing.T) {
	shortIdlePoll(t)

	m := newMockBackend()
	m.seed("INBOX", 5, 100, "")
	m.seed("INBOX", 9, 200, "")
	m.seed("INBOX", 20, 300, "")
	h := newIMAPHarness(t, m)
	h.login("a1")
	h.selectInbox("a2")

	h.send("a3 IDLE")
	if got := h.readLine(); got != "+ idling" {
		t.Fatalf("IDLE continuation = %q, want %q", got, "+ idling")
	}

	// Expunge the middle message (UID 9, sequence 2) via the shared mailbox.
	m.mbox.removeUID("INBOX", 9)

	if got := h.readLine(); got != "* 2 EXPUNGE" {
		t.Fatalf("expunge report = %q, want %q", got, "* 2 EXPUNGE")
	}

	// DONE must still complete the IDLE tag cleanly.
	h.send("DONE")
	if got := h.readLine(); !strings.HasPrefix(got, "a3 OK") {
		t.Fatalf("IDLE termination = %q, want a3 OK...", got)
	}
}

// TestIMAP_Idle_ReportsMultipleExpungesHighestFirst removes two of three messages
// and asserts the EXPUNGE sequence numbers follow the RFC 3501 §7.4.1 renumbering
// rule: the highest-numbered removal is announced first, so each sequence number
// is still valid at the moment it is reported (removing seq 3 then seq 2 leaves
// seq 1 addressing the surviving message correctly).
func TestIMAP_Idle_ReportsMultipleExpungesHighestFirst(t *testing.T) {
	shortIdlePoll(t)

	m := newMockBackend()
	m.seed("INBOX", 5, 100, "")
	m.seed("INBOX", 9, 200, "")
	m.seed("INBOX", 20, 300, "")
	h := newIMAPHarness(t, m)
	h.login("a1")
	h.selectInbox("a2")

	h.send("a3 IDLE")
	if got := h.readLine(); got != "+ idling" {
		t.Fatalf("IDLE continuation = %q, want %q", got, "+ idling")
	}

	// Remove UID 9 (seq 2) and UID 20 (seq 3) in one atomic mailbox state change,
	// so the poll observes both removals in a single diff.
	m.mbox.mu.Lock()
	m.mbox.byFolder["INBOX"] = []imap.Message{{UID: 5, Size: 100}}
	m.mbox.mu.Unlock()

	if got := h.readLine(); got != "* 3 EXPUNGE" {
		t.Fatalf("first expunge = %q, want %q (highest sequence first)", got, "* 3 EXPUNGE")
	}
	if got := h.readLine(); got != "* 2 EXPUNGE" {
		t.Fatalf("second expunge = %q, want %q", got, "* 2 EXPUNGE")
	}

	h.send("DONE")
	if got := h.readLine(); !strings.HasPrefix(got, "a3 OK") {
		t.Fatalf("IDLE termination = %q, want a3 OK...", got)
	}
}

// TestIMAP_Idle_TaggedOKAfterActivity guards the timeout/tagged-response fix
// (issue #32, second sub-item, already ordered correctly by the goroutine
// lifecycle change). After the poll has pushed an untagged "* 2 EXISTS" for a new
// arrival, a DONE must still yield the tagged "OK IDLE terminated" intact — the
// poll goroutine is stopped before the tagged line is written, so the two never
// interleave or race, and the OK is never swallowed.
func TestIMAP_Idle_TaggedOKAfterActivity(t *testing.T) {
	shortIdlePoll(t)

	m := newMockBackend()
	m.seed("INBOX", 5, 100, "")
	h := newIMAPHarness(t, m)
	h.login("b1")
	h.selectInbox("b2")

	h.send("b3 IDLE")
	if got := h.readLine(); got != "+ idling" {
		t.Fatalf("IDLE continuation = %q, want %q", got, "+ idling")
	}

	// A new message arrives externally: the poll must announce it as EXISTS.
	m.mbox.addMessage("INBOX", 6, 120)
	if got := h.readLine(); got != "* 2 EXISTS" {
		t.Fatalf("new-message report = %q, want %q", got, "* 2 EXISTS")
	}

	// DONE after that activity: the tagged completion must arrive intact.
	h.send("DONE")
	if got := h.readLine(); !strings.HasPrefix(got, "b3 OK") {
		t.Fatalf("IDLE termination = %q, want b3 OK...", got)
	}
}

// TestIMAP_Idle_ReportsExternalFlagChange asserts that a flag change on a message
// still present in the mailbox is pushed as an untagged FETCH FLAGS during IDLE.
func TestIMAP_Idle_ReportsExternalFlagChange(t *testing.T) {
	shortIdlePoll(t)

	m := newMockBackend()
	m.seed("INBOX", 5, 100, "")
	m.seed("INBOX", 9, 200, "")
	h := newIMAPHarness(t, m)
	h.login("c1")
	h.selectInbox("c2")

	h.send("c3 IDLE")
	if got := h.readLine(); got != "+ idling" {
		t.Fatalf("IDLE continuation = %q, want %q", got, "+ idling")
	}

	// Flag the second message (UID 9, seq 2) out-of-band.
	m.mbox.setFlagged("INBOX", 9, true)

	if got := h.readLine(); got != `* 2 FETCH (FLAGS (\Flagged))` {
		t.Fatalf("flag-change report = %q, want %q", got, `* 2 FETCH (FLAGS (\Flagged))`)
	}

	h.send("DONE")
	if got := h.readLine(); !strings.HasPrefix(got, "c3 OK") {
		t.Fatalf("IDLE termination = %q, want c3 OK...", got)
	}
}
