package rfctest

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// TestFetchInvalidHeaderLists rejects malformed HEADER.FIELDS forms before any
// item is dispatched, even when an earlier BODY[] would have set Seen.
//
// checklist: IMAP-FETCH-22
func TestFetchInvalidHeaderLists(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, target := range []string{"FETCH 1", "UID FETCH 10", "UID FETCH 999", "empty"} {
			for _, section := range []string{
				"HEADER.FIELDS", "HEADER.FIELDS.NOT", "HEADER.FIELDSX (SUBJECT)",
				"HEADER.FIELDS.NOTX (SUBJECT)", "HEADER.FIELDS.NOT.NOT (SUBJECT)",
				"HEADER.FIELDS(SUBJECT)", "HEADER.FIELDS  (SUBJECT)", "HEADER.FIELDS\t(SUBJECT)",
				"HEADER.FIELDS ()", "HEADER.FIELDS ( )", "HEADER.FIELDS ( SUBJECT)",
				"HEADER.FIELDS (SUBJECT )", "HEADER.FIELDS (SUBJECT  FROM)",
				"HEADER.FIELDS (SUBJECT\tFROM)", "HEADER.FIELDS ((SUBJECT))",
				"HEADER.FIELDS (SUBJECT) EXTRA", "HEADER.FIELDS (SUBJECT)(FROM)",
				"HEADER.FIELDS (* )", "HEADER.FIELDS (%)", `HEADER.FIELDS ("SUBJECT"FROM)`,
				`HEADER.FIELDS ("SUBJECT""FROM")`, `HEADER.FIELDS ("SUB\JECT")`,
				`HEADER.FIELDS (SUB\JECT)`, "HEADER.FIELDS ({7})",
				"HEADER.FIELDS.NOT ()", "HEADER.FIELDS.NOT (SUBJECT)TEXT",
			} {
				t.Run(selection+"/"+target+"/"+section, func(t *testing.T) {
					box := newFetchMailbox()
					if target == "empty" {
						box.messages = nil
					}
					c := fetchClient(t, box, selection)
					prefix := target
					if target == "empty" {
						prefix = "FETCH *"
					}
					replies, tagged := fetchCommand(t, c, prefix+" (BODY[] BODY.PEEK["+section+"])")
					if len(replies) != 0 || !strings.HasPrefix(tagged, "F1 BAD ") {
						t.Fatalf("want only BAD, got %#v %q", replies, tagged)
					}
					if len(box.fetches) != 0 || len(box.updates) != 0 {
						t.Fatalf("backend effects: %v %v", box.fetches, box.updates)
					}
					lines, tagged := c.command("N1", "NOOP")
					requireOK(t, tagged, "N1")
					requireExactly(t, lines)
					lines, tagged = c.command("N2", "FETCH 1 FLAGS")
					requireOK(t, tagged, "N2")
					if target != "empty" {
						requireExactly(t, lines, "* 1 FETCH (UID 10 FLAGS ())")
					} else {
						requireExactly(t, lines)
					}
				})
			}
		}
	}
}

// TestFetchHeaderAstrings proves decoded names, exact case-insensitive selection,
// valid response labels, partial-after-subset slicing and Seen/EXAMINE rules.
//
// checklist: IMAP-FETCH-23
func TestFetchHeaderAstrings(t *testing.T) {
	const raw = "Subject: folded\r\n\tvalue\r\nFrom: sender\r\nX-]: right\r\nX-[: left\r\nX-(: paren\r\nX-\"Quote: quote\r\nX-\\Slash: slash\r\n\r\nbody"
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, prefix := range []string{"FETCH 1", "UID FETCH 10"} {
			for _, peek := range []bool{false, true} {
				for _, tc := range []struct{ list, payload string }{
					{`("sUbJeCt" FROM)`, "Subject: folded\r\n\tvalue\r\nFrom: sender\r\n\r\n"},
					{`("X-]" X-[)`, "X-]: right\r\nX-[: left\r\n\r\n"},
					{`(X-])`, "X-]: right\r\n\r\n"},
					{`("X-(" "X-\"Quote" "X-\\Slash")`, "X-(: paren\r\nX-\"Quote: quote\r\nX-\\Slash: slash\r\n\r\n"},
					{`(" Subject " "" "missing field")`, "\r\n"},
					{`(Subject Subject "SUBJECT")`, "Subject: folded\r\n\tvalue\r\n\r\n"},
				} {
					for _, exclude := range []bool{false, true} {
						for _, partial := range []bool{false, true} {
							t.Run(fmt.Sprintf("%s/%s/peek=%t/not=%t/partial=%t/%s", selection, prefix, peek, exclude, partial, tc.list), func(t *testing.T) {
								box := newFetchMailbox()
								box.raw[10] = raw
								c := fetchClient(t, box, selection)
								section := "HEADER.FIELDS"
								if exclude {
									section += ".NOT"
								}
								section += " " + tc.list
								item := "BODY"
								if peek {
									item += ".PEEK"
								}
								item += "[" + section + "]"
								want := tc.payload
								if exclude {
									var b strings.Builder
									// Remove whole selected fields, preserving their continuations.
									keep := false
									for _, line := range strings.Split(strings.SplitN(raw, "\r\n\r\n", 2)[0], "\r\n") {
										if line[0] != '\t' {
											keep = !strings.Contains(tc.payload, line+"\r\n")
										}
										if keep {
											b.WriteString(line + "\r\n")
										}
									}
									b.WriteString("\r\n")
									want = b.String()
								}
								label := "BODY[" + section + "]"
								if partial {
									item += "<1.7>"
									label += "<1>"
									if len(want) > 8 {
										want = want[1:8]
									} else {
										want = want[1:]
									}
								}
								replies, tagged := fetchCommand(t, c, prefix+" "+item)
								requireOK(t, tagged, "F1")
								if len(replies) != 1 || len(replies[0].literals) != 1 || replies[0].literals[0] != want {
									t.Fatalf("payload: %#v want %q", replies, want)
								}
								if !strings.Contains(replies[0].framing, fmt.Sprintf(" %s {%d}", label, len(want))) {
									t.Fatalf("label: %q", replies[0].framing)
								}
								seen := !peek && selection == "SELECT"
								flags := "FLAGS ()"
								if seen {
									flags = `FLAGS (\Seen)`
								}
								if !strings.Contains(replies[0].framing, flags) {
									t.Fatalf("flags: %q", replies[0].framing)
								}
								writes := 0
								if seen {
									writes = 1
								}
								if len(box.fetches) != 1 || len(box.updates[10]) != writes {
									t.Fatalf("backend effects: %v %v", box.fetches, box.updates)
								}
							})
						}
					}
				}
			}
		}
	}
}

// TestFetchHeaderLiteralNames checks both literal synchronization forms use the
// same decoder and cannot inject a section delimiter or a following command.
//
// checklist: IMAP-FETCH-23
func TestFetchHeaderLiteralNames(t *testing.T) {
	for _, sync := range []bool{false, true} {
		for _, prefix := range []string{"FETCH 1", "UID FETCH 10"} {
			for _, name := range []string{"Subject", "X-])", `X-"Q\`, "Subject)] BODY[]"} {
				t.Run(fmt.Sprintf("%t/%s/%s", sync, prefix, name), func(t *testing.T) {
					box := newFetchMailbox()
					box.raw[10] = "Subject: folded\r\n\tvalue\r\nX-]): bracket\r\nX-\"Q\\: escaped\r\n\r\nbody"
					c := fetchClient(t, box, "SELECT")
					marker := fmt.Sprintf("{%d+}", len(name))
					if sync {
						marker = fmt.Sprintf("{%d}", len(name))
					}
					start := "F1 " + prefix + " BODY.PEEK[HEADER.FIELDS (" + marker + "\r\n"
					if sync {
						writeLiteralWire(t, c, start)
						if line := c.line(); !strings.HasPrefix(line, "+ ") {
							t.Fatalf("continuation: %q", line)
						}
						start = ""
					}
					writeLiteralWire(t, c, start+name+")]\r\nN1 NOOP\r\n")
					payload := "\r\n"
					switch name {
					case "Subject":
						payload = "Subject: folded\r\n\tvalue\r\n\r\n"
					case "X-])":
						payload = "X-]): bracket\r\n\r\n"
					case `X-"Q\`:
						payload = "X-\"Q\\: escaped\r\n\r\n"
					}
					quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `"`
					want := fmt.Sprintf("* 1 FETCH (UID 10 FLAGS () BODY[HEADER.FIELDS (%s)] {%d}\r\n%s)\r\n", quoted, len(payload), payload)
					got := make([]byte, len(want))
					if _, err := io.ReadFull(c.r, got); err != nil {
						t.Fatal(err)
					}
					if string(got) != want {
						t.Fatalf("wire: %q want %q", got, want)
					}
					requireOK(t, c.line(), "F1")
					requireOK(t, c.line(), "N1")
					if len(box.fetches) != 1 || len(box.updates) != 0 {
						t.Fatalf("effects: %v %v", box.fetches, box.updates)
					}
				})
			}
		}
	}
}
