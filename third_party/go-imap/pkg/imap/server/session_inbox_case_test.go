package server

import (
	"strings"
	"testing"
)

// RFC 3501 §5.1: the mailbox name "INBOX" is a special case-INSENSITIVE name —
// "inbox", "InBoX", "INBOX" all denote the one real INBOX. Only this reserved
// name is case-insensitive; every other mailbox name stays case-sensitive.

// hasStatus reports whether the tagged status line carries the given word
// (" OK", " NO", " BAD") as a space-delimited token after the tag.
func hasStatus(status, word string) bool {
	return strings.Contains(status, " "+word+" ")
}

// TestDeleteInboxCaseInsensitiveRejected: the standard-folder guard forbids
// deleting INBOX. A lower/mixed-case spelling must be rejected identically —
// on the buggy code "DELETE inbox" bypasses the guard and returns OK.
func TestDeleteInboxCaseInsensitiveRejected(t *testing.T) {
	for i, name := range []string{"INBOX", "inbox", "InBoX", "iNbOx"} {
		mock := newMockBackend()
		mock.seed("INBOX", 5, 100, "")
		h := newIMAPHarness(t, mock)
		h.login("a1")

		_, status := h.command("d1", "DELETE %s", name)
		if !hasStatus(status, "NO") || hasStatus(status, "OK") {
			t.Errorf("DELETE %q [%d]: status = %q, want NO (INBOX must not be deletable)", name, i, status)
		}
	}
}

// TestCreateInboxCaseInsensitiveRejected: INBOX always exists, so CREATE of it
// in any case must fail with NO (RFC 3501 §6.3.3). A normal folder name — and a
// distinct-case variant of one — still creates fine.
func TestCreateInboxCaseInsensitiveRejected(t *testing.T) {
	mock := newMockBackend()
	h := newIMAPHarness(t, mock)
	h.login("a1")

	for _, name := range []string{"INBOX", "inbox", "Inbox", "iNBOx"} {
		_, status := h.command("c1", "CREATE %s", name)
		if !hasStatus(status, "NO") || hasStatus(status, "OK") {
			t.Errorf("CREATE %q: status = %q, want NO (INBOX cannot be created)", name, status)
		}
	}

	// Non-INBOX names are unaffected; case still distinguishes them.
	for _, name := range []string{"Work", "work"} {
		_, status := h.command("c2", "CREATE %s", name)
		if !hasStatus(status, "OK") {
			t.Errorf("CREATE %q: status = %q, want OK (normal folder)", name, status)
		}
	}
}

// TestSelectInboxCaseInsensitiveResolves: SELECT/EXAMINE of any-case "inbox"
// must resolve to the real INBOX, so the seeded messages are reported. On the
// buggy code the name passes through verbatim and the backend lookup misses.
func TestSelectInboxCaseInsensitiveResolves(t *testing.T) {
	for _, tc := range []struct{ cmd, name string }{
		{"SELECT", "inbox"},
		{"SELECT", "InBoX"},
		{"EXAMINE", "inbox"},
	} {
		mock := newMockBackend()
		mock.seed("INBOX", 5, 100, "Subject: a\r\n\r\nx\r\n")
		mock.seed("INBOX", 9, 100, "Subject: b\r\n\r\ny\r\n")
		h := newIMAPHarness(t, mock)
		h.login("a1")

		untagged, status := h.command("s1", "%s %s", tc.cmd, tc.name)
		if !hasStatus(status, "OK") {
			t.Errorf("%s %q: status = %q, want OK", tc.cmd, tc.name, status)
		}
		if !containsLine(untagged, "* 2 EXISTS") {
			t.Errorf("%s %q: untagged = %v, want a \"* 2 EXISTS\" (real INBOX)", tc.cmd, tc.name, untagged)
		}
	}
}

// TestStatusInboxCaseInsensitiveButOthersCaseSensitive: STATUS of any-case
// inbox resolves to the real INBOX, while a normal folder name stays
// case-sensitive ("Work" and "work" are different mailboxes).
func TestStatusInboxCaseInsensitiveButOthersCaseSensitive(t *testing.T) {
	mock := newMockBackend()
	mock.seed("INBOX", 5, 100, "")
	mock.seed("INBOX", 9, 100, "")
	mock.seed("Work", 7, 100, "")
	h := newIMAPHarness(t, mock)
	h.login("a1")

	// Any-case inbox -> the real INBOX (2 messages).
	for _, name := range []string{"INBOX", "inbox", "InBoX"} {
		untagged, status := h.command("t1", "STATUS %s (MESSAGES)", name)
		if !hasStatus(status, "OK") || !anyContains(untagged, "MESSAGES 2") {
			t.Errorf("STATUS %q: untagged = %v, status = %q, want MESSAGES 2", name, untagged, status)
		}
	}

	// "Work" is case-sensitive: exact case sees its message, other case does not.
	untagged, _ := h.command("t2", "STATUS Work (MESSAGES)")
	if !anyContains(untagged, "MESSAGES 1") {
		t.Errorf("STATUS Work: untagged = %v, want MESSAGES 1", untagged)
	}
	untagged, _ = h.command("t3", "STATUS work (MESSAGES)")
	if !anyContains(untagged, "MESSAGES 0") {
		t.Errorf("STATUS work: untagged = %v, want MESSAGES 0 (distinct from Work)", untagged)
	}
}

// TestRenameInboxSourceCaseInsensitive: RENAME's source-name guard forbids
// renaming INBOX; a lower/mixed-case source must be rejected the same way,
// while renaming a normal folder still works.
func TestRenameInboxSourceCaseInsensitive(t *testing.T) {
	mock := newMockBackend()
	h := newIMAPHarness(t, mock)
	h.login("a1")

	for _, name := range []string{"INBOX", "inbox", "InBoX"} {
		_, status := h.command("r1", "RENAME %s Archive", name)
		if !hasStatus(status, "NO") || hasStatus(status, "OK") {
			t.Errorf("RENAME %q Archive: status = %q, want NO (INBOX source protected)", name, status)
		}
	}

	// A non-standard folder renames fine.
	if _, status := h.command("r2", "RENAME Work Archive"); !hasStatus(status, "OK") {
		t.Errorf("RENAME Work Archive: status = %q, want OK", status)
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func anyContains(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
