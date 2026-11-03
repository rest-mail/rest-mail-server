package rfctest

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// statusMailbox records metadata reads as well as all content/flag operations.
// The embedded selection fixture returns independent message slices.
type statusMailbox struct {
	*exampleMailbox
	reads    []string
	validity []string
	fetches  int
}

func newStatusMailbox() *statusMailbox {
	return &statusMailbox{exampleMailbox: &exampleMailbox{&selectionMailbox{
		folders: map[string][]imap.Message{
			"INBOX": {{UID: 7, Seen: false, Flagged: true}, {UID: 90, Seen: true}},
			"Other": {{UID: 42, Seen: true}},
			"Empty": nil,
		},
	}}}
}

func (m *statusMailbox) Messages(folder string) ([]imap.Message, error) {
	m.reads = append(m.reads, folder)
	return m.selectionMailbox.Messages(folder)
}

func (m *statusMailbox) UIDValidity(folder string) (uint32, error) {
	m.validity = append(m.validity, folder)
	if folder == "INBOX" {
		return 123, nil
	}
	return 456, nil
}

func (m *statusMailbox) Fetch(uint32) ([]byte, error) {
	m.fetches++
	return nil, fmt.Errorf("STATUS must not read content")
}

func (m *statusMailbox) requireNoWrites(t *testing.T) {
	t.Helper()
	if m.fetches+m.stores+m.deletes+m.moves != 0 {
		t.Fatalf("unexpected operations: fetch=%d store=%d delete=%d move=%d", m.fetches, m.stores, m.deletes, m.moves)
	}
}

// TestStatusExample uses the command, count and UIDNEXT from RFC 3501 §6.3.10.
// The server may return items in request order and quote the response mailbox.
//
// checklist: IMAP-STATUS-01
func TestStatusExample(t *testing.T) {
	box := newStatusMailbox()
	messages := make([]imap.Message, 231)
	for i := range messages {
		messages[i] = imap.Message{UID: uint32(i + 1), Seen: true}
	}
	messages[len(messages)-1].UID = 44291
	box.folders["blurdybloop"] = messages
	c := selectionClient(t, box)
	lines, tagged := c.command("A042", "STATUS blurdybloop (UIDNEXT MESSAGES)")
	requireOK(t, tagged, "A042")
	if !reflect.DeepEqual(lines, []string{`* STATUS "blurdybloop" (UIDNEXT 44292 MESSAGES 231)`}) {
		t.Fatalf("RFC example response: %q", lines)
	}
	box.requireNoWrites(t)
}

// TestStatusRequestedItems checks exact item membership, values, order and case
// independently of the currently selected mailbox. RECENT is intentionally not
// asserted: the backend has no independent Recent field.
//
// checklist: IMAP-STATUS-01 IMAP-STATUS-02
// checklist: IMAP2-SEC-227
func TestStatusRequestedItems(t *testing.T) {
	for _, tc := range []struct{ args, response string }{
		{`INBOX (MESSAGES)`, `"INBOX" (MESSAGES 2)`},
		{`inBoX (unseen uidvalidity uidnext messages)`, `"INBOX" (UNSEEN 1 UIDVALIDITY 123 UIDNEXT 91 MESSAGES 2)`},
		{`Other (UIDVALIDITY MESSAGES UNSEEN UIDNEXT)`, `"Other" (UIDVALIDITY 456 MESSAGES 1 UNSEEN 0 UIDNEXT 43)`},
		{`Empty (UNSEEN UIDNEXT UIDVALIDITY MESSAGES)`, `"Empty" (UNSEEN 0 UIDNEXT 1 UIDVALIDITY 456 MESSAGES 0)`},
		{`INBOX (MESSAGES MESSAGES)`, `"INBOX" (MESSAGES 2 MESSAGES 2)`},
	} {
		t.Run(tc.args, func(t *testing.T) {
			box := newStatusMailbox()
			c := selectionClient(t, box)
			_, selected := c.command("M1", "SELECT Other")
			requireOK(t, selected, "M1")
			lines, tagged := c.command("S1", "sTaTuS %s", tc.args)
			requireOK(t, tagged, "S1")
			if !reflect.DeepEqual(lines, []string{"* STATUS " + tc.response}) {
				t.Fatalf("response %q, want %q", lines, tc.response)
			}
			box.requireNoWrites(t)
		})
	}
}

// TestStatusInvalidArguments checks complete-command validation before any
// metadata read, and proves BAD leaves authentication/selection reusable.
//
// checklist: IMAP-STATUS-03 IMAP-STATUS-04
func TestStatusInvalidArguments(t *testing.T) {
	cases := []struct{ name, args string }{
		{"missing mailbox", ""}, {"missing list", "INBOX"},
		{"bare item", "INBOX MESSAGES"}, {"empty list", "INBOX ()"},
		{"missing close", "INBOX (MESSAGES"}, {"nested", "INBOX ((MESSAGES))"},
		{"extra close", "INBOX (MESSAGES))"}, {"extra open", "INBOX (MESSAGES)("},
		{"trailing text", "INBOX (MESSAGES) junk"}, {"second list", "INBOX (MESSAGES) (UNSEEN)"},
		{"nonascii item", "INBOX (meſſages)"},
		{"unknown", "INBOX (MESSAGES BOGUS)"}, {"quoted item", `INBOX ("MESSAGES")`},
		{"leading list space", "INBOX ( MESSAGES)"}, {"trailing list space", "INBOX (MESSAGES )"},
		{"doubled list space", "INBOX (MESSAGES  UNSEEN)"}, {"tab list space", "INBOX (MESSAGES\tUNSEEN)"},
		{"vertical list space", "INBOX (MESSAGES\vUNSEEN)"}, {"unicode list space", "INBOX (MESSAGES\u00a0UNSEEN)"},
		{"missing separator", `"INBOX"(MESSAGES)`}, {"doubled separator", "INBOX  (MESSAGES)"},
		{"tab separator", "INBOX\t(MESSAGES)"}, {"leading space", " INBOX (MESSAGES)"},
		{"trailing space", "INBOX (MESSAGES) "}, {"trailing tab", "INBOX (MESSAGES)\t"},
		{"unclosed mailbox", `"INBOX (MESSAGES)`}, {"bad escape", `"IN\qBOX" (MESSAGES)`},
		{"atom star", "* (MESSAGES)"}, {"atom percent", "% (MESSAGES)"},
		{"atom backslash", `In\box (MESSAGES)`}, {"atom quote", `In"box (MESSAGES)`},
		{"atom close", "In)box (MESSAGES)"}, {"atom open", "In(box (MESSAGES)"},
		{"atom brace", "In{box (MESSAGES)"}, {"atom DEL", "In\x7fbox (MESSAGES)"},
		{"atom NUL", "In\x00box (MESSAGES)"}, {"quoted NUL", "\"In\x00box\" (MESSAGES)"},
		{"atom nonascii", "caf\xc3\xa9 (MESSAGES)"},
	}
	for _, mode := range []string{"authenticated", "SELECT", "EXAMINE"} {
		t.Run(mode, func(t *testing.T) {
			box := newStatusMailbox()
			c := selectionClient(t, box)
			if mode != "authenticated" {
				_, tagged := c.command("M1", "%s INBOX", mode)
				requireOK(t, tagged, "M1")
			}
			box.reads, box.validity = nil, nil
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					lines, tagged := c.command("S1", "STATUS %s", tc.args)
					if len(lines) != 0 || !strings.HasPrefix(tagged, "S1 BAD ") {
						t.Fatalf("invalid arguments: %q %q", lines, tagged)
					}
					if len(box.reads)+len(box.validity) != 0 {
						t.Fatalf("invalid command reached metadata backend: %q %q", box.reads, box.validity)
					}
				})
			}
			assertStatusSelection(t, c, mode)
			box.requireNoWrites(t)
			lines, tagged := c.command("R1", "STATUS Other (MESSAGES)")
			requireOK(t, tagged, "R1")
			requireLine(t, lines, `* STATUS "Other" (MESSAGES 1)`)
		})
	}
}

func assertStatusSelection(t *testing.T, c *client, mode string) {
	t.Helper()
	lines, tagged := c.command("F1", "FETCH 1 (UID FLAGS)")
	if mode == "authenticated" {
		if len(lines) != 0 || !strings.HasPrefix(tagged, "F1 NO ") {
			t.Fatalf("STATUS selected a mailbox: %q %q", lines, tagged)
		}
		return
	}
	requireOK(t, tagged, "F1")
	if !reflect.DeepEqual(lines, []string{`* 1 FETCH (UID 7 FLAGS (\Flagged))`}) {
		t.Fatalf("STATUS changed selected messages or flags: %q", lines)
	}
}

// TestStatusPreservesSelection proves successful and failed status lookups do
// not select a queried mailbox, change flags or turn an
// EXAMINE selection writable. Refused unauthenticated requests never reach the
// mailbox. The fixture distinguishes current backend metadata from selection.
//
// checklist: IMAP-STATUS-04 IMAP-STATUS-05
// checklist: IMAP2-CMD-122
func TestStatusPreservesSelection(t *testing.T) {
	for _, mode := range []string{"unauthenticated", "authenticated", "SELECT", "EXAMINE"} {
		t.Run(mode, func(t *testing.T) {
			box := newStatusMailbox()
			c := dialMailbox(t, box)
			c.line()
			if mode != "unauthenticated" {
				c.login()
			}
			if mode == "SELECT" || mode == "EXAMINE" {
				_, tagged := c.command("M1", "%s INBOX", mode)
				requireOK(t, tagged, "M1")
			}
			box.reads, box.validity = nil, nil
			for _, folder := range []string{"Other", "Missing", "Empty"} {
				lines, tagged := c.command("S1", "STATUS %s (MESSAGES UNSEEN)", folder)
				if mode == "unauthenticated" || folder == "Missing" {
					if len(lines) != 0 || !strings.HasPrefix(tagged, "S1 NO ") {
						t.Fatalf("refusal: %q %q", lines, tagged)
					}
				} else {
					requireOK(t, tagged, "S1")
					count := 0
					if folder == "Other" {
						count = 1
					}
					requireLine(t, lines, fmt.Sprintf(`* STATUS "%s" (MESSAGES %d UNSEEN 0)`, folder, count))
				}
			}
			if mode == "unauthenticated" {
				if len(box.reads)+len(box.validity) != 0 {
					t.Fatalf("unauthenticated metadata access: %q %q", box.reads, box.validity)
				}
				return
			}
			assertStatusSelection(t, c, mode)
			box.requireNoWrites(t)
			if mode == "SELECT" || mode == "EXAMINE" {
				_, tagged := c.command("W1", `STORE 1 +FLAGS.SILENT (\Answered)`)
				if mode == "SELECT" {
					requireOK(t, tagged, "W1")
					if box.stores != 1 {
						t.Fatal("STATUS lost writable selection")
					}
				} else if !strings.HasPrefix(tagged, "W1 NO ") || box.stores != 0 {
					t.Fatalf("STATUS lost read-only selection: %q, stores=%d", tagged, box.stores)
				}
			}
		})
	}
}

// TestStatusMailboxStrings checks astring decoding and echoing, including
// counted literals whose payload is not constrained to quoted-string octets.
//
// checklist: IMAP-STATUS-06
func TestStatusMailboxStrings(t *testing.T) {
	for _, tc := range []struct{ token, folder, response string }{
		{`"Work Projects"`, "Work Projects", `"Work Projects"`},
		{`"Work\"box"`, `Work"box`, `"Work\"box"`},
		{`"Work\\box"`, `Work\box`, `"Work\\box"`},
		{`"*%"`, "*%", `"*%"`},
		{`box]`, "box]", `"box]"`},
		{`""`, "", `""`},
	} {
		t.Run(tc.token, func(t *testing.T) {
			box := newStatusMailbox()
			box.folders[tc.folder] = nil
			c := selectionClient(t, box)
			lines, tagged := c.command("S1", "STATUS %s (MESSAGES)", tc.token)
			requireOK(t, tagged, "S1")
			if !reflect.DeepEqual(lines, []string{"* STATUS " + tc.response + " (MESSAGES 0)"}) || !reflect.DeepEqual(box.reads, []string{tc.folder}) {
				t.Fatalf("mailbox round trip: lines=%q reads=%q", lines, box.reads)
			}
			box.requireNoWrites(t)
		})
	}
	for _, sync := range []bool{false, true} {
		// The literal carries the modified UTF-7 name (RFC 3501 §5.1.3), the
		// backend gets it decoded, and STATUS answers with the name as sent.
		for _, tc := range []struct{ folder, stored string }{
			{"Work \"box\\", "Work \"box\\"}, {"caf&AOk-", "café"},
			{"line&AA0ACg-break", "line\r\nbreak"}, {"bad\x00name", ""},
		} {
			folder, stored := tc.folder, tc.stored
			t.Run(fmt.Sprintf("literal/%t/%q", sync, folder), func(t *testing.T) {
				box := newStatusMailbox()
				box.folders[stored] = nil
				c := selectionClient(t, box)
				payload := folder + " (MESSAGES)\r\nN1 NOOP\r\n"
				if sync {
					writeLiteralWire(t, c, fmt.Sprintf("S1 STATUS {%d}\r\n", len(folder)))
					if line := c.line(); !strings.HasPrefix(line, "+ ") {
						t.Fatalf("continuation: %q", line)
					}
					writeLiteralWire(t, c, payload)
				} else {
					writeLiteralWire(t, c, fmt.Sprintf("S1 STATUS {%d+}\r\n%s", len(folder), payload))
				}
				if strings.ContainsRune(folder, 0) {
					if line := c.line(); !strings.HasPrefix(line, "S1 BAD ") {
						t.Fatalf("NUL refusal: %q", line)
					}
					if len(box.reads) != 0 {
						t.Fatalf("NUL reached backend: %q", box.reads)
					}
				} else {
					want := fmt.Sprintf("* STATUS %q (MESSAGES 0)", folder)
					// Read the exact wire bytes, including any response literal.
					got := make([]byte, len(want)+2)
					if _, err := io.ReadFull(c.r, got); err != nil {
						t.Fatal(err)
					}
					if string(got) != want+"\r\n" {
						t.Fatalf("literal response: %q, want %q", got, want)
					}
					requireOK(t, c.line(), "S1")
					if !reflect.DeepEqual(box.reads, []string{stored}) {
						t.Fatalf("literal backend name: %q, want %q", box.reads, stored)
					}
				}
				requireOK(t, c.line(), "N1")
				box.requireNoWrites(t)
			})
		}
	}
}
