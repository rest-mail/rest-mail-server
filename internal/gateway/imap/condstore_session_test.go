package imap

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// changeElsewhere is another client's flag change, made in the store as the API
// makes it: the message takes its folder's next mod-sequence.
func (f *fakeAPI) changeElsewhere(id uint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.msgs[id]
	m.isFlagged = !m.isFlagged
	m.modSeq = f.bump(m.folder)
}

// newCondstoreAPI is an INBOX of five messages, UIDs 1 to 5, stored one after the
// other: message n has mod-sequence n and the folder's HIGHESTMODSEQ is 5.
func newCondstoreAPI(t *testing.T) (*fakeAPI, *Backend) {
	t.Helper()
	api := newFakeAPI()
	for id := uint(1); id <= 5; id++ {
		api.seed(id, "INBOX", "Subject: m\r\n\r\nbody\r\n", "m")
	}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	return api, NewBackend(apiclient.New(srv.URL), nil)
}

// REST Mail's IMAP server advertises CONDSTORE (RFC 7162 §3.1): SELECT
// (CONDSTORE) reports the folder's HIGHESTMODSEQ from the API's folder listing,
// FETCH MODSEQ and CHANGEDSINCE read each message's mod_seq, and STORE
// UNCHANGEDSINCE sends the API's unchanged_since, so a message another client
// changed meanwhile is left alone and reported in MODIFIED.
func TestCondstoreThroughTheAPI(t *testing.T) {
	api, backend := newCondstoreAPI(t)
	c := newTranscript(t, backend)
	c.login("l1")
	if lines, _ := c.command("c1", "CAPABILITY"); len(lines) == 0 || !strings.Contains(lines[0], " CONDSTORE") || !strings.Contains(lines[0], " QRESYNC") {
		t.Fatalf("CAPABILITY: %v", lines)
	}
	lines, status := c.command("s1", "SELECT INBOX (CONDSTORE)")
	if !strings.HasPrefix(status, "s1 OK") || !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "* OK [HIGHESTMODSEQ 5]") }) {
		t.Fatalf("SELECT (CONDSTORE): %v %s", lines, status)
	}
	if lines, status = c.command("st1", "STATUS INBOX (HIGHESTMODSEQ)"); !slices.Contains(lines, `* STATUS "INBOX" (HIGHESTMODSEQ 5)`) {
		t.Fatalf("STATUS HIGHESTMODSEQ: %v %s", lines, status)
	}

	lines, status = c.command("f1", "FETCH 1:* (FLAGS) (CHANGEDSINCE 3)")
	if !strings.HasPrefix(status, "f1 OK") || !slices.Equal(lines, []string{
		"* 4 FETCH (UID 4 FLAGS () MODSEQ (4))",
		"* 5 FETCH (UID 5 FLAGS () MODSEQ (5))",
	}) {
		t.Fatalf("FETCH CHANGEDSINCE 3: %v %s", lines, status)
	}

	// Message 2 is changed by another client after this one last saw it.
	api.changeElsewhere(2)
	lines, status = c.command("u1", `STORE 1:2 (UNCHANGEDSINCE 5) +FLAGS.SILENT (\Seen)`)
	if status != "u1 OK [MODIFIED 2] Conditional STORE failed" {
		t.Fatalf("STORE UNCHANGEDSINCE: %v %s", lines, status)
	}
	if !slices.Contains(lines, "* 1 FETCH (UID 1 MODSEQ (7))") {
		t.Fatalf("STORE UNCHANGEDSINCE: the stored message's new MODSEQ is missing: %v", lines)
	}
	for _, p := range api.patchesFor(2) {
		if p["is_read"] == true && p["unchanged_since"] != float64(5) {
			t.Fatalf("message 2 was written without unchanged_since: %v", p)
		}
	}
	api.mu.Lock()
	read1, read2 := api.msgs[1].isRead, api.msgs[2].isRead
	api.mu.Unlock()
	if !read1 || read2 {
		t.Fatalf("after STORE UNCHANGEDSINCE: message 1 read %t, message 2 read %t; want true, false", read1, read2)
	}
	if got := api.modSeqOf(1); got != 7 {
		t.Fatalf("message 1 mod_seq %d, want 7", got)
	}

	// UNCHANGEDSINCE 0 always fails, whatever the message's mod-sequence.
	if _, status = c.command("u2", `STORE 3 (UNCHANGEDSINCE 0) +FLAGS.SILENT (\Seen)`); status != "u2 OK [MODIFIED 3] Conditional STORE failed" {
		t.Fatalf("STORE UNCHANGEDSINCE 0: %s", status)
	}
}

// REST Mail's IMAP server advertises QRESYNC (RFC 7162 §3.2): with QRESYNC
// enabled an EXPUNGE is reported as VANISHED, and a client that comes back with
// SELECT (QRESYNC (uidvalidity modseq)) learns, from the API's record of UIDs
// that left the folder, which messages vanished since, followed by the messages
// that changed since.
func TestQresyncThroughTheAPI(t *testing.T) {
	api, backend := newCondstoreAPI(t)

	first := newTranscript(t, backend)
	first.login("l1")
	if lines, status := first.command("e1", "ENABLE QRESYNC"); !slices.Contains(lines, "* ENABLED QRESYNC") {
		t.Fatalf("ENABLE QRESYNC: %v %s", lines, status)
	}
	if _, status := first.command("s1", "SELECT INBOX"); !strings.HasPrefix(status, "s1 OK") {
		t.Fatal(status)
	}
	if _, status := first.command("d1", `STORE 2:4 +FLAGS.SILENT (\Deleted)`); !strings.HasPrefix(status, "d1 OK") {
		t.Fatal(status)
	}
	lines, status := first.command("x1", "EXPUNGE")
	if !strings.HasPrefix(status, "x1 OK") || !slices.Contains(lines, "* VANISHED 2:4") || slices.ContainsFunc(lines, func(l string) bool { return strings.HasSuffix(l, " EXPUNGE") }) {
		t.Fatalf("EXPUNGE with QRESYNC enabled: %v %s", lines, status)
	}
	for _, id := range []uint{2, 3, 4} {
		if !api.deleted(id) {
			t.Fatalf("message %d was not deleted through the API", id)
		}
	}
	// Mod-sequences 1-5 were the stores, 6-8 the \Deleted flags, 9-11 the
	// expunges. Message 5 then changes elsewhere, at 12.
	api.changeElsewhere(5)

	// Another device last synced at mod-sequence 5: it hears that 2, 3 and 4
	// vanished (three UIDs, so the API answers in two pages) and that 5 changed.
	second := newTranscript(t, backend)
	second.login("l2")
	second.command("e2", "ENABLE QRESYNC")
	lines, status = second.command("s2", "SELECT INBOX (QRESYNC (1 5))")
	if !strings.HasPrefix(status, "s2 OK") {
		t.Fatalf("SELECT (QRESYNC): %v %s", lines, status)
	}
	vanished := slices.Index(lines, "* VANISHED (EARLIER) 2:4")
	fetched := slices.Index(lines, `* 2 FETCH (UID 5 FLAGS (\Flagged) MODSEQ (12))`)
	if vanished < 0 || fetched < vanished || !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "* OK [HIGHESTMODSEQ 12]") }) {
		t.Fatalf("SELECT (QRESYNC (1 5)): %v", lines)
	}
	if slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "* 1 FETCH") }) {
		t.Fatalf("SELECT (QRESYNC (1 5)) reported message 1, unchanged since 5: %v", lines)
	}

	// A client that synced after the expunges hears of none of them.
	third := newTranscript(t, backend)
	third.login("l3")
	third.command("e3", "ENABLE QRESYNC")
	lines, _ = third.command("s3", "SELECT INBOX (QRESYNC (1 11))")
	if slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "VANISHED") }) {
		t.Fatalf("SELECT (QRESYNC (1 11)) after the expunges: %v", lines)
	}
	// UID FETCH (CHANGEDSINCE n VANISHED) reads the same record.
	lines, _ = third.command("f3", "UID FETCH 1:* (FLAGS) (CHANGEDSINCE 8 VANISHED)")
	if len(lines) == 0 || lines[0] != "* VANISHED (EARLIER) 2:4" {
		t.Fatalf("UID FETCH (CHANGEDSINCE 8 VANISHED): %v", lines)
	}
}
