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
					// BAD never changes the selected mailbox (RFC 3501 §6). This test
					// used to require deselection here, which was the defect itself.
					lines, tagged = c.command("F1", "FETCH 1 (UID FLAGS)")
					switch initial {
					case "":
						requireExactly(t, lines)
						if strings.HasPrefix(tagged, "F1 OK ") {
							t.Fatalf("invalid selection selected a mailbox: %q", tagged)
						}
					case "SELECT":
						requireOK(t, tagged, "F1")
						requireExactly(t, lines, `* 1 FETCH (UID 7 FLAGS (\Flagged \Deleted))`)
					case "EXAMINE":
						requireOK(t, tagged, "F1")
						requireExactly(t, lines, `* 1 FETCH (UID 7 FLAGS (\Flagged))`)
					}
					// Reuse the old UID in a new mailbox: a later valid selection
					// recovers, and the old deletion marks must not follow it.
					box.folders["Recovery"] = []imap.Message{{UID: 7}}
					_, tagged = c.command("S3", "SELECT Recovery")
					requireOK(t, tagged, "S3")
					lines, tagged = c.command("C1", "CLOSE")
					requireOK(t, tagged, "C1")
					requireExactly(t, lines)
					if box.deletes != 0 || box.stores != stores {
						t.Fatal("new selection inherited deletion marks or wrote flags")
					}
				})
			}
		}
	}
}

// TestSelectionRefusalKeepsSelection: "A rejected command (BAD response) never
// changes the state of the connection or of the selected mailbox" (RFC 3501 §6).
// Only a failed SELECT or EXAMINE, one answered NO, gives up the old selection
// (TestSelectionFailureDropsOldMailbox).
// The refused command names another mailbox, so the old mailbox must keep
// answering; its read-only mode and deletion marks must survive with it. Every
// route to BAD is covered: the handler's own parse, literal octets it parses
// after splicing, and the refusals made while the command line is still being
// read.
//
// checklist: IMAP-SELECT-11
func TestSelectionRefusalKeepsSelection(t *testing.T) {
	for _, command := range []string{"SELECT", "EXAMINE"} {
		for _, initial := range []string{"SELECT", "EXAMINE"} {
			for _, args := range []string{
				"", "Other extra", `"Other" extra`, `"Other"extra`, "(Other)", "Other*",
				"Other\x00", "Other\xc3\xa9", "{5+}\r\nOther extra", "{5}\r\nOther extra", "{99999}",
			} {
				t.Run(initial+"/"+command+"/"+fmt.Sprintf("%q", args), func(t *testing.T) {
					box := newStatusMailbox()
					c := selectionClient(t, box)
					_, tagged := c.command("S1", "%s INBOX", initial)
					requireOK(t, tagged, "S1")
					if initial == "SELECT" {
						_, tagged = c.command("D1", `STORE 1 +FLAGS (\Deleted)`)
						requireOK(t, tagged, "D1")
					}
					box.reads, box.validity = nil, nil

					if !strings.HasPrefix(args, "{5}") {
						lines, tagged := c.command("S2", "%s %s", command, args)
						requireExactly(t, lines)
						if !strings.HasPrefix(tagged, "S2 BAD ") {
							t.Fatalf("invalid arguments: %q, want BAD", tagged)
						}
					} else {
						// A synchronizing literal: the octets wait for the continuation.
						writeLiteralWire(t, c, "S2 "+command+" {5}\r\n")
						if line := c.line(); !strings.HasPrefix(line, "+ ") {
							t.Fatalf("continuation: %q", line)
						}
						writeLiteralWire(t, c, "Other extra\r\n")
						if line := c.line(); !strings.HasPrefix(line, "S2 BAD ") {
							t.Fatalf("invalid literal argument: %q, want BAD", line)
						}
					}
					if len(box.reads)+len(box.validity) != 0 {
						t.Fatalf("refused selection reached backend: reads=%q validity=%q", box.reads, box.validity)
					}

					flags := `\Flagged`
					if initial == "SELECT" {
						flags = `\Flagged \Deleted`
					}
					lines, tagged := c.command("F1", "FETCH 1 (UID FLAGS)")
					requireOK(t, tagged, "F1")
					requireExactly(t, lines, fmt.Sprintf("* 1 FETCH (UID 7 FLAGS (%s))", flags))

					_, tagged = c.command("W1", `STORE 2 +FLAGS (\Flagged)`)
					if initial == "EXAMINE" && !strings.HasPrefix(tagged, "W1 NO ") {
						t.Fatalf("refused selection lost read-only mode: %q", tagged)
					}
					if initial == "SELECT" {
						requireOK(t, tagged, "W1")
					}

					// The deletion mark still belongs to this selection, so CLOSE
					// honors it, and EXAMINE's CLOSE still removes nothing.
					_, tagged = c.command("C1", "CLOSE")
					requireOK(t, tagged, "C1")
					want := 0
					if initial == "SELECT" {
						want = 1
					}
					if box.deletes != want {
						t.Fatalf("CLOSE removed %d messages, want %d", box.deletes, want)
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
