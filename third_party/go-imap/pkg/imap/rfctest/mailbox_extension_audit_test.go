package rfctest

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

type mailboxAuditFixture struct {
	mailbox
	folders []imap.Folder
	sizes   []int64
	err     error
	reads   int
}

func (m *mailboxAuditFixture) Folders() ([]imap.Folder, error) {
	if m.folders != nil {
		return m.folders, nil
	}
	return []imap.Folder{{Name: "INBOX"}}, nil
}
func (m *mailboxAuditFixture) Messages(string) ([]imap.Message, error) {
	m.reads++
	if m.err != nil {
		return nil, m.err
	}
	var messages []imap.Message
	for i, size := range m.sizes {
		messages = append(messages, imap.Message{UID: uint32(i + 1), Size: int(size)})
	}
	return messages, nil
}

// RFC8438 sections3/4 require an unsigned 63-bit SIZE no smaller than the
// message-size sum. Invalid host values cannot be reported as negative or
// wrapped data, including in LIST-STATUS; unrelated metadata still works.
// checklist: IMAP-STATUSSIZE-03
func TestStatusSizeNumericBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sizes []int64
		want  string
	}{
		{"negative", []int64{-1}, ""},
		{"over32bits", []int64{math.MaxInt32, math.MaxInt32, 9}, "4294967303"},
		{"maximum", []int64{math.MaxInt64}, "9223372036854775807"},
		{"overflow", []int64{math.MaxInt64, 1}, ""},
		{"empty", nil, "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strconv.IntSize < 64 && (tc.name == "maximum" || tc.name == "overflow") {
				t.Skip("the host Message.Size int cannot represent this fixture on a 32-bit architecture")
			}
			m := &mailboxAuditFixture{sizes: tc.sizes}
			c := selectionClient(t, m)
			lines, tagged := c.command("S1", "STATUS INBOX (MESSAGES SIZE)")
			if tc.want == "" {
				if !strings.HasPrefix(tagged, "S1 NO ") || len(lines) != 0 {
					t.Fatalf("invalid size emitted data: %q %q", lines, tagged)
				}
			} else {
				requireOK(t, tagged, "S1")
				requireExactly(t, lines, fmt.Sprintf(`* STATUS "INBOX" (MESSAGES %d SIZE %s)`, len(tc.sizes), tc.want))
			}
			lines, tagged = c.command("L1", `LIST "" INBOX RETURN (STATUS (SIZE))`)
			requireOK(t, tagged, "L1")
			want := []string{`* LIST () "/" "INBOX"`}
			if tc.want != "" {
				want = append(want, `* STATUS "INBOX" (SIZE `+tc.want+`)`)
			}
			requireExactly(t, lines, want...)
			lines, tagged = c.command("S2", "STATUS INBOX (MESSAGES)")
			requireOK(t, tagged, "S2")
			requireExactly(t, lines, fmt.Sprintf(`* STATUS "INBOX" (MESSAGES %d)`, len(tc.sizes)))
		})
	}
}

// RFC5819 section2 requires a buffered LIST to gain Noselect if the mailbox
// vanished before STATUS. Unexpected storage errors may omit STATUS without
// misclassifying an otherwise selectable folder.
// checklist: IMAP-LISTSTATUS-04
func TestListStatusVanishedMailbox(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		attrs string
	}{
		{"vanished", imap.ErrNoSuchMailbox, `\Noselect`},
		{"wrapped vanished", fmt.Errorf("private lookup: %w", imap.ErrNoSuchMailbox), `\Noselect`},
		{"unexpected", errors.New("private storage unavailable"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &mailboxAuditFixture{err: tc.err}
			c := selectionClient(t, m)
			lines, tagged := c.command("L1", `LIST "" INBOX RETURN (STATUS (MESSAGES))`)
			requireOK(t, tagged, "L1")
			requireExactly(t, lines, `* LIST (`+tc.attrs+`) "/" "INBOX"`)
			if m.reads != 1 {
				t.Fatalf("STATUS lookups=%d, want1", m.reads)
			}
			_, tagged = c.command("N1", "NOOP")
			requireOK(t, tagged, "N1")
		})
	}
}

// RFC5819 section3's recursive example omits STATUS for a parent returned
// only because a child satisfies selection. No parent metadata read is needed.
// checklist: IMAP-LISTSTATUS-05
func TestListStatusRecursiveSelection(t *testing.T) {
	m := &mailboxAuditFixture{folders: []imap.Folder{{Name: "Archive"}, {Name: "Archive/2026", SpecialUse: imap.SpecialUseArchive}}}
	c := selectionClient(t, m)
	lines, tagged := c.command("L1", `LIST (SPECIAL-USE RECURSIVEMATCH) "" "%%" RETURN (STATUS (MESSAGES))`)
	requireOK(t, tagged, "L1")
	requireExactly(t, lines, `* LIST () "/" "Archive" ("CHILDINFO" ("SPECIAL-USE"))`)
	if m.reads != 0 {
		t.Fatalf("nonmatching parent caused%d STATUS reads", m.reads)
	}
	lines, tagged = c.command("L2", `LIST (SPECIAL-USE RECURSIVEMATCH) "" "*" RETURN (STATUS (MESSAGES))`)
	requireOK(t, tagged, "L2")
	requireExactly(t, lines, `* LIST (\Archive) "/" "Archive/2026"`, `* STATUS "Archive/2026" (MESSAGES 0)`)
	if m.reads != 1 {
		t.Fatalf("matching child STATUS reads=%d, want1", m.reads)
	}
}

// RFC5258 section3 requires repeated options to behave as one occurrence.
// Retain the first STATUS parameter list, still validating repeated syntax.
// checklist: IMAP-LISTEXT-07
func TestListRepeatedStatusOptions(t *testing.T) {
	for _, options := range []string{
		`STATUS (MESSAGES) STATUS (MESSAGES)`,
		`STATUS (MESSAGES) status (UNSEEN)`,
		`STATUS (MESSAGES) STATUS (UNSEEN) CHILDREN`,
	} {
		t.Run(options, func(t *testing.T) {
			m := &mailboxAuditFixture{}
			c := selectionClient(t, m)
			lines, tagged := c.command("L1", `LIST "" INBOX RETURN (%s)`, options)
			requireOK(t, tagged, "L1")
			attrs := ""
			if strings.HasSuffix(options, "CHILDREN") {
				attrs = `\HasNoChildren`
			}
			requireExactly(t, lines, `* LIST (`+attrs+`) "/" "INBOX"`, `* STATUS "INBOX" (MESSAGES 0)`)
			if m.reads != 1 {
				t.Fatalf("duplicate option caused%d reads", m.reads)
			}
		})
	}
	for _, options := range []string{`STATUS (MESSAGES) STATUS ()`, `STATUS (MESSAGES) STATUS (BOGUS)`, `STATUS (MESSAGES) STATUS`} {
		m := &mailboxAuditFixture{}
		c := selectionClient(t, m)
		lines, tagged := c.command("B1", `LIST "" INBOX RETURN (%s)`, options)
		if !strings.HasPrefix(tagged, "B1 BAD ") || len(lines) != 0 || m.reads != 0 {
			t.Fatalf("malformed repeated option: %q %q reads=%d", lines, tagged, m.reads)
		}
	}
}

// RFC6154 section2 permits all seven standardized roles, multiple mailboxes
// sharing a role and no role; section6 forbids inventing extension attributes.
// checklist: IMAP-SPECIALUSE-10
func TestSpecialUseCompleteAttributeSet(t *testing.T) {
	roles := []string{`\All`, `\Archive`, `\Drafts`, `\Flagged`, `\Junk`, `\Sent`, `\Trash`, `\Sent`, `\Unregistered`}
	m := &mailboxAuditFixture{}
	var expected []string
	for i, role := range roles {
		name := fmt.Sprintf("role%d", i)
		m.folders = append(m.folders, imap.Folder{Name: name, SpecialUse: role})
		if role != `\Unregistered` {
			expected = append(expected, `* LIST (`+role+`) "/" "`+name+`"`)
		}
	}
	c := selectionClient(t, m)
	lines, tagged := c.command("L1", `LIST (SPECIAL-USE) "" "*"`)
	requireOK(t, tagged, "L1")
	requireExactly(t, lines, expected...)
	lines, tagged = c.command("L2", `LIST "" role8 RETURN (SPECIAL-USE)`)
	requireOK(t, tagged, "L2")
	requireExactly(t, lines, `* LIST () "/" "role8"`)
}
