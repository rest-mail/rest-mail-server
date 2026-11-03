package rfctest

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// checklist: IMAP-SELECT-11
func TestSelectionInvalidArguments(t *testing.T) {
	for _, command := range []string{"SELECT", "EXAMINE"} {
		for _, initial := range []string{"", "SELECT", "EXAMINE"} {
			for _, args := range []string{
				"", " INBOX", "INBOX ", "INBOX extra", `"INBOX" extra`,
				`"INBOX"extra`, `"unterminated`, `"bad\escape"`, `"trailing\`,
				"(INBOX)", "INBOX)", "INBOX*", "INBOX%", "INBOX{", `INBOX\`,
				`IN"BOX`, "INBOX\t", "\tINBOX", "INBOX\x00", "INBOX\x7f", "INBOX\xc3\xa9",
				"\"IN\x00BOX\"",
			} {
				t.Run(command+"/"+initial+"/"+fmt.Sprintf("%q", args), func(t *testing.T) {
					box := newStatusMailbox()
					c := selectionClient(t, box)
					if initial != "" {
						_, tagged := c.command("S1", "%s INBOX", initial)
						requireOK(t, tagged, "S1")
						if initial == "SELECT" {
							_, tagged = c.command("D1", `STORE 1 +FLAGS (\Deleted)`)
							requireOK(t, tagged, "D1")
						}
					}
					box.reads, box.validity = nil, nil
					stores := box.stores
					lines, tagged := c.command("S2", "%s %s", command, args)
					requireExactly(t, lines)
					if !strings.HasPrefix(tagged, "S2 BAD ") {
						t.Fatalf("invalid arguments: %q, want BAD", tagged)
					}
					if len(box.reads)+len(box.validity)+box.fetches+box.deletes+box.moves != 0 || box.stores != stores {
						t.Fatalf("invalid selection touched backend: reads=%q validity=%q stores=%d deletes=%d", box.reads, box.validity, box.stores, box.deletes)
					}
					lines, tagged = c.command("F1", "FETCH 1 (UID FLAGS)")
					requireExactly(t, lines)
					if strings.HasPrefix(tagged, "F1 OK ") {
						t.Fatalf("invalid selection retained old mailbox: %q", tagged)
					}
					// Reuse the old UID in a new mailbox: stale deletion marks must
					// not follow it, even when the failed command was EXAMINE.
					box.folders["Recovery"] = []imap.Message{{UID: 7}}
					_, tagged = c.command("S3", "SELECT Recovery")
					requireOK(t, tagged, "S3")
					lines, tagged = c.command("C1", "CLOSE")
					requireOK(t, tagged, "C1")
					requireExactly(t, lines)
					if box.deletes != 0 || box.stores != stores {
						t.Fatal("failed selection preserved deletion marks or wrote flags")
					}
				})
			}
		}
	}
}

// checklist: IMAP-SELECT-12
// checklist: IMAP2-SEC-227
func TestSelectionMailboxAstrings(t *testing.T) {
	for _, command := range []string{"SELECT", "EXAMINE"} {
		for _, tc := range []struct{ wire, name string }{
			{"inBoX", "INBOX"}, {`"inBoX"`, "INBOX"},
			{`"Project Archive"`, "Project Archive"}, {`" padded "`, " padded "},
			{`"Back\\slash\"quote"`, `Back\slash"quote`},
			{`"wild*%cards"`, "wild*%cards"}, {"a]b+c/d&-_=", "a]b+c/d&_="},
			{"caf&AOk-", "café"}, {`"&U,BTFw-/&ZeVnLIqe-"`, "台北/日本語"},
			{`""`, ""},
		} {
			t.Run(command+"/"+tc.wire, func(t *testing.T) {
				box := newStatusMailbox()
				box.folders[tc.name] = []imap.Message{{UID: 42, Seen: true}}
				c := selectionClient(t, box)
				lines, tagged := c.command("S1", "%s %s", command, tc.wire)
				requireOK(t, tagged, "S1")
				requireLine(t, lines, "* 1 EXISTS")
				if !reflect.DeepEqual(box.reads, []string{tc.name}) || !reflect.DeepEqual(box.validity, []string{tc.name}) {
					t.Fatalf("mailbox decoding: reads=%q validity=%q, want %q", box.reads, box.validity, tc.name)
				}
				box.requireNoWrites(t)
			})
		}
	}
}

// checklist: IMAP-SELECT-12
func TestSelectionLiteralMailbox(t *testing.T) {
	for _, command := range []string{"SELECT", "EXAMINE"} {
		for _, sync := range []bool{false, true} {
			// A literal name is modified UTF-7 too (RFC 3501 §5.1.3): é, CR and
			// LF travel in its base64.
			for _, tc := range []struct{ name, want string }{
				{"inBoX", "INBOX"}, {"Project Archive", "Project Archive"}, {" padded ", " padded "},
				{"Back\\slash\"quote", "Back\\slash\"quote"}, {"caf&AOk-", "café"},
				{"line&AA0ACg-name", "line\r\nname"}, {"", ""},
			} {
				name, want := tc.name, tc.want
				t.Run(fmt.Sprintf("%s/sync=%t/%q", command, sync, name), func(t *testing.T) {
					box := newStatusMailbox()
					box.folders[want] = []imap.Message{{UID: 42, Seen: true}}
					c := selectionClient(t, box)
					marker := "+"
					if sync {
						marker = ""
					}
					header := fmt.Sprintf("S1 %s {%d%s}\r\n", command, len(name), marker)
					payload := name + "\r\nN1 NOOP\r\n"
					if sync {
						writeLiteralWire(t, c, header)
						if line := c.line(); !strings.HasPrefix(line, "+ ") {
							t.Fatalf("continuation: %q", line)
						}
						writeLiteralWire(t, c, payload)
					} else {
						writeLiteralWire(t, c, header+payload)
					}
					var lines []string
					for {
						line := c.line()
						if strings.HasPrefix(line, "S1 ") {
							requireOK(t, line, "S1")
							break
						}
						lines = append(lines, line)
					}
					requireLine(t, lines, "* 1 EXISTS")
					requireOK(t, c.line(), "N1")
					// SELECT reads the mailbox, and the pipelined NOOP polls it again.
					if !reflect.DeepEqual(box.reads, []string{want, want}) || !reflect.DeepEqual(box.validity, []string{want}) {
						t.Fatalf("literal decoding: reads=%q validity=%q, want %q", box.reads, box.validity, want)
					}
					box.requireNoWrites(t)
				})
			}
		}
	}
}

// checklist: IMAP-SELECT-11
func TestSelectionInvalidLiteralArguments(t *testing.T) {
	for _, command := range []string{"SELECT", "EXAMINE"} {
		for _, tc := range []struct{ name, suffix string }{
			{"INBOX", " extra"}, {"INBOX", "extra"}, {"INBOX", " "}, {"IN\x00BOX", ""},
		} {
			t.Run(command+"/"+fmt.Sprintf("%q/%q", tc.name, tc.suffix), func(t *testing.T) {
				box := newStatusMailbox()
				c := selectionClient(t, box)
				writeLiteralWire(t, c, fmt.Sprintf("S1 %s {%d+}\r\n%s%s\r\nN1 NOOP\r\n", command, len(tc.name), tc.name, tc.suffix))
				if line := c.line(); !strings.HasPrefix(line, "S1 BAD ") {
					t.Fatalf("invalid literal argument: %q, want BAD without selection responses", line)
				}
				requireOK(t, c.line(), "N1")
				if len(box.reads)+len(box.validity) != 0 {
					t.Fatalf("invalid literal reached backend: reads=%q validity=%q", box.reads, box.validity)
				}
				box.requireNoWrites(t)
			})
		}
	}
}
