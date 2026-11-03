package server

import (
	"strings"
	"testing"
)

// flagsInFetch returns the parenthesised FLAGS content from the first untagged
// FETCH line that carries one, e.g. `* 1 FETCH (FLAGS (\Seen \Flagged))` yields
// `\Seen \Flagged`.
func flagsInFetch(untagged []string) (string, bool) {
	for _, l := range untagged {
		if !strings.Contains(l, "FETCH") {
			continue
		}
		i := strings.Index(l, "FLAGS (")
		if i < 0 {
			continue
		}
		rest := l[i+len("FLAGS ("):]
		j := strings.Index(rest, ")")
		if j < 0 {
			continue
		}
		return rest[:j], true
	}
	return "", false
}

func hasFlag(flagStr, flag string) bool {
	for _, f := range strings.Fields(flagStr) {
		if f == flag {
			return true
		}
	}
	return false
}

func newStoreHarness(t *testing.T, uid uint32) *imapHarness {
	t.Helper()
	b := newMockBackend()
	b.seed("INBOX", uid, 100, "")
	h := newIMAPHarness(t, b)
	h.login("a1")
	h.selectInbox("a2")
	return h
}

// TestStore_ReplaceModeSetsExactFlags pins RFC 3501 §6.4.6: bare FLAGS replaces
// the flag set with exactly the given flags. After setting \Seen, a
// `FLAGS (\Flagged)` must leave the message with \Flagged only — \Seen cleared,
// \Flagged set. The bug treated replace-mode as removal.
func TestStore_ReplaceModeSetsExactFlags(t *testing.T) {
	h := newStoreHarness(t, 1)

	if _, st := h.command("a3", `STORE 1 +FLAGS (\Seen)`); !strings.Contains(st, " OK") {
		t.Fatalf("setup STORE +FLAGS status = %q", st)
	}

	untagged, status := h.command("a4", `STORE 1 FLAGS (\Flagged)`)
	if !strings.Contains(status, " OK") {
		t.Fatalf("STORE FLAGS status = %q", status)
	}
	got, ok := flagsInFetch(untagged)
	if !ok {
		t.Fatalf("no untagged FETCH in %v", untagged)
	}
	if !hasFlag(got, `\Flagged`) {
		t.Errorf("replace-mode result %q missing \\Flagged", got)
	}
	if hasFlag(got, `\Seen`) {
		t.Errorf("replace-mode result %q still has \\Seen; FLAGS must replace the whole set", got)
	}

	up, ok := h.mock.mbox.lastStore(1)
	if !ok {
		t.Fatalf("backend Store never called for replace-mode")
	}
	if up.Flagged == nil || !*up.Flagged {
		t.Errorf("replace-mode did not persist \\Flagged=true: %+v", up)
	}
	if up.Seen == nil || *up.Seen {
		t.Errorf("replace-mode did not persist \\Seen=false: %+v", up)
	}
}

// TestStore_SilentSuppressesUntagged pins the .SILENT suffix: the flag change is
// applied but no untagged FETCH is emitted (RFC 3501 §6.4.6).
func TestStore_SilentSuppressesUntagged(t *testing.T) {
	h := newStoreHarness(t, 1)

	untagged, status := h.command("a3", `STORE 1 +FLAGS.SILENT (\Seen)`)
	if !strings.Contains(status, " OK") {
		t.Fatalf("STORE +FLAGS.SILENT status = %q", status)
	}
	for _, l := range untagged {
		if strings.Contains(l, "FETCH") {
			t.Errorf(".SILENT must suppress the untagged FETCH, got %q", l)
		}
	}
	if !h.mock.mbox.wasMarkedRead(1) {
		t.Errorf(".SILENT still applies the change; \\Seen was not persisted")
	}
}

// TestStore_DraftFlag pins that \Draft (a modelled persistent flag advertised in
// PERMANENTFLAGS) is stored, not silently dropped.
func TestStore_DraftFlag(t *testing.T) {
	h := newStoreHarness(t, 1)

	untagged, status := h.command("a3", `STORE 1 +FLAGS (\Draft)`)
	if !strings.Contains(status, " OK") {
		t.Fatalf("STORE +FLAGS (\\Draft) status = %q", status)
	}
	got, ok := flagsInFetch(untagged)
	if !ok {
		t.Fatalf("no untagged FETCH in %v", untagged)
	}
	if !hasFlag(got, `\Draft`) {
		t.Errorf("result %q missing \\Draft", got)
	}
	up, ok := h.mock.mbox.lastStore(1)
	if !ok {
		t.Fatalf("backend Store never called for \\Draft")
	}
	if up.Draft == nil || !*up.Draft {
		t.Errorf("\\Draft not persisted: %+v", up)
	}
}

// TestStore_AnsweredFlag pins that \Answered (advertised in PERMANENTFLAGS) is
// stored and reported, not silently dropped.
func TestStore_AnsweredFlag(t *testing.T) {
	h := newStoreHarness(t, 1)

	untagged, status := h.command("a3", `STORE 1 +FLAGS (\Answered)`)
	if !strings.Contains(status, " OK") {
		t.Fatalf("STORE +FLAGS (\\Answered) status = %q", status)
	}
	got, ok := flagsInFetch(untagged)
	if !ok {
		t.Fatalf("no untagged FETCH in %v", untagged)
	}
	if !hasFlag(got, `\Answered`) {
		t.Errorf("result %q missing \\Answered", got)
	}
}

// TestStore_DeletedShownInFetch pins that a pending \Deleted appears in the
// untagged FETCH flag list (buildFlags previously omitted it).
func TestStore_DeletedShownInFetch(t *testing.T) {
	h := newStoreHarness(t, 1)

	untagged, status := h.command("a3", `STORE 1 +FLAGS (\Deleted)`)
	if !strings.Contains(status, " OK") {
		t.Fatalf("STORE +FLAGS (\\Deleted) status = %q", status)
	}
	got, ok := flagsInFetch(untagged)
	if !ok {
		t.Fatalf("no untagged FETCH in %v", untagged)
	}
	if !hasFlag(got, `\Deleted`) {
		t.Errorf("result %q missing \\Deleted; a pending deletion must show in FETCH", got)
	}
}

// TestStore_RemoveMode pins -FLAGS: only the listed flags are cleared, others
// stay.
func TestStore_RemoveMode(t *testing.T) {
	h := newStoreHarness(t, 1)

	if _, st := h.command("a3", `STORE 1 +FLAGS (\Seen \Flagged)`); !strings.Contains(st, " OK") {
		t.Fatalf("setup STORE status = %q", st)
	}
	untagged, status := h.command("a4", `STORE 1 -FLAGS (\Seen)`)
	if !strings.Contains(status, " OK") {
		t.Fatalf("STORE -FLAGS status = %q", status)
	}
	got, ok := flagsInFetch(untagged)
	if !ok {
		t.Fatalf("no untagged FETCH in %v", untagged)
	}
	if hasFlag(got, `\Seen`) {
		t.Errorf("-FLAGS (\\Seen) left \\Seen set: %q", got)
	}
	if !hasFlag(got, `\Flagged`) {
		t.Errorf("-FLAGS (\\Seen) wrongly cleared \\Flagged: %q", got)
	}
}

// TestUIDStore_ReplaceModeSetsExactFlags is the UID STORE twin of the replace-mode
// contract.
func TestUIDStore_ReplaceModeSetsExactFlags(t *testing.T) {
	h := newStoreHarness(t, 5)

	if _, st := h.command("a3", `UID STORE 5 +FLAGS (\Seen)`); !strings.Contains(st, " OK") {
		t.Fatalf("setup UID STORE status = %q", st)
	}
	untagged, status := h.command("a4", `UID STORE 5 FLAGS (\Flagged)`)
	if !strings.Contains(status, " OK") {
		t.Fatalf("UID STORE FLAGS status = %q", status)
	}
	got, ok := flagsInFetch(untagged)
	if !ok {
		t.Fatalf("no untagged FETCH in %v", untagged)
	}
	if !hasFlag(got, `\Flagged`) {
		t.Errorf("UID replace-mode result %q missing \\Flagged", got)
	}
	if hasFlag(got, `\Seen`) {
		t.Errorf("UID replace-mode result %q still has \\Seen", got)
	}
	up, ok := h.mock.mbox.lastStore(5)
	if !ok {
		t.Fatalf("backend Store never called for UID replace-mode")
	}
	if up.Flagged == nil || !*up.Flagged {
		t.Errorf("UID replace-mode did not persist \\Flagged=true: %+v", up)
	}
	if up.Seen == nil || *up.Seen {
		t.Errorf("UID replace-mode did not persist \\Seen=false: %+v", up)
	}
}
