package rfctest

import (
	"errors"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// statusFolders has INBOX with 17 messages, 1 seen; foo with 30, 1 seen; and
// broken, which the store cannot count.
type statusFolders struct{ mailbox }

func (statusFolders) Folders() ([]imap.Folder, error) {
	return []imap.Folder{{Name: "INBOX"}, {Name: "foo"}, {Name: "broken"}}, nil
}

func (statusFolders) Messages(folder string) ([]imap.Message, error) {
	n := map[string]int{"INBOX": 17, "foo": 30}[folder]
	if n == 0 {
		return nil, errors.New("store unavailable")
	}
	msgs := make([]imap.Message, n)
	for i := range msgs {
		msgs[i] = imap.Message{UID: uint32(i + 1), Seen: i == 0}
	}
	return msgs, nil
}

// TestListStatus: with LIST-STATUS advertised, LIST's STATUS return option
// (RFC 5819) follows each listed mailbox's LIST with its STATUS, carrying the
// requested items in the order asked, as in the RFC's A01 example; a mailbox
// whose STATUS the store cannot read is still listed, its STATUS dropped, and
// the command still OK (§2). SPECIAL-USE may sit beside it. Without the option
// LIST sends no STATUS.
//
// checklist: IMAP-LISTSTATUS-01 IMAP-LISTSTATUS-02
// checklist: IMAP2-CMD-108
func TestListStatus(t *testing.T) {
	c := selectionClient(t, statusFolders{})
	requireCaps(t, c.capabilities("C1"), []string{"LIST-STATUS"}, nil)

	lines, tagged := c.command("A01", `LIST "" %% RETURN (STATUS (MESSAGES UNSEEN))`)
	requireOK(t, tagged, "A01")
	requireExactly(t, lines,
		`* LIST () "/" "INBOX"`, `* STATUS "INBOX" (MESSAGES 17 UNSEEN 16)`,
		`* LIST () "/" "foo"`, `* STATUS "foo" (MESSAGES 30 UNSEEN 29)`,
		`* LIST () "/" "broken"`)

	lines, tagged = c.command("A02", `list "" "f*" return (special-use status (uidnext messages))`)
	requireOK(t, tagged, "A02")
	requireExactly(t, lines, `* LIST () "/" "foo"`, `* STATUS "foo" (UIDNEXT 31 MESSAGES 30)`)

	lines, tagged = c.command("A03", `LIST "" "foo"`)
	requireOK(t, tagged, "A03")
	requireExactly(t, lines, `* LIST () "/" "foo"`)
}

// TestListStatusRefusals: the STATUS return option is STATUS SP "(" status-att
// *(SP status-att) ")" (RFC 5819 §4): an empty or unknown item, a missing
// list, doubled spaces or an unknown return option is BAD with no response.
// The historical repeated-STATUS case is retained with its corrected RFC5258
// section3 expectation: repeated options act as one; the first list is used.
//
// checklist: IMAP-LISTSTATUS-03
func TestListStatusRefusals(t *testing.T) {
	c := selectionClient(t, statusFolders{})
	for _, ret := range []string{
		"(STATUS ())",
		"(STATUS (MESSAGES BOGUS))",
		"(STATUS)",
		"(STATUS MESSAGES)",
		"(STATUS  (MESSAGES))",
		"(STATUS (MESSAGES  UNSEEN))",
		"(STATUS (MESSAGES) STATUS (UNSEEN))",
		"(STATUS (MESSAGES) )",
		"(MYRIGHTS)",
	} {
		lines, tagged := c.command("B1", `LIST "" * RETURN %s`, ret)
		if ret == "(STATUS (MESSAGES) STATUS (UNSEEN))" {
			requireOK(t, tagged, "B1")
			requireExactly(t, lines,
				`* LIST () "/" "INBOX"`, `* STATUS "INBOX" (MESSAGES 17)`,
				`* LIST () "/" "foo"`, `* STATUS "foo" (MESSAGES 30)`,
				`* LIST () "/" "broken"`)
			continue
		}
		if !strings.HasPrefix(tagged, "B1 BAD ") || len(lines) != 0 {
			t.Fatalf("RETURN %s: %q %q, want BAD", ret, lines, tagged)
		}
	}
}
