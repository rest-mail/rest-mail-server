package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// A failed SELECT/EXAMINE must leave the session with NO mailbox selected
// (RFC 3501 §6.3.1: "if a SELECT command that fails is attempted, no mailbox is
// selected."; §6.3.2 gives EXAMINE the same rule). These tests pin that a
// failure drops any prior selection rather than leaving the old mailbox active
// for a subsequent FETCH/STORE/SEARCH.

// failingSelectMailbox wraps the in-memory mockMailbox and makes SELECT/EXAMINE
// of one named folder fail — as a nonexistent mailbox, permission denial, or
// backend error would — while every other folder behaves normally. Embedding
// *mockMailbox promotes the full Mailbox interface; only Messages is overridden.
type failingSelectMailbox struct {
	*mockMailbox
	failFolder string
}

func (m *failingSelectMailbox) Messages(folder string) ([]imap.Message, error) {
	if folder == m.failFolder {
		return nil, fmt.Errorf("mailbox %q does not exist", folder)
	}
	return m.mockMailbox.Messages(folder)
}

// failingSelectBackend authenticates the same user as mockBackend but hands out
// a failingSelectMailbox.
type failingSelectBackend struct {
	*mockBackend
	mbox *failingSelectMailbox
}

func (b *failingSelectBackend) Authenticate(user, pass string) (imap.Mailbox, error) {
	if _, err := b.mockBackend.Authenticate(user, pass); err != nil {
		return nil, err
	}
	return b.mbox, nil
}

// newFailingSelectHarness builds a harness whose SELECT/EXAMINE of failFolder
// fails while INBOX (seeded with one message, UID 5) selects normally.
func newFailingSelectHarness(t *testing.T, failFolder string) *imapHarness {
	t.Helper()
	m := newMockBackend()
	m.seed("INBOX", 5, 100, "Subject: Msg 5\r\n\r\nbody 5\r\n")
	b := &failingSelectBackend{
		mockBackend: m,
		mbox:        &failingSelectMailbox{mockMailbox: m.mbox, failFolder: failFolder},
	}
	return newIMAPHarnessWith(t, b, m.user, m.pass)
}

// TestIMAP_FailedSelect_Unselects is the core regression: SELECT INBOX (success)
// then SELECT of a nonexistent mailbox (failure). After the failure no mailbox
// may be selected, so a following FETCH must be refused with "No mailbox
// selected" — NOT served from the previously selected INBOX.
func TestIMAP_FailedSelect_Unselects(t *testing.T) {
	h := newFailingSelectHarness(t, "NOSUCH")
	h.login("a1")

	// A: SELECT INBOX succeeds and selects it.
	h.selectInbox("a2")

	// B: SELECT of a nonexistent mailbox fails.
	if _, status := h.command("a3", "SELECT NOSUCH"); !strings.Contains(status, " NO") {
		t.Fatalf("SELECT NOSUCH status = %q, want NO", status)
	}

	// The failed SELECT must have unselected: FETCH is refused, and no FETCH
	// data is emitted from the stale INBOX selection.
	untagged, status := h.command("a4", "FETCH 1 (FLAGS)")
	if !strings.Contains(status, " NO") {
		t.Errorf("FETCH after failed SELECT status = %q, want NO (no mailbox selected); the failed SELECT wrongly left INBOX selected", status)
	}
	for _, l := range untagged {
		if strings.Contains(l, "FETCH") {
			t.Errorf("FETCH after failed SELECT emitted %q; the failed SELECT wrongly served the old mailbox", l)
		}
	}
}

// TestIMAP_FailedExamine_Unselects pins the same rule for EXAMINE (RFC 3501
// §6.3.2).
func TestIMAP_FailedExamine_Unselects(t *testing.T) {
	h := newFailingSelectHarness(t, "NOSUCH")
	h.login("a1")
	h.selectInbox("a2")

	if _, status := h.command("a3", "EXAMINE NOSUCH"); !strings.Contains(status, " NO") {
		t.Fatalf("EXAMINE NOSUCH status = %q, want NO", status)
	}
	if _, status := h.command("a4", "FETCH 1 (FLAGS)"); !strings.Contains(status, " NO") {
		t.Errorf("FETCH after failed EXAMINE status = %q, want NO (no mailbox selected)", status)
	}
}

// TestIMAP_SelectAfterFailedSelect_Works guards the happy path: a valid SELECT
// after a failed one still selects the mailbox and serves its messages.
func TestIMAP_SelectAfterFailedSelect_Works(t *testing.T) {
	h := newFailingSelectHarness(t, "NOSUCH")
	h.login("a1")

	if _, status := h.command("a2", "SELECT NOSUCH"); !strings.Contains(status, " NO") {
		t.Fatalf("SELECT NOSUCH status = %q, want NO", status)
	}

	// Recovering with a valid SELECT must work and serve that mailbox.
	h.selectInbox("a3")
	untagged, status := h.command("a4", "FETCH 1 (FLAGS)")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH after recovery SELECT status = %q, want OK", status)
	}
	found := false
	for _, l := range untagged {
		if strings.Contains(l, "FETCH") {
			found = true
		}
	}
	if !found {
		t.Errorf("FETCH after recovery SELECT returned no FETCH data; a valid SELECT should serve messages")
	}
}
