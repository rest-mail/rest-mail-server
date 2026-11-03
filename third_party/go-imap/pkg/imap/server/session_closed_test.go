package server

import (
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

const closedLine = "* OK [CLOSED] Previous mailbox is now closed"

// rev2Harness is a logged-in session that speaks IMAP4rev2, with INBOX and
// Drafts, and a folder Broken that cannot be selected. No command enables rev2 yet, so the test sets the state directly.
func rev2Harness(t *testing.T) *imapHarness {
	t.Helper()
	mock := newMockBackend()
	mock.mbox.folders = append(mock.mbox.folders, imap.Folder{Name: "Drafts"})
	mock.seed("INBOX", 1, 100, "")
	mock.seed("Drafts", 2, 100, "")
	// "Broken" fails to select, as a missing or unreadable mailbox would.
	backend := &failingSelectBackend{
		mockBackend: mock,
		mbox:        &failingSelectMailbox{mockMailbox: mock.mbox, failFolder: "Broken"},
	}
	h := newIMAPHarnessSession(t, backend, mock.user, mock.pass, func(s *Session) { s.imap4rev2 = true })
	h.mock = mock
	h.login("a1")
	return h
}

func countClosed(lines []string) int {
	n := 0
	for _, l := range lines {
		if l == closedLine {
			n++
		}
	}
	return n
}

// TestSelectClosedRev2: "When deselecting a selected mailbox, the server MUST
// return an untagged OK response with the "[CLOSED]" response code" (RFC 9051
// §6.3.2), as the first response of the new selection: "all responses before
// the CLOSED response code relate to the mailbox that was closed, and all
// subsequent responses relate to the newly opened mailbox" (§7.1). This is the
// §6.3.2 example: A142 SELECT INBOX, then A143 SELECT Drafts. EXAMINE closes a
// mailbox the same way.
func TestSelectClosedRev2(t *testing.T) {
	h := rev2Harness(t)

	lines, status := h.command("A142", "SELECT INBOX")
	if !hasStatus(status, "OK") || countClosed(lines) != 0 {
		t.Fatalf("first SELECT: %q %q, want OK without CLOSED", lines, status)
	}
	lines, status = h.command("A143", "SELECT Drafts")
	if !hasStatus(status, "OK") || len(lines) == 0 || lines[0] != closedLine || countClosed(lines) != 1 {
		t.Fatalf("SELECT Drafts: %q %q, want CLOSED once and first", lines, status)
	}
	lines, status = h.command("A144", "EXAMINE INBOX")
	if !hasStatus(status, "OK") || len(lines) == 0 || lines[0] != closedLine || countClosed(lines) != 1 {
		t.Fatalf("EXAMINE INBOX: %q %q, want CLOSED once and first", lines, status)
	}
}

// TestSelectClosedRev2OnFailure: a SELECT that fails still closed the old
// mailbox, "if a mailbox is selected and a SELECT command that fails is
// attempted, no mailbox is selected" (RFC 9051 §6.3.2), so CLOSED comes before
// the NO. A SELECT refused with BAD changes nothing and sends no CLOSED.
func TestSelectClosedRev2OnFailure(t *testing.T) {
	h := rev2Harness(t)
	h.selectInbox("s1")

	lines, status := h.command("s2", "SELECT INBOX extra")
	if !hasStatus(status, "BAD") || countClosed(lines) != 0 {
		t.Fatalf("BAD SELECT: %q %q, want BAD without CLOSED", lines, status)
	}
	lines, status = h.command("s3", "SELECT Broken")
	if !hasStatus(status, "NO") || len(lines) != 1 || lines[0] != closedLine {
		t.Fatalf("failed SELECT: %q %q, want CLOSED then NO", lines, status)
	}
	// Nothing is selected now, so the next SELECT closes nothing.
	lines, status = h.command("s4", "SELECT INBOX")
	if !hasStatus(status, "OK") || countClosed(lines) != 0 {
		t.Fatalf("SELECT after failure: %q %q, want OK without CLOSED", lines, status)
	}
}

// TestSelectClosedRev2NotAfterClose: "There is no need to return the CLOSED
// response code on completion of the CLOSE or the UNSELECT command" (RFC 9051
// §7.1); a SELECT after either has no mailbox to close.
func TestSelectClosedRev2NotAfterClose(t *testing.T) {
	h := rev2Harness(t)
	for i, closer := range []string{"CLOSE", "UNSELECT"} {
		h.selectInbox("s" + string(rune('a'+i)))
		lines, status := h.command("c"+string(rune('a'+i)), "%s", closer)
		if !hasStatus(status, "OK") || countClosed(lines) != 0 {
			t.Fatalf("%s: %q %q, want OK without CLOSED", closer, lines, status)
		}
		lines, status = h.command("n"+string(rune('a'+i)), "SELECT Drafts")
		if !hasStatus(status, "OK") || countClosed(lines) != 0 {
			t.Fatalf("SELECT after %s: %q %q, want OK without CLOSED", closer, lines, status)
		}
		h.command("u"+string(rune('a'+i)), "UNSELECT")
	}
}

// TestSelectNoClosedRev1: the default rev1 session gets no CLOSED (see
// closedCode).
func TestSelectNoClosedRev1(t *testing.T) {
	mock := newMockBackend()
	h := newIMAPHarness(t, mock)
	h.login("a1")
	h.selectInbox("s1")
	lines, status := h.command("s2", "SELECT INBOX")
	if !hasStatus(status, "OK") || countClosed(lines) != 0 {
		t.Fatalf("rev1 reselect: %q %q, want OK without CLOSED", lines, status)
	}
}

// TestStatusDeletedCountsStoredFlag: STATUS DELETED counts the backend's
// \Deleted flags (RFC 9051 §6.3.11), in the order asked beside SIZE.
func TestStatusDeletedCountsStoredFlag(t *testing.T) {
	mock := newMockBackend()
	mock.seed("INBOX", 1, 10, "")
	mock.seed("INBOX", 2, 20, "")
	mock.seed("INBOX", 3, 30, "")
	mock.mbox.byFolder["INBOX"][0].Deleted = true
	mock.mbox.byFolder["INBOX"][2].Deleted = true
	h := newIMAPHarness(t, mock)
	h.login("a1")
	untagged, status := h.command("s1", "STATUS INBOX (DELETED SIZE MESSAGES)")
	if !hasStatus(status, "OK") {
		t.Fatalf("STATUS status = %q, want OK", status)
	}
	if line := statusLine(untagged); line != `* STATUS "INBOX" (DELETED 2 SIZE 60 MESSAGES 3)` {
		t.Errorf("STATUS response = %q", line)
	}
}
