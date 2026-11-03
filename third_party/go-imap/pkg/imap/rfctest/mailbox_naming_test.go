package rfctest

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// namesMailbox is an account whose store names its folders in UTF-8. It records
// each folder the server reads and each destination it copies, moves or
// appends to.
type namesMailbox struct {
	mailbox
	folders      map[string][]imap.Message
	reads, dests []string
}

func newNamesMailbox(names ...string) *namesMailbox {
	m := &namesMailbox{folders: map[string][]imap.Message{"INBOX": {{UID: 5, Seen: true}}}}
	for _, name := range names {
		m.folders[name] = []imap.Message{{UID: 9}}
	}
	return m
}

func (m *namesMailbox) Folders() ([]imap.Folder, error) {
	names := make([]string, 0, len(m.folders))
	for name := range m.folders {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]imap.Folder, 0, len(names))
	for _, name := range names {
		out = append(out, imap.Folder{Name: name})
	}
	return out, nil
}

func (m *namesMailbox) Messages(folder string) ([]imap.Message, error) {
	m.reads = append(m.reads, folder)
	messages, ok := m.folders[folder]
	if !ok {
		return nil, errors.New("mailbox unavailable")
	}
	return slices.Clone(messages), nil
}

func (m *namesMailbox) Copy(_ uint32, dest string) error {
	m.dests = append(m.dests, "copy "+dest)
	return nil
}

func (m *namesMailbox) Move(_ uint32, dest string) error {
	m.dests = append(m.dests, "move "+dest)
	return nil
}

func (m *namesMailbox) Append(dest string, _ imap.FlagUpdate, _ []byte) error {
	m.dests = append(m.dests, "append "+dest)
	return nil
}

// utf8NamesMailbox is a namesMailbox whose backend supports UTF8=ACCEPT.
type utf8NamesMailbox struct{ *namesMailbox }

func (utf8NamesMailbox) EnableUTF8() error { return nil }

// The store's names, with the RFC 3501 §5.1.3 example among them.
var storeNames = []string{"~peter/mail/台北/日本語", "Tom & Jerry", "Entwürfe", "Plain", "tab\there"}

// TestMailboxNamesAreWrittenInModifiedUTF7: a server whose store does not
// follow the modified UTF-7 convention "MUST convert to modified UTF-7 any
// mailbox name that contains either non-ASCII characters or the "&" character"
// (RFC 3501 §5.1.3), and SHOULD NOT return 8-bit names in LIST or LSUB (§5.1).
// LIST, LSUB, STATUS and QUOTAROOT write the §5.1.3 example as the RFC does, "&"
// as "&-" and a control in base64, and leave plain names alone. LIST patterns
// are matched against the names as written.
//
// checklist: IMAP-MUTF7-01
// checklist: IMAP2-RESP-189
// checklist: IMAP2-COMPAT-250 IMAP2-COMPAT-255
func TestMailboxNamesAreWrittenInModifiedUTF7(t *testing.T) {
	box := newNamesMailbox(storeNames...)
	c := selectionClient(t, box)
	for _, cmd := range []string{"LIST", "LSUB"} {
		lines, tagged := c.command("L1", `%s "" "*"`, cmd)
		requireOK(t, tagged, "L1")
		for _, name := range []string{"~peter/mail/&U,BTFw-/&ZeVnLIqe-", "Tom &- Jerry", "Entw&APw-rfe", "Plain", "tab&AAk-here", "INBOX"} {
			requireLine(t, lines, fmt.Sprintf(`* %s () "/" %q`, cmd, name))
		}
		if len(lines) != 6 {
			t.Fatalf("%s: %q, want 6 names", cmd, lines)
		}
		for _, line := range lines {
			if !isASCII(line) {
				t.Fatalf("%s returned an 8-bit name: %q", cmd, line)
			}
		}
	}

	for _, tc := range []struct{ pattern, want string }{
		{"Entw&APw-*", "Entw&APw-rfe"},
		{"*&-*", "Tom &- Jerry"},
		{"~peter/*/&ZeVnLIqe-", "~peter/mail/&U,BTFw-/&ZeVnLIqe-"},
	} {
		lines, tagged := c.command("L2", `LIST "" %q`, tc.pattern)
		requireOK(t, tagged, "L2")
		requireExactly(t, lines, fmt.Sprintf(`* LIST () "/" %q`, tc.want))
	}
	lines, tagged := c.command("L3", `LIST "" "Entw*rfe"`)
	requireOK(t, tagged, "L3")
	requireExactly(t, lines, `* LIST () "/" "Entw&APw-rfe"`)

	lines, tagged = c.command("S1", `STATUS "Entw&APw-rfe" (MESSAGES)`)
	requireOK(t, tagged, "S1")
	requireExactly(t, lines, `* STATUS "Entw&APw-rfe" (MESSAGES 1)`)
	lines, tagged = c.command("Q1", `GETQUOTAROOT "Tom &- Jerry"`)
	requireOK(t, tagged, "Q1")
	requireLine(t, lines, `* QUOTAROOT "Tom &- Jerry" ""`)
	if !slices.Equal(box.reads, []string{"Entwürfe"}) {
		t.Fatalf("backend reads %q, want the decoded name", box.reads)
	}
}

// TestMailboxNamesAreDecodedFromModifiedUTF7: the names a client sends are
// modified UTF-7, so the store gets them decoded, for SELECT, EXAMINE, STATUS,
// COPY, UID COPY, MOVE, APPEND, DELETE and RENAME. The modified BASE64 is kept
// exactly and is case-sensitive (§5.1.3 MUST), so a name differing from a
// folder's only in the case of its base64 names a different folder, while INBOX
// stays case-insensitive (§5.1).
//
// checklist: IMAP-MUTF7-02
// checklist: IMAP2-RESP-189
// checklist: IMAP2-COMPAT-250
func TestMailboxNamesAreDecodedFromModifiedUTF7(t *testing.T) {
	box := newNamesMailbox(append(storeNames, "café")...)
	c := selectionClient(t, box)

	_, tagged := c.command("S1", `SELECT "~peter/mail/&U,BTFw-/&ZeVnLIqe-"`)
	requireOK(t, tagged, "S1")
	_, tagged = c.command("S2", `EXAMINE "Tom &- Jerry"`)
	requireOK(t, tagged, "S2")
	_, tagged = c.command("S3", `STATUS caf&AOk- (MESSAGES)`)
	requireOK(t, tagged, "S3")
	_, tagged = c.command("S4", `SELECT "caf&aok-"`)
	if !strings.HasPrefix(tagged, "S4 NO ") {
		t.Fatalf("base64 in another case: %q, want NO", tagged)
	}
	_, tagged = c.command("S5", "SELECT inbox")
	requireOK(t, tagged, "S5")
	want := []string{"~peter/mail/台北/日本語", "Tom & Jerry", "café", "caf檉", "INBOX"}
	if !slices.Equal(box.reads, want) {
		t.Fatalf("backend reads %q, want %q", box.reads, want)
	}

	for i, cmd := range []string{`COPY 1 "Entw&APw-rfe"`, `UID COPY 5 "Tom &- Jerry"`, `MOVE 1 "~peter/mail/&U,BTFw-"`} {
		_, tagged = c.command(fmt.Sprintf("C%d", i), "%s", cmd)
		requireOK(t, tagged, fmt.Sprintf("C%d", i))
	}
	_, tagged = appendCommand(t, c, `"Entw&APw-rfe"`, "abc")
	requireOK(t, tagged, "A1")
	_, tagged = c.command("D1", `DELETE "Tom &- Jerry"`)
	requireOK(t, tagged, "D1")
	_, tagged = c.command("R1", `RENAME "Entw&APw-rfe" "Entw&APw-rfe-alt"`)
	requireOK(t, tagged, "R1")
	want = []string{"copy Entwürfe", "copy Tom & Jerry", "move ~peter/mail/台北", "append Entwürfe", "move Trash", "move Entwürfe-alt"}
	if !slices.Equal(box.dests, want) {
		t.Fatalf("backend destinations %q, want %q", box.dests, want)
	}
}

// TestInvalidModifiedUTF7NamesAreRefused: a server SHOULD verify that a name
// with "&" given to CREATE "is: in the correctly modified UTF-7 syntax, has no
// superfluous shifts, and has no encoding in modified BASE64 of any printing
// US-ASCII character which can represent itself" (RFC 3501 §5.1.3). CREATE and
// RENAME's new name refuse each such name with NO, including the RFC's own
// examples "&Jjo!" and "&U,BTFw-&ZeVnLIqe-", and take their correct forms. Any
// other command finds no folder by such a name: NO, without asking the store.
//
// checklist: IMAP-MUTF7-03
// checklist: IMAP2-COMPAT-251 IMAP2-COMPAT-252 IMAP2-COMPAT-254
func TestInvalidModifiedUTF7NamesAreRefused(t *testing.T) {
	box := newNamesMailbox("Plain")
	c := selectionClient(t, box)
	for _, name := range []string{
		"&Jjo!",              // no shift back to US-ASCII
		"&U,BTFw-&ZeVnLIqe-", // a superfluous shift
		"&AGE-",              // "a" in base64
		"&Jjo",               // never shifts back
		"&AGEA-",             // an odd number of octets
		"&2D0-",              // an unpaired surrogate
		"&Jjp-",              // nonzero padding bits
		"&U/BTFw-",           // "/" where modified BASE64 has ","
		"a&b",                // a lone "&"
	} {
		t.Run(name, func(t *testing.T) {
			lines, tagged := c.command("C1", "CREATE %q", name)
			requireExactly(t, lines)
			if !strings.HasPrefix(tagged, "C1 NO ") {
				t.Fatalf("CREATE: %q, want NO", tagged)
			}
			_, tagged = c.command("R1", `RENAME Plain %q`, name)
			if !strings.HasPrefix(tagged, "R1 NO ") {
				t.Fatalf("RENAME: %q, want NO", tagged)
			}
			for _, cmd := range []string{"SELECT %q", "STATUS %q (MESSAGES)", "DELETE %q", "SUBSCRIBE %q"} {
				_, tagged = c.command("N1", cmd, name)
				if !strings.HasPrefix(tagged, "N1 NO ") {
					t.Fatalf("%s: %q, want NO", fmt.Sprintf(cmd, name), tagged)
				}
			}
			if len(box.reads) != 0 || len(box.dests) != 0 {
				t.Fatalf("an invalid name reached the store: reads=%q dests=%q", box.reads, box.dests)
			}
		})
	}
	for _, name := range []string{"&Jjo-!", "&U,BTF2XlZyyKng-", "Tom &- Jerry", "plain"} {
		_, tagged := c.command("C2", "CREATE %q", name)
		requireOK(t, tagged, "C2")
	}
}

// TestEightBitNamesBeforeUTF8: mailbox names are 7-bit, and a server SHOULD
// prohibit the creation of 8-bit mailbox names (RFC 3501 §5.1). Without
// UTF8=ACCEPT, CREATE refuses an 8-bit name with NO and no other command finds
// a folder by one. A UTF8=ACCEPT server MUST accept UTF8-quoted mailbox names
// (RFC 6855 §3), so it takes a UTF-8 name before the client enables the mode,
// but it still writes names in modified UTF-7 until then.
//
// checklist: IMAP-MUTF7-04
func TestEightBitNamesBeforeUTF8(t *testing.T) {
	box := newNamesMailbox("café")
	c := selectionClient(t, box)
	sendLiteral(t, c, "C1 CREATE ", "Entwürfe", "")
	if tagged := c.line(); !strings.HasPrefix(tagged, "C1 NO ") {
		t.Fatalf("8-bit CREATE: %q, want NO", tagged)
	}
	sendLiteral(t, c, "S1 SELECT ", "café", "")
	if tagged := c.line(); !strings.HasPrefix(tagged, "S1 NO ") {
		t.Fatalf("8-bit SELECT: %q, want NO", tagged)
	}
	if len(box.reads) != 0 {
		t.Fatalf("an 8-bit name reached the store: %q", box.reads)
	}

	box = newNamesMailbox("café")
	c = selectionClient(t, utf8NamesMailbox{box})
	_, tagged := c.command("C2", `CREATE "Entwürfe"`)
	requireOK(t, tagged, "C2")
	_, tagged = c.command("S2", `SELECT "café"`)
	requireOK(t, tagged, "S2")
	lines, tagged := c.command("L1", `LIST "" "caf*"`)
	requireOK(t, tagged, "L1")
	requireExactly(t, lines, `* LIST () "/" "caf&AOk-"`)
	if !slices.Equal(box.reads, []string{"café"}) {
		t.Fatalf("backend reads %q, want the UTF-8 name", box.reads)
	}
}

// TestUTF8AcceptNamesAreUTF8: once the client has enabled UTF8=ACCEPT, names
// travel as UTF-8 both ways (RFC 6855 §3): LIST writes the store's names as they
// are, and a name a command carries reaches the store unchanged, with no
// modified UTF-7 decoding, so "&-" is two characters.
//
// checklist: IMAP-MUTF7-05
func TestUTF8AcceptNamesAreUTF8(t *testing.T) {
	box := newNamesMailbox("Entwürfe", "Tom & Jerry", "Tom &- Jerry")
	c := selectionClient(t, utf8NamesMailbox{box})
	enableUTF8(t, c)
	lines, tagged := c.command("L1", `LIST "" "Tom*"`)
	requireOK(t, tagged, "L1")
	requireExactly(t, lines, `* LIST () "/" "Tom & Jerry"`, `* LIST () "/" "Tom &- Jerry"`)
	// An 8-bit name is written as a literal, whose octets end the response line.
	if _, err := fmt.Fprintf(c.conn, "L2 LIST \"\" \"Entw*\"\r\n"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{fmt.Sprintf(`* LIST () "/" {%d}`, len("Entwürfe")), "Entwürfe"} {
		if got := c.line(); got != want {
			t.Fatalf("LIST: %q, want %q", got, want)
		}
	}
	requireOK(t, c.line(), "L2")

	for i, name := range []string{"Entwürfe", "Tom &- Jerry", "Tom & Jerry"} {
		_, tagged = c.command(fmt.Sprintf("S%d", i), "SELECT %q", name)
		requireOK(t, tagged, fmt.Sprintf("S%d", i))
	}
	if want := []string{"Entwürfe", "Tom &- Jerry", "Tom & Jerry"}; !slices.Equal(box.reads, want) {
		t.Fatalf("backend reads %q, want %q", box.reads, want)
	}
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
