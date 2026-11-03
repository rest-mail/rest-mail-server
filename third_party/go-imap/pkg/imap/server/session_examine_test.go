package server

import (
	"strings"
	"testing"
)

// EXAMINE opens a mailbox READ-ONLY (RFC 3501 §6.3.2): the tagged OK must carry
// [READ-ONLY], and no command may change the mailbox's permanent state. These
// tests pin the read-only contract — STORE/EXPUNGE/UID STORE/UID EXPUNGE are
// refused with NO and the backend is never mutated — while SELECT stays
// read-write.

// examineInbox drives EXAMINE INBOX and returns the tagged status line.
func (h *imapHarness) examineInbox(tag string) string {
	h.t.Helper()
	_, status := h.command(tag, "EXAMINE INBOX")
	return status
}

func TestIMAP_Examine_ReportsReadOnly(t *testing.T) {
	m := newMockBackend()
	m.seed("INBOX", 5, 100, "Subject: Msg 5\r\n\r\nbody 5\r\n")
	h := newIMAPHarness(t, m)
	h.login("a1")

	status := h.examineInbox("a2")
	if !strings.Contains(status, " OK") {
		t.Fatalf("EXAMINE status = %q, want OK", status)
	}
	if !strings.Contains(status, "[READ-ONLY]") {
		t.Errorf("EXAMINE status = %q, want [READ-ONLY] resp-code (RFC 3501 §6.3.2)", status)
	}
}

func TestIMAP_Examine_StoreRefusedAndNoMutation(t *testing.T) {
	m := newMockBackend()
	m.seed("INBOX", 5, 100, "Subject: Msg 5\r\n\r\nbody 5\r\n")
	h := newIMAPHarness(t, m)
	h.login("a1")
	h.examineInbox("a2")

	_, status := h.command("a3", "STORE 1 +FLAGS (\\Seen)")
	if !strings.Contains(status, " NO") {
		t.Errorf("STORE after EXAMINE status = %q, want NO (read-only)", status)
	}
	if m.mbox.wasMarkedRead(5) {
		t.Errorf("STORE after EXAMINE mutated \\Seen for UID 5; read-only must not reach the backend")
	}
}

func TestIMAP_Examine_StoreDeletedRefused(t *testing.T) {
	m := newMockBackend()
	m.seed("INBOX", 5, 100, "Subject: Msg 5\r\n\r\nbody 5\r\n")
	h := newIMAPHarness(t, m)
	h.login("a1")
	h.examineInbox("a2")

	// Setting \Deleted is a permanent-state change and must be refused.
	if _, status := h.command("a3", "STORE 1 +FLAGS (\\Deleted)"); !strings.Contains(status, " NO") {
		t.Errorf("STORE \\Deleted after EXAMINE status = %q, want NO (read-only)", status)
	}
	// EXPUNGE is also refused, and nothing is deleted.
	untagged, status := h.command("a4", "EXPUNGE")
	if !strings.Contains(status, " NO") {
		t.Errorf("EXPUNGE after EXAMINE status = %q, want NO (read-only)", status)
	}
	for _, l := range untagged {
		if strings.Contains(l, "EXPUNGE") {
			t.Errorf("EXPUNGE after EXAMINE emitted %q; read-only must expunge nothing", l)
		}
	}
	if m.mbox.wasDeleted(5) {
		t.Errorf("EXPUNGE after EXAMINE deleted UID 5; read-only must not reach the backend")
	}
}

func TestIMAP_Examine_CloseDoesNotExpunge(t *testing.T) {
	m := newMockBackend()
	m.seed("INBOX", 5, 100, "Subject: Msg 5\r\n\r\nbody 5\r\n")
	h := newIMAPHarness(t, m)
	h.login("a1")
	h.examineInbox("a2")

	if _, status := h.command("a3", "CLOSE"); !strings.Contains(status, " OK") {
		t.Fatalf("CLOSE after EXAMINE status = %q, want OK", status)
	}
	if m.mbox.wasDeleted(5) {
		t.Errorf("CLOSE after EXAMINE deleted UID 5; a read-only mailbox is never expunged")
	}
}

// Control: SELECT is read-write, so STORE reaches the backend and EXPUNGE
// deletes. This guards against the read-only guard leaking into normal SELECT.
func TestIMAP_Select_StoreAndExpungeStillWork(t *testing.T) {
	m := newMockBackend()
	m.seed("INBOX", 5, 100, "Subject: Msg 5\r\n\r\nbody 5\r\n")
	h := newIMAPHarness(t, m)
	h.login("a1")
	h.selectInbox("a2")

	if _, status := h.command("a3", "STORE 1 +FLAGS (\\Seen)"); !strings.Contains(status, " OK") {
		t.Fatalf("STORE after SELECT status = %q, want OK", status)
	}
	if !m.mbox.wasMarkedRead(5) {
		t.Errorf("STORE after SELECT did not mark UID 5 \\Seen")
	}

	if _, status := h.command("a4", "STORE 1 +FLAGS (\\Deleted)"); !strings.Contains(status, " OK") {
		t.Fatalf("STORE \\Deleted after SELECT status = %q, want OK", status)
	}
	if _, status := h.command("a5", "EXPUNGE"); !strings.Contains(status, " OK") {
		t.Fatalf("EXPUNGE after SELECT status = %q, want OK", status)
	}
	if !m.mbox.wasDeleted(5) {
		t.Errorf("EXPUNGE after SELECT did not delete UID 5")
	}
}

// Re-SELECTing a mailbox after EXAMINE must clear read-only, restoring
// read-write semantics.
func TestIMAP_SelectAfterExamine_IsReadWriteAgain(t *testing.T) {
	m := newMockBackend()
	m.seed("INBOX", 5, 100, "Subject: Msg 5\r\n\r\nbody 5\r\n")
	h := newIMAPHarness(t, m)
	h.login("a1")

	h.examineInbox("a2")
	h.selectInbox("a3")

	if _, status := h.command("a4", "STORE 1 +FLAGS (\\Seen)"); !strings.Contains(status, " OK") {
		t.Errorf("STORE after SELECT-following-EXAMINE status = %q, want OK", status)
	}
	if !m.mbox.wasMarkedRead(5) {
		t.Errorf("SELECT after EXAMINE did not restore read-write; STORE was dropped")
	}
}

func TestIMAP_Examine_UIDStoreAndUIDExpungeRefused(t *testing.T) {
	b := newUIDPlusBackend()
	h := newIMAPHarnessWith(t, b, b.user, b.pass)
	h.login("a1")

	if _, status := h.command("a2", "EXAMINE INBOX"); !strings.Contains(status, "[READ-ONLY]") {
		t.Fatalf("EXAMINE status = %q, want [READ-ONLY]", status)
	}

	if _, status := h.command("a3", "UID STORE 5 +FLAGS (\\Seen)"); !strings.Contains(status, " NO") {
		t.Errorf("UID STORE after EXAMINE status = %q, want NO (read-only)", status)
	}
	if _, status := h.command("a4", "UID EXPUNGE 5"); !strings.Contains(status, " NO") {
		t.Errorf("UID EXPUNGE after EXAMINE status = %q, want NO (read-only)", status)
	}
	if b.store.deleteCount() != 0 {
		t.Errorf("read-only session deleted %d messages via UID commands, want 0", b.store.deleteCount())
	}
}
