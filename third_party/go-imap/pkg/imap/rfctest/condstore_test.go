package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// modSeqMailbox is a store that keeps mod-sequences (RFC 7162 §3.1) and
// remembers the mod-sequence at which each UID left INBOX (§3.2). Every
// change to a message, and every expunge, raises the folder's counter.
type modSeqMailbox struct {
	mailbox
	messages []imap.Message
	highest  uint64
	vanished map[uint32]uint64 // UID → mod-sequence of its expunge
	noModSeq bool              // HighestModSeq answers 0: NOMODSEQ
	moved    map[uint32]string
}

func newModSeqMailbox(messages ...imap.Message) *modSeqMailbox {
	m := &modSeqMailbox{messages: messages, vanished: map[uint32]uint64{}, moved: map[uint32]string{}}
	for _, msg := range messages {
		m.highest = max(m.highest, msg.ModSeq)
	}
	return m
}

func (m *modSeqMailbox) Messages(folder string) ([]imap.Message, error) {
	if folder != "INBOX" {
		return nil, nil
	}
	return slices.Clone(m.messages), nil
}

func (m *modSeqMailbox) UIDValidity(string) (uint32, error) { return 67890007, nil }
func (m *modSeqMailbox) AppendUID(string, imap.FlagUpdate, []byte) (uint32, error) {
	return 0, nil
}
func (m *modSeqMailbox) CopyUID(uint32, string) (uint32, error) { return 0, nil }

// UIDNext counts expunged UIDs, as a store that keeps mod-sequences would.
func (m *modSeqMailbox) UIDNext(string) (uint32, error) {
	var next uint32
	for _, msg := range m.messages {
		next = max(next, msg.UID+1)
	}
	for uid := range m.vanished {
		next = max(next, uid+1)
	}
	return next, nil
}

func (m *modSeqMailbox) HighestModSeq(string) (uint64, error) {
	if m.noModSeq {
		return 0, nil
	}
	return m.highest, nil
}

func (m *modSeqMailbox) Vanished(_ string, since uint64) ([]uint32, error) {
	var uids []uint32
	for uid, modSeq := range m.vanished {
		if modSeq > since {
			uids = append(uids, uid)
		}
	}
	slices.Sort(uids)
	return uids, nil
}

func (m *modSeqMailbox) index(uid uint32) int {
	return slices.IndexFunc(m.messages, func(msg imap.Message) bool { return msg.UID == uid })
}

// StoreModSeq changes the flags and raises the mod-sequence only when
// something changed (RFC 7162 §3.1.11).
func (m *modSeqMailbox) StoreModSeq(uid uint32, f imap.FlagUpdate, since uint64) (uint64, error) {
	i := m.index(uid)
	msg := &m.messages[i]
	if since != 0 && msg.ModSeq > since {
		return msg.ModSeq, imap.ErrModified
	}
	before := *msg
	set := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	set(&msg.Seen, f.Seen)
	set(&msg.Answered, f.Answered)
	set(&msg.Flagged, f.Flagged)
	set(&msg.Draft, f.Draft)
	set(&msg.Deleted, f.Deleted)
	if f.KeywordsSet {
		msg.Keywords = slices.Clone(f.Keywords)
	}
	if !slices.Equal(before.Keywords, msg.Keywords) || before.Seen != msg.Seen || before.Answered != msg.Answered ||
		before.Flagged != msg.Flagged || before.Draft != msg.Draft || before.Deleted != msg.Deleted {
		m.highest++
		msg.ModSeq = m.highest
	}
	return msg.ModSeq, nil
}

func (m *modSeqMailbox) Store(uid uint32, f imap.FlagUpdate) error {
	_, err := m.StoreModSeq(uid, f, 0)
	return err
}

func (m *modSeqMailbox) Delete(uid uint32) error {
	i := m.index(uid)
	m.messages = slices.Delete(m.messages, i, i+1)
	m.highest++
	m.vanished[uid] = m.highest
	return nil
}

func (m *modSeqMailbox) Move(uid uint32, dest string) error {
	m.moved[uid] = dest
	return m.Delete(uid)
}

// change makes a flag change as another session would: the store raises the
// message's mod-sequence without this session knowing.
func (m *modSeqMailbox) change(uid uint32, f imap.FlagUpdate) {
	_, _ = m.StoreModSeq(uid, f, 0)
}

// condstoreMailbox holds UIDs 4, 6, 8, 9 and 10 at sequence numbers 1 to 5,
// with mod-sequences that are not in UID order.
func condstoreMailbox() *modSeqMailbox {
	return newModSeqMailbox(
		imap.Message{UID: 4, Seen: true, ModSeq: 65402},
		imap.Message{UID: 6, Deleted: true, ModSeq: 75403},
		imap.Message{UID: 8, ModSeq: 29738, Keywords: []string{"$NoJunk"}},
		imap.Message{UID: 9, ModSeq: 12000},
		imap.Message{UID: 10, Answered: true, ModSeq: 12001},
	)
}

func condstoreClient(t *testing.T, m imap.Mailbox, selectArgs string) *client {
	t.Helper()
	c := selectionClient(t, m)
	_, tagged := c.command("S1", "SELECT INBOX%s", selectArgs)
	requireOK(t, tagged, "S1")
	return c
}

func capabilityList(t *testing.T, c *client) []string {
	t.Helper()
	lines, tagged := c.command("C", "CAPABILITY")
	requireOK(t, tagged, "C")
	return strings.Fields(strings.TrimPrefix(lines[0], "* CAPABILITY "))
}

// TestCondstoreCapability: a store that keeps mod-sequences and expunged UIDs
// advertises CONDSTORE and QRESYNC; one without the interfaces advertises
// neither, and its STATUS, FETCH and SELECT refuse the new items (RFC 7162
// §§3.1.1, 3.2.3).
//
// checklist: IMAP-CONDSTORE-01
func TestCondstoreCapability(t *testing.T) {
	caps := capabilityList(t, selectionClient(t, condstoreMailbox()))
	if !slices.Contains(caps, "CONDSTORE") || !slices.Contains(caps, "QRESYNC") {
		t.Fatalf("capabilities %q lack CONDSTORE and QRESYNC", caps)
	}
	plain := selectionClient(t, newExpungeMailbox(2))
	caps = capabilityList(t, plain)
	if slices.Contains(caps, "CONDSTORE") || slices.Contains(caps, "QRESYNC") {
		t.Fatalf("a store without mod-sequences advertises %q", caps)
	}
	for _, cmd := range []string{"STATUS INBOX (HIGHESTMODSEQ)", "SELECT INBOX (CONDSTORE)"} {
		if _, tagged := plain.command("B", "%s", cmd); !strings.HasPrefix(tagged, "B BAD") {
			t.Fatalf("%s without CONDSTORE: %q, want BAD", cmd, tagged)
		}
	}
	lines, tagged := plain.command("E", "ENABLE CONDSTORE QRESYNC")
	requireOK(t, tagged, "E")
	requireExactly(t, lines, "* ENABLED")
	_, tagged = plain.command("S", "SELECT INBOX")
	requireOK(t, tagged, "S")
	if _, tagged := plain.command("F", "FETCH 1 (MODSEQ)"); !strings.HasPrefix(tagged, "F BAD") {
		t.Fatalf("FETCH MODSEQ without CONDSTORE: %q, want BAD", tagged)
	}
}

// TestCondstoreSelectHighestModSeq follows RFC 7162 §3.1.2.1 Example 1 and
// §3.1.8 Example 18: before any CONDSTORE enabling command SELECT reports no
// HIGHESTMODSEQ; SELECT (CONDSTORE) does, and so does every SELECT and
// EXAMINE after it. Reselecting answers CLOSED first (§3.2.11).
//
// checklist: IMAP-CONDSTORE-02 IMAP-CONDSTORE-11
func TestCondstoreSelectHighestModSeq(t *testing.T) {
	c := selectionClient(t, condstoreMailbox())
	lines, tagged := c.command("A141", "SELECT INBOX")
	requireOK(t, tagged, "A141")
	for _, line := range lines {
		if strings.Contains(line, "MODSEQ") {
			t.Fatalf("SELECT before CONDSTORE was enabled sent %q", line)
		}
	}
	lines, tagged = c.command("A142", "SELECT INBOX (CONDSTORE)")
	requireOK(t, tagged, "A142")
	if lines[0] != "* OK [CLOSED] Previous mailbox is now closed" {
		t.Fatalf("reselect began %q, want CLOSED", lines[0])
	}
	requireLine(t, lines, "* OK [HIGHESTMODSEQ 75403] Highest")
	lines, tagged = c.command("A143", "EXAMINE INBOX")
	requireOK(t, tagged, "A143")
	requireLine(t, lines, "* OK [HIGHESTMODSEQ 75403] Highest")
	for _, bad := range []string{"SELECT INBOX (CONDSTORE CONDSTORE)", "SELECT INBOX (FOO)", "SELECT INBOX ()", "SELECT INBOX (QRESYNC (1 2))"} {
		if _, tagged := c.command("B", "%s", bad); !strings.HasPrefix(tagged, "B BAD") {
			t.Fatalf("%s: %q, want BAD", bad, tagged)
		}
	}
}

// TestCondstoreNoModSeq follows RFC 7162 §3.1.2.2 Example 2: a mailbox
// without mod-sequences answers NOMODSEQ, STATUS HIGHESTMODSEQ is 0 (§3.1.7),
// and FETCH MODSEQ, CHANGEDSINCE, STORE UNCHANGEDSINCE and SEARCH MODSEQ are
// BAD while it stays selected.
//
// checklist: IMAP-CONDSTORE-03
func TestCondstoreNoModSeq(t *testing.T) {
	m := condstoreMailbox()
	m.noModSeq = true
	c := condstoreClient(t, m, " (CONDSTORE)")
	lines, tagged := c.command("A142", "SELECT INBOX")
	requireOK(t, tagged, "A142")
	requireLine(t, lines, "* OK [NOMODSEQ] Sorry, this mailbox format doesn't support modsequences")
	lines, tagged = c.command("T", "STATUS INBOX (HIGHESTMODSEQ)")
	requireOK(t, tagged, "T")
	requireExactly(t, lines, `* STATUS "INBOX" (HIGHESTMODSEQ 0)`)
	for _, cmd := range []string{
		"FETCH 1 (MODSEQ)",
		"FETCH 1:* (FLAGS) (CHANGEDSINCE 1)",
		"UID FETCH 1:* (FLAGS) (CHANGEDSINCE 1)",
		`STORE 1 (UNCHANGEDSINCE 5) +FLAGS (\Seen)`,
		"SEARCH MODSEQ 1",
	} {
		if _, tagged := c.command("B", "%s", cmd); !strings.HasPrefix(tagged, "B BAD") {
			t.Fatalf("%s on a NOMODSEQ mailbox: %q, want BAD", cmd, tagged)
		}
	}
}

// TestCondstoreStoreUnchangedSince follows RFC 7162 §3.1.3 Examples 3, 6 and
// 8: a conditional STORE changes only the messages not modified since the
// given mod-sequence, sends a FETCH with MODSEQ for each even with .SILENT,
// and lists the rest in MODIFIED, as sequence numbers or, for UID STORE, as
// UIDs. UNCHANGEDSINCE 0 always fails.
//
// checklist: IMAP-CONDSTORE-04 IMAP-CONDSTORE-05
func TestCondstoreStoreUnchangedSince(t *testing.T) {
	m := condstoreMailbox()
	c := condstoreClient(t, m, "")

	// Example 3: every message passes. The first enabling command reports
	// HIGHESTMODSEQ first (Example 5).
	lines, tagged := c.command("a103", `UID STORE 6,4,8 (UNCHANGEDSINCE 75403) +FLAGS.SILENT (\Deleted)`)
	requireOK(t, tagged, "a103")
	requireExactly(t, lines,
		"* OK [HIGHESTMODSEQ 75403] Highest",
		"* 2 FETCH (UID 6 MODSEQ (75403))",
		"* 1 FETCH (UID 4 MODSEQ (75404))",
		"* 3 FETCH (UID 8 MODSEQ (75405))",
	)
	if tagged != "a103 OK UID STORE completed" {
		t.Fatalf("a103: %q", tagged)
	}

	// Example 6: messages 1 and 3 changed after 70000, so only 4 is stored.
	lines, tagged = c.command("d105", `STORE 1,4,3 (UNCHANGEDSINCE 70000) +FLAGS.SILENT (\Flagged)`)
	requireExactly(t, lines, "* 4 FETCH (UID 9 MODSEQ (75406))")
	if tagged != "d105 OK [MODIFIED 1,3] Conditional STORE failed" {
		t.Fatalf("d105: %q", tagged)
	}
	if m.messages[0].Flagged || m.messages[2].Flagged || !m.messages[3].Flagged {
		t.Fatalf("store changed the wrong messages: %+v", m.messages)
	}

	// A UID STORE names the UIDs that failed; without .SILENT, FLAGS too.
	lines, tagged = c.command("d106", `UID STORE 4,10 (UNCHANGEDSINCE 70000) +FLAGS (\Draft)`)
	requireExactly(t, lines, "* 5 FETCH (UID 10 FLAGS (\\Answered \\Draft) MODSEQ (75407))")
	if tagged != "d106 OK [MODIFIED 4] Conditional STORE failed" {
		t.Fatalf("d106: %q", tagged)
	}

	// Example 8: UNCHANGEDSINCE 0 fails for every message.
	lines, tagged = c.command("a102", `STORE 5 (UNCHANGEDSINCE 0) +FLAGS.SILENT ($MDNSent)`)
	requireExactly(t, lines)
	if tagged != "a102 OK [MODIFIED 5] Conditional STORE failed" {
		t.Fatalf("a102: %q", tagged)
	}

	for _, bad := range []string{
		`STORE 1 (UNCHANGEDSINCE) +FLAGS (\Seen)`,
		`STORE 1 (UNCHANGEDSINCE x) +FLAGS (\Seen)`,
		`STORE 1 (UNCHANGEDSINCE 9223372036854775808) +FLAGS (\Seen)`,
		`STORE 1 (CHANGEDSINCE 1) +FLAGS (\Seen)`,
	} {
		if _, tagged := c.command("B", "%s", bad); !strings.HasPrefix(tagged, "B BAD") {
			t.Fatalf("%s: %q, want BAD", bad, tagged)
		}
	}
}

// TestCondstoreStoreReportsModSeq: once CONDSTORE is on, a plain STORE and a
// FETCH that sets \Seen report the new MODSEQ with UID, and NOOP reports a
// change made elsewhere the same way (RFC 7162 §3.1.3, §3.1.4.2 Example 14,
// §3.2.4).
//
// checklist: IMAP-CONDSTORE-06
func TestCondstoreStoreReportsModSeq(t *testing.T) {
	m := condstoreMailbox()
	c := condstoreClient(t, m, " (CONDSTORE)")
	lines, tagged := c.command("A160", `STORE 3 +FLAGS.SILENT (\Deleted)`)
	requireOK(t, tagged, "A160")
	requireExactly(t, lines, "* 3 FETCH (UID 8 MODSEQ (75404))")

	if _, err := fmt.Fprintf(c.conn, "A161 FETCH 4 (BODY[TEXT])\r\n"); err != nil {
		t.Fatal(err)
	}
	if head := c.line(); head != `* 4 FETCH (UID 9 FLAGS (\Seen) MODSEQ (75405) BODY[TEXT] {5}` {
		t.Fatalf("FETCH that set \\Seen: %q", head)
	}
	for line := c.line(); !strings.HasPrefix(line, "A161 "); line = c.line() {
	}

	m.change(10, imap.FlagUpdate{Flagged: boolPtr(true)})
	lines, tagged = c.command("C180", "NOOP")
	requireOK(t, tagged, "C180")
	requireExactly(t, lines, `* 5 FETCH (UID 10 FLAGS (\Answered \Flagged) MODSEQ (75406))`)
}

func boolPtr(b bool) *bool { return &b }

// TestCondstoreFetch follows RFC 7162 §3.1.4 Examples 12 and 13: FETCH
// MODSEQ returns each message's mod-sequence, and CHANGEDSINCE returns only
// the messages changed since, with MODSEQ added.
//
// checklist: IMAP-CONDSTORE-07
func TestCondstoreFetch(t *testing.T) {
	c := condstoreClient(t, condstoreMailbox(), "")
	lines, tagged := c.command("a", "FETCH 1:3 (MODSEQ)")
	requireOK(t, tagged, "a")
	requireExactly(t, lines,
		"* OK [HIGHESTMODSEQ 75403] Highest",
		`* 1 FETCH (UID 4 FLAGS (\Seen) MODSEQ (65402))`,
		`* 2 FETCH (UID 6 FLAGS (\Deleted) MODSEQ (75403))`,
		`* 3 FETCH (UID 8 FLAGS ($NoJunk) MODSEQ (29738))`,
	)
	lines, tagged = c.command("s100", "UID FETCH 1:* (FLAGS) (CHANGEDSINCE 12345)")
	requireOK(t, tagged, "s100")
	requireExactly(t, lines,
		`* 1 FETCH (UID 4 FLAGS (\Seen) MODSEQ (65402))`,
		`* 2 FETCH (UID 6 FLAGS (\Deleted) MODSEQ (75403))`,
		`* 3 FETCH (UID 8 FLAGS ($NoJunk) MODSEQ (29738))`,
	)
	lines, tagged = c.command("s101", "FETCH 1:* (FLAGS) (CHANGEDSINCE 65402)")
	requireOK(t, tagged, "s101")
	requireExactly(t, lines, `* 2 FETCH (UID 6 FLAGS (\Deleted) MODSEQ (75403))`)
	for _, bad := range []string{
		"FETCH 1 (FLAGS) (CHANGEDSINCE 0)",
		"FETCH 1 (FLAGS) (CHANGEDSINCE)",
		"FETCH 1 (FLAGS) (CHANGEDSINCE 1 CHANGEDSINCE 2)",
		"FETCH 1 (FLAGS) (CHANGEDSINCE 1 VANISHED)",
		"UID FETCH 1 (FLAGS) (CHANGEDSINCE 1 VANISHED)",
	} {
		if _, tagged := c.command("B", "%s", bad); !strings.HasPrefix(tagged, "B BAD") {
			t.Fatalf("%s: %q, want BAD", bad, tagged)
		}
	}
}

// TestCondstoreSearchModSeq follows RFC 7162 §3.1.5 Examples 15 and 16 and
// §3.1.10 Example 19: SEARCH MODSEQ matches mod-sequences equal to or above
// the value, ignores the entry name and type, and ends a non-empty result
// with the highest mod-sequence returned; ESEARCH carries it as MODSEQ.
//
// checklist: IMAP-CONDSTORE-08 IMAP-ESEARCH-06
func TestCondstoreSearchModSeq(t *testing.T) {
	c := condstoreClient(t, condstoreMailbox(), " (CONDSTORE)")
	lines, tagged := c.command("a", `SEARCH MODSEQ "/flags/\\draft" all 29738`)
	requireOK(t, tagged, "a")
	requireExactly(t, lines, "* SEARCH 1 2 3 (MODSEQ 75403)")
	lines, tagged = c.command("t", "SEARCH OR NOT MODSEQ 12000 LARGER 50000")
	requireOK(t, tagged, "t")
	requireExactly(t, lines, "* SEARCH")
	lines, tagged = c.command("u", "UID SEARCH MODSEQ 12001 UNSEEN")
	requireOK(t, tagged, "u")
	requireExactly(t, lines, "* SEARCH 6 8 10 (MODSEQ 75403)")
	lines, tagged = c.command("e", "SEARCH RETURN (ALL) MODSEQ 12001")
	requireOK(t, tagged, "e")
	requireExactly(t, lines, `* ESEARCH (TAG "e") ALL 1:3,5 MODSEQ 75403`)
	lines, tagged = c.command("f", "SEARCH RETURN (MIN) MODSEQ 12001")
	requireOK(t, tagged, "f")
	requireExactly(t, lines, `* ESEARCH (TAG "f") MIN 1 MODSEQ 65402`)
	for _, bad := range []string{"SEARCH MODSEQ", "SEARCH MODSEQ x", `SEARCH MODSEQ "/flags/\\draft" 5`, `SEARCH MODSEQ "draft" all 5`} {
		if _, tagged := c.command("B", "%s", bad); !strings.HasPrefix(tagged, "B BAD") {
			t.Fatalf("%s: %q, want BAD", bad, tagged)
		}
	}
}

// TestCondstoreStatus follows RFC 7162 §3.1.7 Example 17: STATUS
// HIGHESTMODSEQ reports the folder's HIGHESTMODSEQ, in the order asked.
//
// checklist: IMAP-CONDSTORE-09
func TestCondstoreStatus(t *testing.T) {
	c := selectionClient(t, condstoreMailbox())
	lines, tagged := c.command("A042", "STATUS INBOX (MESSAGES HIGHESTMODSEQ)")
	requireOK(t, tagged, "A042")
	requireExactly(t, lines, `* STATUS "INBOX" (MESSAGES 5 HIGHESTMODSEQ 75403)`)
}

// TestCondstoreEnable: ENABLE CONDSTORE and ENABLE QRESYNC answer ENABLED
// once each; QRESYNC turns CONDSTORE on too, and CONDSTORE enabled with a
// mailbox selected reports its HIGHESTMODSEQ (RFC 7162 §§3.1, 3.2.3; RFC 5161
// §3.1).
//
// checklist: IMAP-CONDSTORE-10 IMAP-QRESYNC-01
func TestCondstoreEnable(t *testing.T) {
	c := condstoreClient(t, condstoreMailbox(), "")
	lines, tagged := c.command("E1", "ENABLE CONDSTORE")
	requireOK(t, tagged, "E1")
	requireExactly(t, lines, "* OK [HIGHESTMODSEQ 75403] Highest", "* ENABLED CONDSTORE")
	lines, tagged = c.command("E2", "ENABLE CONDSTORE QRESYNC")
	requireOK(t, tagged, "E2")
	requireExactly(t, lines, "* ENABLED QRESYNC")

	c = selectionClient(t, condstoreMailbox())
	lines, tagged = c.command("A01", "ENABLE QRESYNC")
	requireOK(t, tagged, "A01")
	requireExactly(t, lines, "* ENABLED QRESYNC")
	lines, tagged = c.command("A02", "SELECT INBOX")
	requireOK(t, tagged, "A02")
	requireLine(t, lines, "* OK [HIGHESTMODSEQ 75403] Highest")
}

// qresyncClient enables QRESYNC before selecting INBOX with selectArgs.
func qresyncClient(t *testing.T, m imap.Mailbox, selectArgs string) (*client, []string) {
	t.Helper()
	c := selectionClient(t, m)
	_, tagged := c.command("A01", "ENABLE QRESYNC")
	requireOK(t, tagged, "A01")
	lines, tagged := c.command("A03", "SELECT INBOX%s", selectArgs)
	requireOK(t, tagged, "A03")
	return c, lines
}

// TestQresyncSelect follows RFC 7162 §3.2.5's examples: SELECT (QRESYNC ...)
// reports the known UIDs expunged since the given mod-sequence with VANISHED
// (EARLIER), then a FETCH with UID, FLAGS and MODSEQ for each known message
// changed since, after the usual SELECT responses. A UIDVALIDITY mismatch
// reports neither; QRESYNC before ENABLE QRESYNC is BAD.
//
// checklist: IMAP-QRESYNC-02 IMAP-QRESYNC-03
func TestQresyncSelect(t *testing.T) {
	m := condstoreMailbox()
	m.vanished = map[uint32]uint64{1: 100, 2: 40000, 3: 70000, 5: 80000}
	m.highest = 80000

	c := selectionClient(t, m)
	if _, tagged := c.command("B", "SELECT INBOX (QRESYNC (67890007 1))"); !strings.HasPrefix(tagged, "B BAD") {
		t.Fatalf("QRESYNC before ENABLE QRESYNC: %q, want BAD", tagged)
	}

	_, lines := qresyncClient(t, m, " (QRESYNC (67890007 30000 2:8))")
	n := len(lines)
	if n < 4 || !strings.HasPrefix(lines[n-4], "* OK [PERMANENTFLAGS") {
		t.Fatalf("resync responses must follow SELECT's own: %q", lines)
	}
	requireExactly(t, lines[n-3:],
		"* VANISHED (EARLIER) 2:3,5",
		`* 1 FETCH (UID 4 FLAGS (\Seen) MODSEQ (65402))`,
		`* 2 FETCH (UID 6 FLAGS (\Deleted) MODSEQ (75403))`,
	)
	requireLine(t, lines, "* OK [HIGHESTMODSEQ 80000] Highest")

	// Without known UIDs every UID below UIDNEXT counts.
	_, lines = qresyncClient(t, m, " (QRESYNC (67890007 60000))")
	requireExactly(t, lines[len(lines)-3:],
		"* VANISHED (EARLIER) 3,5",
		`* 1 FETCH (UID 4 FLAGS (\Seen) MODSEQ (65402))`,
		`* 2 FETCH (UID 6 FLAGS (\Deleted) MODSEQ (75403))`,
	)

	// UIDVALIDITY mismatch: "behave as if no dynamic message data changed".
	_, lines = qresyncClient(t, m, " (QRESYNC (12345 30000 1:10 (1:3 4,6,8)))")
	for _, line := range lines {
		if strings.Contains(line, "VANISHED") || strings.Contains(line, "FETCH") {
			t.Fatalf("UIDVALIDITY mismatch still resynchronised: %q", line)
		}
	}

	for _, bad := range []string{
		"SELECT INBOX (QRESYNC (67890007))",
		"SELECT INBOX (QRESYNC (0 1))",
		"SELECT INBOX (QRESYNC (67890007 0))",
		"SELECT INBOX (QRESYNC (67890007 1 x))",
		"SELECT INBOX (QRESYNC (67890007 1 1:5 (1:2)))",
		"SELECT INBOX (QRESYNC (67890007 1) QRESYNC (67890007 1))",
	} {
		if _, tagged := c.command("B", "%s", bad); !strings.HasPrefix(tagged, "B BAD") {
			t.Fatalf("%s: %q, want BAD", bad, tagged)
		}
	}
}

// TestQresyncUIDFetchVanished follows RFC 7162 §3.2.6: UID FETCH ...
// (CHANGEDSINCE n VANISHED) sends VANISHED (EARLIER) for the UIDs in the set
// expunged since n before any FETCH; VANISHED needs ENABLE QRESYNC.
//
// checklist: IMAP-QRESYNC-04
func TestQresyncUIDFetchVanished(t *testing.T) {
	m := condstoreMailbox()
	m.vanished = map[uint32]uint64{1: 100, 3: 70000, 11: 80000}
	m.highest = 80000

	plain := condstoreClient(t, m, " (CONDSTORE)")
	if _, tagged := plain.command("B", "UID FETCH 1:* (FLAGS) (CHANGEDSINCE 1 VANISHED)"); !strings.HasPrefix(tagged, "B BAD") {
		t.Fatalf("VANISHED without ENABLE QRESYNC: %q, want BAD", tagged)
	}

	c, _ := qresyncClient(t, m, "")
	lines, tagged := c.command("s100", "UID FETCH 1:* (FLAGS) (CHANGEDSINCE 65402 VANISHED)")
	requireOK(t, tagged, "s100")
	requireExactly(t, lines,
		"* VANISHED (EARLIER) 3,11",
		`* 2 FETCH (UID 6 FLAGS (\Deleted) MODSEQ (75403))`,
	)
	lines, tagged = c.command("s101", "UID FETCH 2:9 (FLAGS) (CHANGEDSINCE 1 VANISHED)")
	requireOK(t, tagged, "s101")
	if lines[0] != "* VANISHED (EARLIER) 3" {
		t.Fatalf("VANISHED (EARLIER) outside the UID set or not first: %q", lines)
	}
}

// TestQresyncExpungeVanished: once QRESYNC is enabled, EXPUNGE, UID EXPUNGE,
// MOVE and an expunge seen by NOOP are reported with one VANISHED listing the
// UIDs, never EXPUNGE (RFC 7162 §3.2.10; RFC 6851 §4.4).
//
// checklist: IMAP-QRESYNC-05 IMAP-MOVE-13 IMAP-MOVE-18
func TestQresyncExpungeVanished(t *testing.T) {
	m := condstoreMailbox()
	c, _ := qresyncClient(t, m, "")
	lines, tagged := c.command("E1", "EXPUNGE")
	requireOK(t, tagged, "E1")
	requireExactly(t, lines, "* VANISHED 6")

	lines, tagged = c.command("M1", "UID MOVE 4,9 Archive")
	requireOK(t, tagged, "M1")
	requireExactly(t, lines, "* VANISHED 4,9")
	if m.moved[4] != "Archive" || m.moved[9] != "Archive" {
		t.Fatalf("moved %v", m.moved)
	}

	_ = m.Delete(10)
	lines, tagged = c.command("N1", "NOOP")
	requireOK(t, tagged, "N1")
	requireExactly(t, lines, "* VANISHED 10")

	// Each expunge raised HIGHESTMODSEQ and was remembered.
	lines, tagged = c.command("F1", "UID FETCH 1:* (FLAGS) (CHANGEDSINCE 75403 VANISHED)")
	requireOK(t, tagged, "F1")
	requireExactly(t, lines, "* VANISHED (EARLIER) 4,6,9:10")
}
