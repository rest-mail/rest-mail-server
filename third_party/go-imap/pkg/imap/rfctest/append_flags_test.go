package rfctest

import (
	"strings"
	"testing"
)

// TestAppendInvalidFlagList checks RFC 3501 §§6.3.11 and 9 through the public
// Session: APPEND's optional flag-list is a parenthesised, possibly empty, list
// of flags separated by a single SP and followed by SP, and each flag is an atom
// with an optional leading "\". Anything else answers BAD before a continuation
// invites message bytes. Every case names only supported flags, so a lenient
// parser would store the message.
//
// checklist: IMAP-APPEND-24
func TestAppendInvalidFlagList(t *testing.T) {
	for _, flags := range []string{
		`((\Seen \Draft))`,
		`(\Seen) (\Draft)`,
		`(\Seen)(\Draft)`,
		`(\Seen \Draft){3}`,
		`(\Seen  \Draft)`,
		`( \Seen \Draft)`,
		`(\Seen \Draft )`,
		`( )`,
		"(\\Seen\t\\Draft)",
		`(\Seen\Draft)`,
		`(\\Seen \Draft)`,
		`(\Seen \ \Draft)`,
		`(\Seen \* \Draft)`,
		`(\Seen "\Draft")`,
		`(\Seen {3}`,
	} {
		t.Run(flags, func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			args := "INBOX " + flags
			if !strings.HasSuffix(flags, "{3}") {
				args += " {3}"
			}
			lines, tagged := c.command("A1", "APPEND %s", args)
			if !strings.HasPrefix(tagged, "A1 BAD ") || len(lines) != 0 {
				t.Fatalf("invalid flags: replies=%q tagged=%q, want only BAD", lines, tagged)
			}
			if len(box.records) != 0 {
				t.Fatalf("invalid flags stored: %+v", box.records)
			}
			_, tagged = c.command("N1", "NOOP")
			requireOK(t, tagged, "N1")
		})
	}
}

// TestAppendValidFlagList preserves the flag-list forms a client may send: an
// empty list, one flag, and several flags separated by single SP. A
// well-formed list naming a flag the store cannot keep stays NO, not BAD.
//
// checklist: IMAP-APPEND-25
func TestAppendValidFlagList(t *testing.T) {
	for _, tc := range []struct {
		flags                          string
		seen, answered, flagged, draft bool
	}{
		{flags: `($Label1)`},
		{flags: `()`},
		{flags: `(\Seen)`, seen: true},
		{flags: `(\Answered \Deleted)`, answered: true},
		{flags: `(\Flagged \Draft \Seen)`, seen: true, flagged: true, draft: true},
	} {
		t.Run(tc.flags, func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			_, tagged := appendCommand(t, c, "INBOX "+tc.flags, "msg")
			requireOK(t, tagged, "A1")
			if len(box.records) != 1 || box.records[0].raw != "msg" {
				t.Fatalf("records: %+v", box.records)
			}
			f := box.records[0].flags
			for name, tt := range map[string]struct {
				got  *bool
				want bool
			}{"Seen": {f.Seen, tc.seen}, "Answered": {f.Answered, tc.answered}, "Flagged": {f.Flagged, tc.flagged}, "Draft": {f.Draft, tc.draft}} {
				if (tt.got != nil && *tt.got) != tt.want || (tt.got != nil) != tt.want {
					t.Errorf("%s: got %v, want %t", name, tt.got, tt.want)
				}
			}
		})
	}
	for _, flags := range []string{`(\Unknown)`, `(\Seen \X-Extension)`} {
		t.Run(flags, func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			_, tagged := c.command("A1", "APPEND INBOX %s {3}", flags)
			if !strings.HasPrefix(tagged, "A1 NO ") {
				t.Fatalf("unsupported flag: %q", tagged)
			}
			if len(box.records) != 0 {
				t.Fatalf("unsupported flag stored: %+v", box.records)
			}
		})
	}
}
