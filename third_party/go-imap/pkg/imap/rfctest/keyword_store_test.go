package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// plainStore keeps system flags but not keywords: it does not implement
// imap.KeywordMailbox.
type plainStore struct {
	mailbox
	messages []imap.Message
	updates  []imap.FlagUpdate
	appends  []imap.FlagUpdate
}

func (m *plainStore) Messages(string) ([]imap.Message, error) { return slices.Clone(m.messages), nil }
func (m *plainStore) Store(_ uint32, update imap.FlagUpdate) error {
	m.updates = append(m.updates, update)
	return nil
}
func (m *plainStore) Append(_ string, flags imap.FlagUpdate, _ []byte) error {
	m.appends = append(m.appends, flags)
	return nil
}

// keywordStore keeps keywords (imap.KeywordMailbox) in one INBOX, with limit
// distinct keywords at most, 0 for no limit.
type keywordStore struct {
	mailbox
	messages []imap.Message
	limit    int
	appends  []imap.FlagUpdate
}

func (m *keywordStore) KeywordLimit() int                       { return m.limit }
func (m *keywordStore) Messages(string) ([]imap.Message, error) { return slices.Clone(m.messages), nil }
func (m *keywordStore) Fetch(uint32) ([]byte, error)            { return []byte("Subject: k\r\n\r\nk\r\n"), nil }
func (m *keywordStore) Store(uid uint32, update imap.FlagUpdate) error {
	for index := range m.messages {
		if m.messages[index].UID == uid && update.KeywordsSet {
			m.messages[index].Keywords = slices.Clone(update.Keywords)
		}
	}
	return nil
}
func (m *keywordStore) Append(_ string, flags imap.FlagUpdate, raw []byte) error {
	m.appends = append(m.appends, flags)
	m.messages = append(m.messages, imap.Message{UID: uint32(100 + len(m.messages)), Size: len(raw), Keywords: slices.Clone(flags.Keywords)})
	return nil
}

// TestKeywordCreationNeedsAKeywordStore: a store that is not an
// imap.KeywordMailbox keeps no keywords, so SELECT offers no \*, a keyword in
// STORE is ignored and never reaches the store, and APPEND drops keywords
// while keeping system flags (RFC 3501 §§6.4.6, 7.1).
//
// checklist: IMAP-INV-FLAGS-05
func TestKeywordCreationNeedsAKeywordStore(t *testing.T) {
	box := &plainStore{messages: []imap.Message{{UID: 7}}}
	c := selectionClient(t, box)
	lines, tagged := c.command("S1", "SELECT INBOX")
	requireOK(t, tagged, "S1")
	requireLine(t, lines, `* OK [PERMANENTFLAGS (\Seen \Answered \Flagged \Deleted \Draft)] Limited`)

	lines, tagged = c.command("K1", `STORE 1 +FLAGS ($Forwarded \Seen)`)
	requireOK(t, tagged, "K1")
	requireExactly(t, lines, `* 1 FETCH (FLAGS (\Seen))`)
	if len(box.updates) != 1 || box.updates[0].KeywordsSet || box.updates[0].Seen == nil {
		t.Fatalf("store updates %+v, want only \\Seen", box.updates)
	}
	requireSearch(t, c, "SEARCH KEYWORD $Forwarded", "* SEARCH")

	_, tagged = appendCommand(t, c, `INBOX (\Flagged Work)`, "msg")
	requireOK(t, tagged, "A1")
	if len(box.appends) != 1 || len(box.appends[0].Keywords) != 0 || box.appends[0].Flagged == nil {
		t.Fatalf("append flags %+v, want only \\Flagged", box.appends)
	}

	// A FLAGS replace never asks such a store to clear keywords it reports.
	reported := &plainStore{messages: []imap.Message{{UID: 8, Keywords: []string{"Old"}}}}
	c = storeClientFor(t, reported)
	lines, tagged = c.command("K2", `STORE 1 FLAGS (\Seen)`)
	requireOK(t, tagged, "K2")
	requireExactly(t, lines, `* 1 FETCH (FLAGS (\Seen Old))`)
	if len(reported.updates) != 1 || reported.updates[0].KeywordsSet {
		t.Fatalf("store updates %+v, want no keyword change", reported.updates)
	}
}

// TestAppendKeepsKeywords: APPEND's keywords reach a keyword store, and FETCH
// FLAGS and SEARCH KEYWORD/UNKEYWORD then use them, including the RFC 9051
// §2.3.2 keywords, compared without case. An omitted flag list stores none.
//
// checklist: IMAP2-CMD-126
func TestAppendKeepsKeywords(t *testing.T) {
	rev2 := []string{"$Forwarded", "$MDNSent", "$Junk", "$NotJunk", "$Phishing"}
	box := &keywordStore{messages: []imap.Message{{UID: 1}}}
	c := storeClientFor(t, box)
	_, tagged := appendCommand(t, c, "INBOX ("+strings.Join(rev2, " ")+" Work)", "msg")
	requireOK(t, tagged, "A1")
	if want := append(slices.Clone(rev2), "Work"); len(box.appends) != 1 || !slices.Equal(box.appends[0].Keywords, want) {
		t.Fatalf("append flags %+v, want keywords %v", box.appends, want)
	}
	_, tagged = appendCommand(t, c, "INBOX", "bare")
	requireOK(t, tagged, "A1")
	if len(box.appends) != 2 || len(box.appends[1].Keywords) != 0 {
		t.Fatalf("APPEND with no flag list stored %+v", box.appends)
	}
	lines, tagged := c.command("F1", "FETCH 2 FLAGS")
	requireOK(t, tagged, "F1")
	requireLine(t, lines, `* 2 FETCH (UID 101 FLAGS ($Forwarded $MDNSent $Junk $NotJunk $Phishing Work))`)
	for _, keyword := range append(slices.Clone(rev2), "work") {
		requireSearch(t, c, "SEARCH KEYWORD "+strings.ToUpper(keyword), "* SEARCH 2")
		requireSearch(t, c, "SEARCH UNKEYWORD "+strings.ToLower(keyword), "* SEARCH 1 3")
	}
}

// TestKeywordsPerMessageCap: a message carries at most imap.MaxKeywords
// keywords. A STORE or APPEND over the cap is answered NO with RFC 5530's
// LIMIT code, an APPEND before its literal is read, and nothing is stored.
//
// checklist: IMAP-STORE-06
func TestKeywordsPerMessageCap(t *testing.T) {
	names := func(from, count int) string {
		list := make([]string, count)
		for i := range list {
			list[i] = fmt.Sprintf("k%d", from+i)
		}
		return strings.Join(list, " ")
	}
	box := &keywordStore{messages: []imap.Message{{UID: 1}}}
	c := storeClientFor(t, box)
	_, tagged := c.command("K1", "STORE 1 +FLAGS (%s)", names(0, imap.MaxKeywords+1))
	if !strings.HasPrefix(tagged, "K1 NO [LIMIT] ") {
		t.Fatalf("STORE over the cap: %q", tagged)
	}
	_, tagged = c.command("K2", "STORE 1 +FLAGS.SILENT (%s)", names(0, imap.MaxKeywords))
	requireOK(t, tagged, "K2")
	_, tagged = c.command("K3", "UID STORE 1 +FLAGS (k%d)", imap.MaxKeywords)
	if !strings.HasPrefix(tagged, "K3 NO [LIMIT] ") {
		t.Fatalf("UID STORE past a full message: %q", tagged)
	}
	if got := len(box.messages[0].Keywords); got != imap.MaxKeywords {
		t.Fatalf("message holds %d keywords, want %d", got, imap.MaxKeywords)
	}
	lines, tagged := c.command("A1", "APPEND INBOX (%s) {3}", names(0, imap.MaxKeywords+1))
	if !strings.HasPrefix(tagged, "A1 NO [LIMIT] ") || len(lines) != 0 || len(box.appends) != 0 {
		t.Fatalf("APPEND over the cap: %q %q %+v", lines, tagged, box.appends)
	}
	_, tagged = c.command("K4", "STORE 1 +FLAGS (%s)", strings.Repeat("x", imap.MaxKeywordLength+1))
	if !strings.HasPrefix(tagged, "K4 NO ") {
		t.Fatalf("keyword over %d bytes: %q", imap.MaxKeywordLength, tagged)
	}
}

// TestKeywordLimitRemovesWildcard: once the selected folder carries as many
// distinct keywords as the store keeps (imap.KeywordMailbox.KeywordLimit),
// PERMANENTFLAGS lists them without \* (RFC 9051 §7.1) and a STORE or APPEND
// that would add another is NO [LIMIT]; a keyword the folder has stays usable.
//
// checklist: IMAP2-RESP-175 IMAP-INV-FLAGS-05
func TestKeywordLimitRemovesWildcard(t *testing.T) {
	box := &keywordStore{limit: 2, messages: []imap.Message{{UID: 1, Keywords: []string{"Work"}}, {UID: 2}}}
	c := selectionClient(t, box)
	lines, tagged := c.command("S1", "SELECT INBOX")
	requireOK(t, tagged, "S1")
	requireLine(t, lines, `* OK [PERMANENTFLAGS (\Seen \Answered \Flagged \Deleted \Draft Work \*)] Limited`)

	_, tagged = c.command("K1", "STORE 2 +FLAGS.SILENT (Home)")
	requireOK(t, tagged, "K1")
	lines, tagged = c.command("N1", "NOOP")
	requireOK(t, tagged, "N1")
	requireLine(t, lines, `* FLAGS (\Seen \Answered \Flagged \Deleted \Draft Home Work)`)
	requireLine(t, lines, `* OK [PERMANENTFLAGS (\Seen \Answered \Flagged \Deleted \Draft Home Work)] Limited`)

	_, tagged = c.command("K2", "STORE 1 +FLAGS (Travel)")
	if tagged != "K2 NO [LIMIT] Too many keywords" {
		t.Fatalf("STORE past the folder limit: %q", tagged)
	}
	lines, tagged = c.command("A1", "APPEND INBOX (Travel) {3}")
	if tagged != "A1 NO [LIMIT] Too many keywords" || len(lines) != 0 || len(box.appends) != 0 {
		t.Fatalf("APPEND past the folder limit: %q %q", lines, tagged)
	}
	_, tagged = c.command("K3", "STORE 2 +FLAGS.SILENT (work)")
	requireOK(t, tagged, "K3")

	lines, tagged = c.command("S2", "SELECT INBOX")
	requireOK(t, tagged, "S2")
	requireLine(t, lines, `* OK [PERMANENTFLAGS (\Seen \Answered \Flagged \Deleted \Draft Home Work)] Limited`)
}

func storeClientFor(t *testing.T, box imap.Mailbox) *client {
	t.Helper()
	c := selectionClient(t, box)
	if _, tagged := c.command("S1", "SELECT INBOX"); !strings.HasPrefix(tagged, "S1 OK") {
		t.Fatalf("SELECT: %q", tagged)
	}
	return c
}
