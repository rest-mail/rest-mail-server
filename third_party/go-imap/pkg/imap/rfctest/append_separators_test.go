package rfctest

import (
	"strings"
	"testing"
)

// TestAppendInvalidSeparators checks RFC 3501 §9 through the public Session:
// APPEND's mailbox, optional flag-list, optional date-time and message literal
// are separated by exactly one SP. A missing, doubled, leading or tab separator
// answers BAD before a continuation invites message bytes. Every case is
// otherwise well formed, so a lenient parser would store the message.
//
// checklist: IMAP-APPEND-26
func TestAppendInvalidSeparators(t *testing.T) {
	const date = `" 7-Feb-1994 21:52:25 -0800"`
	for _, args := range []string{
		" INBOX {3}",
		"INBOX  {3}",
		"INBOX{3}",
		`"INBOX"{3}`,
		"INBOX\t{3}",
		`INBOX(\Seen) {3}`,
		`"INBOX"(\Seen) {3}`,
		`INBOX  (\Seen) {3}`,
		`INBOX (\Seen)  {3}`,
		`INBOX (\Seen)  ` + date + ` {3}`,
		`INBOX  ` + date + ` {3}`,
		`INBOX ` + date + `{3}`,
		`INBOX ` + date + `  {3}`,
		`INBOX (\Seen) ` + date + "\t{3}",
	} {
		t.Run(args, func(t *testing.T) {
			box := &datedAppendMailbox{&appendMailbox{}}
			c := selectionClient(t, box)
			lines, tagged := c.command("A1", "APPEND %s", args)
			if !strings.HasPrefix(tagged, "A1 BAD ") || len(lines) != 0 {
				t.Fatalf("invalid separators: replies=%q tagged=%q, want only BAD", lines, tagged)
			}
			if len(box.records) != 0 {
				t.Fatalf("invalid separators stored: %+v", box.records)
			}
			_, tagged = c.command("N1", "NOOP")
			requireOK(t, tagged, "N1")
		})
	}
}

// TestAppendSingleSeparators preserves every optional-argument combination
// separated by single SP, including a quoted mailbox directly before the date.
//
// checklist: IMAP-APPEND-26
func TestAppendSingleSeparators(t *testing.T) {
	const date = `" 7-Feb-1994 21:52:25 -0800"`
	for _, args := range []string{
		"INBOX",
		`"INBOX"`,
		`INBOX (\Seen)`,
		`"INBOX" ` + date,
		`INBOX (\Seen) ` + date,
	} {
		t.Run(args, func(t *testing.T) {
			box := &datedAppendMailbox{&appendMailbox{}}
			c := selectionClient(t, box)
			_, tagged := appendCommand(t, c, args, "msg")
			requireOK(t, tagged, "A1")
			if len(box.records) != 1 || box.records[0].folder != "INBOX" || box.records[0].raw != "msg" {
				t.Fatalf("records: %+v", box.records)
			}
		})
	}
}
