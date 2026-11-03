package server

import (
	"testing"
	"time"
)

// TestAppend_UnquotedMailboxDelivered is the regression guard for issue #18: an
// unquoted (atom) mailbox name is a legal astring (RFC 3501 §6.3.11) and must be
// honoured. The old parser only recognised a quoted name, so an unquoted one was
// ignored and the message misdelivered to INBOX.
func TestAppend_UnquotedMailboxDelivered(t *testing.T) {
	b := newMockBackend()
	h := newIMAPHarness(t, b)
	h.login("a1")

	body := "Subject: Filed\r\n\r\nhi\r\n"
	_, status := h.appendMsg("a2", "Archive", "", body) // unquoted mailbox atom
	mustContain(t, status, " OK", "APPEND status")

	op, ok := b.mbox.lastAppend()
	if !ok {
		t.Fatal("no APPEND recorded")
	}
	if op.dest != "Archive" {
		t.Errorf("delivered to %q, want %q (misdelivery — issue #18)", op.dest, "Archive")
	}
}

// TestAppend_QuotedMailboxStillWorks keeps the quoted form working and shows that
// a parenthesis inside a quoted mailbox name is no longer mistaken for the flag
// list (the old scan used strings.Index("(")).
func TestAppend_QuotedMailboxStillWorks(t *testing.T) {
	b := newMockBackend()
	h := newIMAPHarness(t, b)
	h.login("a1")

	body := "Subject: Q\r\n\r\nhi\r\n"
	_, status := h.appendMsg("a2", `"My (Weird) Folder"`, "", body)
	mustContain(t, status, " OK", "APPEND status")

	op, ok := b.mbox.lastAppend()
	if !ok {
		t.Fatal("no APPEND recorded")
	}
	if op.dest != "My (Weird) Folder" {
		t.Errorf("delivered to %q, want %q", op.dest, "My (Weird) Folder")
	}
	if op.flags.Seen != nil || op.flags.Answered != nil || op.flags.Flagged != nil || op.flags.Draft != nil {
		t.Errorf("a paren inside the mailbox name was parsed as a flag list: %+v", op.flags)
	}
}

// TestAppend_FlagsApplied checks the flag-list is parsed and applied — including
// \Answered, which the old handler silently dropped.
func TestAppend_FlagsApplied(t *testing.T) {
	b := newMockBackend()
	h := newIMAPHarness(t, b)
	h.login("a1")

	body := "Subject: Flagged\r\n\r\nhi\r\n"
	_, status := h.appendMsg("a2", "Archive", `\Seen \Answered`, body)
	mustContain(t, status, " OK", "APPEND status")

	op, ok := b.mbox.lastAppend()
	if !ok {
		t.Fatal("no APPEND recorded")
	}
	if op.flags.Seen == nil || !*op.flags.Seen {
		t.Errorf("\\Seen not applied: %+v", op.flags)
	}
	if op.flags.Answered == nil || !*op.flags.Answered {
		t.Errorf("\\Answered not applied (dropped by old handler): %+v", op.flags)
	}
}

// TestAppend_UnknownFlagRejected asserts an unsupported flag is rejected with a
// tagged NO and no message is delivered, rather than being silently discarded.
func TestAppend_UnknownFlagRejected(t *testing.T) {
	b := newMockBackend()
	h := newIMAPHarness(t, b)
	h.login("a1")

	// The server must reject before the continuation, so no literal is sent.
	h.send(`a2 APPEND Archive (\Bogus) {11}`)
	line := h.readLine()
	mustContain(t, line, "a2 ", "APPEND response tag")
	mustContain(t, line, " NO", "APPEND response")

	if _, ok := b.mbox.lastAppend(); ok {
		t.Error("a rejected APPEND still delivered a message")
	}
}

// TestAppend_DeletedFlagAccepted checks \Deleted is a recognised system flag: it
// is accepted (not rejected as unknown) even though this engine does not persist
// it as a backend flag.
func TestAppend_DeletedFlagAccepted(t *testing.T) {
	b := newMockBackend()
	h := newIMAPHarness(t, b)
	h.login("a1")

	body := "Subject: D\r\n\r\nhi\r\n"
	_, status := h.appendMsg("a2", "Trash", `\Deleted`, body)
	mustContain(t, status, " OK", "APPEND status")

	op, ok := b.mbox.lastAppend()
	if !ok {
		t.Fatal("no APPEND recorded")
	}
	if op.dest != "Trash" {
		t.Errorf("delivered to %q, want %q", op.dest, "Trash")
	}
}

// TestAppend_DateTimeHonored checks the optional quoted date-time is parsed and,
// for a DateAppender backend, delivered so INTERNALDATE can be set. The old
// handler never parsed it.
func TestAppend_DateTimeHonored(t *testing.T) {
	b := newMockBackend()
	h := newIMAPHarness(t, b)
	h.login("a1")

	body := "Subject: Dated\r\n\r\nhi\r\n"
	h.send("a2 APPEND Archive (\\Seen) \"25-Jul-2026 13:04:05 +0000\" {%d}", len(body))
	if got := h.readLine(); got[:1] != "+" {
		t.Fatalf("APPEND continuation = %q, want +...", got)
	}
	h.send("%s", body)
	if got := h.readLine(); got[:3] != "a2 " {
		t.Fatalf("APPEND status = %q, want a2 ...", got)
	}

	op, ok := b.mbox.lastAppend()
	if !ok {
		t.Fatal("no APPEND recorded")
	}
	if op.dest != "Archive" {
		t.Errorf("delivered to %q, want %q", op.dest, "Archive")
	}
	if !op.hasDate {
		t.Fatal("date-time was not delivered to the backend (dropped by old handler)")
	}
	want := time.Date(2026, 7, 25, 13, 4, 5, 0, time.UTC)
	if !op.date.Equal(want) {
		t.Errorf("internaldate = %v, want %v", op.date.UTC(), want)
	}
	if op.flags.Seen == nil || !*op.flags.Seen {
		t.Errorf("\\Seen alongside a date-time was not applied: %+v", op.flags)
	}
}

// TestAppend_EmitsExistsForSelectedMailbox pins RFC 3501 §6.3.11: an APPEND into
// the mailbox the session currently has selected must volunteer an untagged
// "* n EXISTS" so the client learns of the new message. The old handler never
// sent it, so this untagged response was absent (RED).
func TestAppend_EmitsExistsForSelectedMailbox(t *testing.T) {
	b := newMockBackend()
	h := newIMAPHarness(t, b)
	h.login("a1")
	h.selectInbox("a2") // INBOX starts empty

	body := "Subject: New\r\n\r\nhi\r\n"
	untagged, status := h.appendMsg("a3", "INBOX", "", body)
	mustContain(t, status, " OK", "APPEND status")

	if !containsLine(untagged, "* 1 EXISTS") {
		t.Errorf("APPEND into selected INBOX untagged = %v, want it to include \"* 1 EXISTS\" (RFC 3501 §6.3.11)", untagged)
	}
}

// TestAppend_NoExistsForOtherMailbox checks the EXISTS is scoped to the selected
// mailbox: appending elsewhere must not fabricate an EXISTS for the current
// selection.
func TestAppend_NoExistsForOtherMailbox(t *testing.T) {
	b := newMockBackend()
	h := newIMAPHarness(t, b)
	h.login("a1")
	h.selectInbox("a2")

	body := "Subject: Filed\r\n\r\nhi\r\n"
	untagged, status := h.appendMsg("a3", "Archive", "", body)
	mustContain(t, status, " OK", "APPEND status")

	if anyContains(untagged, "EXISTS") {
		t.Errorf("APPEND into Archive (INBOX selected) emitted an EXISTS: %v", untagged)
	}
}
