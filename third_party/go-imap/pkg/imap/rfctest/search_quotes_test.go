package rfctest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// searchQuoted encodes the two quoted-specials of RFC 3501 §§4.3, 9.
func searchQuoted(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

func quotedSearchMailbox(value string) *fetchMailbox {
	box := newFetchMailbox()
	box.messages = []imap.Message{
		{UID: 10, Subject: value, From: imap.Address{Email: value}, To: value},
		{UID: 70, Subject: "unrelated", From: imap.Address{Email: "unrelated"}, To: "unrelated"},
	}
	box.raw = map[uint32]string{
		10: "Subject: " + value + "\r\nCc: " + value + "\r\nBcc: " + value + "\r\nX-Test: " + value + "\r\n\r\n" + value,
		70: "Subject: unrelated\r\n\r\nunrelated",
	}
	return box
}

// TestSearchQuotedStrings checks escape decoding and token boundaries in all
// supported astring search operands, including Boolean/group contexts.
//
// checklist: IMAP-SEARCH-24
func TestSearchQuotedStrings(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, value := range []string{`a"b`, `a\b`, `a\"b`, `end\`, `"`, `a" (b) \c`} {
			t.Run(fmt.Sprintf("%s/%q", selection, value), func(t *testing.T) {
				box := quotedSearchMailbox(value)
				c := fetchClient(t, box, selection)
				for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
					one, other := "1", "2"
					if prefix == "UID SEARCH" {
						one, other = "10", "70"
					}
					for _, key := range []string{"FROM", "TO", "SUBJECT", "BODY", "TEXT", "CC", "BCC", "HEADER X-Test", `HEADER "X-Test"`} {
						query := key + " " + searchQuoted(value)
						for _, tc := range []struct{ query, result string }{
							{query, one}, {"NOT " + query, other},
							{"OR " + query + " UID 70", one + " " + other},
							{"(" + query + " ALL)", one},
						} {
							requireSearch(t, c, prefix+" "+tc.query, "* SEARCH "+tc.result)
						}
					}
				}
				if len(box.updates) != 0 {
					t.Fatalf("search changed flags: %v", box.updates)
				}
			})
		}
	}
}

// TestSearchInvalidQuotes checks that invalid quotes fail before any criterion
// is evaluated, even after a content key and with an empty selected mailbox.
//
// checklist: IMAP-SEARCH-25
func TestSearchInvalidQuotes(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/empty=%t", selection, empty), func(t *testing.T) {
				box := quotedSearchMailbox("hello")
				if empty {
					box.messages = nil
				}
				c := fetchClient(t, box, selection)
				for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
					for _, key := range []string{"FROM", "TO", "SUBJECT", "BODY", "TEXT", "CC", "BCC", "HEADER X-Test", "HEADER"} {
						for _, operand := range []string{`"hello`, `"`, `"hello\`, `"hello\"`, `"he\llo"`, `"he\nllo"`, `"hello"ALL`, `"hello""world"`, `he"llo`, "\"he\x00llo\""} {
							query := key + " " + operand
							if key == "HEADER" {
								query += " hello"
							}
							for _, form := range []string{"%s", "NOT %s", "OR ALL %s", "(ALL %s)", `BODY "hello" %s`} {
								q := fmt.Sprintf(form, query)
								t.Run(prefix+"/"+q, func(t *testing.T) {
									lines, tagged := c.command("Q1", "%s %s", prefix, q)
									if !strings.HasPrefix(tagged, "Q1 BAD ") || len(lines) != 0 {
										t.Errorf("invalid quotes: lines=%q tagged=%q, want only BAD", lines, tagged)
									}
									if len(box.fetches) != 0 || len(box.updates) != 0 {
										t.Fatalf("invalid search accessed backend: fetches=%v updates=%v", box.fetches, box.updates)
									}
									lines, tagged = c.command("N1", "NOOP")
									requireOK(t, tagged, "N1")
									requireExactly(t, lines)
								})
							}
						}
					}
					lines, tagged := c.command("Q1", `%s CHARSET "US\-ASCII" ALL`, prefix)
					if !strings.HasPrefix(tagged, "Q1 BAD ") || len(lines) != 0 {
						t.Errorf("invalid charset quoting: lines=%q tagged=%q", lines, tagged)
					}
					want := "* SEARCH"
					if !empty {
						if prefix == "SEARCH" {
							want += " 1 2"
						} else {
							want += " 10 70"
						}
					}
					requireSearch(t, c, prefix+" ALL", want)
				}
			})
		}
	}
}

// TestSearchQuotedLiterals checks the shared literal representation: escaped
// quotes/backslashes, parentheses, CRLF and UTF-8 literal bytes stay data.
//
// checklist: IMAP-SEARCH-24
func TestSearchQuotedLiterals(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, value := range []string{`a" (b) \c`, "café\r\nnext", "", `end\`} {
			for _, sync := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%q/sync=%t", selection, value, sync), func(t *testing.T) {
					box := quotedSearchMailbox(value)
					c := fetchClient(t, box, selection)
					for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
						for _, key := range []string{"SUBJECT", "BODY", "TEXT"} {
							marker := fmt.Sprintf("{%d+}", len(value))
							if sync {
								marker = fmt.Sprintf("{%d}", len(value))
							}
							if _, err := fmt.Fprintf(c.conn, "Q1 %s %s %s\r\n", prefix, key, marker); err != nil {
								t.Fatal(err)
							}
							if sync {
								if line := c.line(); !strings.HasPrefix(line, "+ ") {
									t.Fatalf("continuation = %q", line)
								}
							}
							if _, err := fmt.Fprintf(c.conn, "%s\r\n", value); err != nil {
								t.Fatal(err)
							}
							want := "* SEARCH 1"
							if prefix == "UID SEARCH" {
								want = "* SEARCH 10"
							}
							if value == "" {
								if prefix == "SEARCH" {
									want += " 2"
								} else {
									want += " 70"
								}
							}
							if line := c.line(); line != want {
								t.Fatalf("result = %q, want %q", line, want)
							}
							requireOK(t, c.line(), "Q1")
						}
					}
					if len(box.updates) != 0 {
						t.Fatalf("search changed flags: %v", box.updates)
					}
				})
			}
		}
	}
}
