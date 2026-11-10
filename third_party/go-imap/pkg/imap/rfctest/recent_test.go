package rfctest

import (
	"slices"
	"sync"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// recentStore is an [imap.RecentMailbox] mark per folder, shared by every
// session given the same store, as a host's would be.
type recentStore struct {
	mu   sync.Mutex
	mark map[string]uint32
}

func newRecentStore() *recentStore { return &recentStore{mark: map[string]uint32{}} }

func (r *recentStore) RecentMark(folder string) (uint32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mark[folder], nil
}

func (r *recentStore) ClaimRecent(folder string, upTo uint32) (uint32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.mark[folder]
	r.mark[folder] = max(prev, upTo)
	return prev, nil
}

func (r *recentStore) markOf(folder string) uint32 {
	mark, _ := r.RecentMark(folder)
	return mark
}

// recentMailbox is a selectionMailbox whose \Recent state lives in a
// recentStore.
type recentMailbox struct {
	*selectionMailbox
	*recentStore
}

// RecentMark and ClaimRecent come from the store, not the harness mailbox,
// which has every message claimed already.
func (m *recentMailbox) RecentMark(folder string) (uint32, error) {
	return m.recentStore.RecentMark(folder)
}

func (m *recentMailbox) ClaimRecent(folder string, upTo uint32) (uint32, error) {
	return m.recentStore.ClaimRecent(folder, upTo)
}

func newRecentMailbox(messages ...imap.Message) *recentMailbox {
	return &recentMailbox{
		selectionMailbox: &selectionMailbox{folders: map[string][]imap.Message{"INBOX": messages}},
		recentStore:      newRecentStore(),
	}
}

// recentClient logs in to box and runs selection ("SELECT" or "EXAMINE") on
// INBOX, returning the untagged responses the selection sent.
func recentClient(t *testing.T, box imap.Mailbox, selection string) (*client, []string) {
	t.Helper()
	c := selectionClient(t, box)
	lines, tagged := c.command("S1", "%s INBOX", selection)
	requireOK(t, tagged, "S1")
	return c, lines
}

// TestRecentClaimedByFirstSelect: a message is \Recent in one session only, the
// first read-write selection after it arrived. EXAMINE and STATUS report the
// state and leave it for the next session; SELECT claims it, so a later
// selection sees nothing recent while the claiming session still does (RFC
// 3501 §§2.3.2, 6.3.1, 6.3.2, 6.3.10, 7.3.2).
//
// checklist: IMAP-SELECT-08 IMAP-STATUS-07 IMAP-RESP-15 IMAP-INV-FLAGS-04
func TestRecentClaimedByFirstSelect(t *testing.T) {
	box := newRecentMailbox(imap.Message{UID: 1, Seen: true}, imap.Message{UID: 2}, imap.Message{UID: 3})

	// EXAMINE reports all three and claims none.
	examiner, lines := recentClient(t, box, "EXAMINE")
	requireLine(t, lines, "* 3 RECENT")
	fetch, tagged := examiner.command("F1", "FETCH 1:3 FLAGS")
	requireOK(t, tagged, "F1")
	requireExactly(t, fetch, `* 1 FETCH (UID 1 FLAGS (\Seen \Recent))`, `* 2 FETCH (UID 2 FLAGS (\Recent))`, `* 3 FETCH (UID 3 FLAGS (\Recent))`)
	if mark := box.markOf("INBOX"); mark != 0 {
		t.Fatalf("EXAMINE moved the mark to %d", mark)
	}

	// STATUS reports them too, and claims nothing either.
	status, tagged := examiner.command("T1", "STATUS INBOX (RECENT)")
	requireOK(t, tagged, "T1")
	requireExactly(t, status, `* STATUS "INBOX" (RECENT 3)`)
	if mark := box.markOf("INBOX"); mark != 0 {
		t.Fatalf("STATUS moved the mark to %d", mark)
	}

	// The first SELECT is told of them first: they are recent to it.
	first, lines := recentClient(t, box, "SELECT")
	requireLine(t, lines, "* 3 RECENT")
	if mark := box.markOf("INBOX"); mark != 3 {
		t.Fatalf("SELECT left the mark at %d, want 3", mark)
	}

	// A second SELECT, and STATUS, find nothing recent.
	second, lines := recentClient(t, box, "SELECT")
	requireLine(t, lines, "* 0 RECENT")
	fetch, tagged = second.command("F2", "FETCH 1:3 FLAGS")
	requireOK(t, tagged, "F2")
	requireExactly(t, fetch, `* 1 FETCH (UID 1 FLAGS (\Seen))`, `* 2 FETCH (UID 2 FLAGS ())`, `* 3 FETCH (UID 3 FLAGS ())`)
	status, tagged = second.command("T2", "STATUS INBOX (RECENT MESSAGES)")
	requireOK(t, tagged, "T2")
	requireExactly(t, status, `* STATUS "INBOX" (RECENT 0 MESSAGES 3)`)

	// The claiming session keeps its \Recent messages.
	fetch, tagged = first.command("F3", "FETCH 2 FLAGS")
	requireOK(t, tagged, "F3")
	requireExactly(t, fetch, `* 2 FETCH (UID 2 FLAGS (\Recent))`)
	if box.stores+box.deletes+box.moves != 0 {
		t.Fatal("\\Recent state changed stored flags")
	}
}

// TestRecentArrivalDuringSelection: a message that arrives while a session has
// the mailbox selected is recent to the first read-write session told of it,
// which reports the new count with RECENT after EXISTS (RFC 3501 §§2.3.2,
// 7.3.2). A read-only session told first sees it as recent without claiming
// it, and a session told after the claim sees no RECENT change.
//
// checklist: IMAP-INV-FLAGS-04 IMAP-RESP-15
func TestRecentArrivalDuringSelection(t *testing.T) {
	box := newRecentMailbox(imap.Message{UID: 1}, imap.Message{UID: 2})
	writer, lines := recentClient(t, box, "SELECT")
	requireLine(t, lines, "* 2 RECENT")
	reader, lines := recentClient(t, box, "EXAMINE")
	requireLine(t, lines, "* 0 RECENT")

	box.folders["INBOX"] = append(slices.Clone(box.folders["INBOX"]), imap.Message{UID: 3})

	noop, tagged := reader.command("N1", "NOOP")
	requireOK(t, tagged, "N1")
	requireExactly(t, noop, "* 3 EXISTS", "* 1 RECENT")
	if mark := box.markOf("INBOX"); mark != 2 {
		t.Fatalf("a read-only session moved the mark to %d", mark)
	}

	noop, tagged = writer.command("N2", "NOOP")
	requireOK(t, tagged, "N2")
	requireExactly(t, noop, "* 3 EXISTS", "* 3 RECENT")
	if mark := box.markOf("INBOX"); mark != 3 {
		t.Fatalf("the read-write session left the mark at %d, want 3", mark)
	}
	search, tagged := writer.command("Q1", "UID SEARCH RECENT")
	requireOK(t, tagged, "Q1")
	requireExactly(t, search, "* SEARCH 1 2 3")

	box.folders["INBOX"] = append(slices.Clone(box.folders["INBOX"]), imap.Message{UID: 4})
	later, lines := recentClient(t, box, "SELECT")
	requireLine(t, lines, "* 1 RECENT")
	box.folders["INBOX"] = append(slices.Clone(box.folders["INBOX"]), imap.Message{UID: 5})
	noop, tagged = writer.command("N3", "NOOP")
	requireOK(t, tagged, "N3")
	requireExactly(t, noop, "* 5 EXISTS", "* 4 RECENT")
	// UID 5 is already claimed: the later session hears of it with no RECENT.
	noop, tagged = later.command("N4", "NOOP")
	requireOK(t, tagged, "N4")
	requireExactly(t, noop, "* 5 EXISTS")
}

// TestSearchRecentNewOld: RECENT matches this session's \Recent messages, NEW
// those that are also unseen, and OLD those that are not recent, which is not
// NOT NEW (RFC 3501 §6.4.4). A session that selects after the claim finds
// nothing recent or new, and every message old.
//
// checklist: IMAP-SEARCH-12
func TestSearchRecentNewOld(t *testing.T) {
	box := newRecentMailbox(imap.Message{UID: 1}, imap.Message{UID: 5, Seen: true}, imap.Message{UID: 9}, imap.Message{UID: 12, Seen: true})
	box.mark["INBOX"] = 1

	first, _ := recentClient(t, box, "SELECT")
	for _, tc := range []struct{ query, seqs string }{
		{"RECENT", "2 3 4"},
		{"NEW", "3"},
		{"OLD", "1"},
		{"NOT NEW", "1 2 4"},
		{"NOT RECENT", "1"},
		{"RECENT SEEN", "2 4"},
		{"OR NEW OLD", "1 3"},
	} {
		requireSearch(t, first, "SEARCH "+tc.query, "* SEARCH "+tc.seqs)
	}

	second, _ := recentClient(t, box, "SELECT")
	requireSearch(t, second, "SEARCH RECENT", "* SEARCH")
	requireSearch(t, second, "SEARCH NEW", "* SEARCH")
	requireSearch(t, second, "SEARCH OLD", "* SEARCH 1 2 3 4")
}

// forgetfulMailbox hides every optional interface of the Mailbox it wraps,
// RecentMailbox among them.
type forgetfulMailbox struct{ imap.Mailbox }

// TestRecentWithoutState: a store that keeps no \Recent state cannot say which
// session was told of a message first, so every message is recent to every
// session, as RFC 3501 §2.3.2 asks: "If it is not possible to determine
// whether or not this session is the first session to be notified about a
// message, then that message SHOULD be considered recent."
//
// checklist: IMAP-INV-FLAGS-02
func TestRecentWithoutState(t *testing.T) {
	inner := newRecentMailbox(imap.Message{UID: 1, Seen: true}, imap.Message{UID: 2})
	box := forgetfulMailbox{inner.selectionMailbox}
	for range 2 {
		c, lines := recentClient(t, box, "SELECT")
		requireLine(t, lines, "* 2 RECENT")
		requireSearch(t, c, "SEARCH RECENT", "* SEARCH 1 2")
		status, tagged := c.command("T1", "STATUS INBOX (RECENT)")
		requireOK(t, tagged, "T1")
		requireExactly(t, status, `* STATUS "INBOX" (RECENT 2)`)
	}
}
