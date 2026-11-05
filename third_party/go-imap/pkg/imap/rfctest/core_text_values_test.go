package rfctest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

type coreTextMailbox struct{ *fetchMailbox }

func (*coreTextMailbox) EnableUTF8() error { return nil }

func coreTextClient(t *testing.T, enabled bool) (*client, *fetchMailbox) {
	t.Helper()
	box := newFetchMailbox()
	box.messages = []imap.Message{{UID: 10, Subject: "Grüße", Seen: true}}
	box.raw = map[uint32]string{10: "Subject: Grüße\r\n\r\nbody\r\n"}
	c := selectionClient(t, &coreTextMailbox{box})
	if enabled {
		enableUTF8(t, c)
	}
	_, tagged := c.command("S1", "SELECT INBOX")
	requireOK(t, tagged, "S1")
	return c, box
}

// TestCoreCommandNamesAreASCII keeps Unicode case folding from turning an
// unknown command into SELECT, SEARCH, STORE or CLOSE. No attempt may change
// selection or flags, with either an ordinary or UTF8=ACCEPT backend.
//
// checklist: IMAP-SYNTAX-09
func TestCoreCommandNamesAreASCII(t *testing.T) {
	for _, utf8 := range []bool{false, true} {
		for _, command := range []string{"ſELECT INBOX", "ſEARCH ALL", `ſTORE 1 +FLAGS (\Seen)`, "CLOſE"} {
			t.Run(fmt.Sprintf("utf8=%t/%s", utf8, command), func(t *testing.T) {
				box := newFetchMailbox()
				var c *client
				if utf8 {
					c, box = coreTextClient(t, true)
				} else {
					c = fetchClient(t, box, "SELECT")
				}
				lines, tagged := c.command("X1", "%s", command)
				if len(lines) != 0 || !strings.HasPrefix(tagged, "X1 BAD ") {
					t.Fatalf("non-ASCII command ran: %q %q", lines, tagged)
				}
				_, tagged = c.command("F1", "FETCH 1 FLAGS")
				requireOK(t, tagged, "F1")
				if len(box.updates) != 0 {
					t.Fatalf("invalid command changed flags: %+v", box.updates)
				}
			})
		}
	}
}

// TestCoreUTF8DoesNotExtendAtoms distinguishes quoted UTF-8 text from command
// keywords, UID subcommands, options and bare mailbox astrings. The restriction
// also applies before ENABLE on a backend that advertises UTF8=ACCEPT.
//
// checklist: IMAP-SYNTAX-10
func TestCoreUTF8DoesNotExtendAtoms(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, command := range []string{
			"SEARCH ſEEN", "UID ſEARCH ALL", "SEARCH SUBJECT Grüße",
			`LIST (ſUBSCRIBED) "" "*"`, "STATUS INBOX (MEſSAGES)",
			"SUBSCRIBE Grüße", "SEARCH (ALL OR ſEEN UNSEEN)",
		} {
			t.Run(fmt.Sprintf("enabled=%t/%s", enabled, command), func(t *testing.T) {
				c, box := coreTextClient(t, enabled)
				lines, tagged := c.command("X1", "%s", command)
				if len(lines) != 0 || !strings.HasPrefix(tagged, "X1 BAD ") {
					t.Fatalf("non-ASCII atom accepted: %q %q", lines, tagged)
				}
				if len(box.fetches) != 0 || len(box.updates) != 0 {
					t.Fatalf("invalid syntax reached message data: %v %v", box.fetches, box.updates)
				}
				requireSearch(t, c, "SEARCH ALL", "* SEARCH 1")
			})
		}
	}
}

// TestCoreCharsetValuesStayASCII never treats a Unicode look-alike as a
// supported IANA charset. An unsupported literal may contain response-looking
// text; the failure must still be one static line followed by a usable session.
//
// checklist: IMAP-SEARCH-29
func TestCoreCharsetValuesStayASCII(t *testing.T) {
	for _, name := range []string{"uſ-aſcii", "UTF-8\r\n* BYE injected", "not-a-charset", "\xff"} {
		for _, sync := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/sync=%t", name, sync), func(t *testing.T) {
				c, box := coreTextClient(t, false)
				prefix := fmt.Sprintf("X1 SEARCH CHARSET {%d+}\r\n", len(name))
				if sync {
					prefix = fmt.Sprintf("X1 SEARCH CHARSET {%d}\r\n", len(name))
					writeLiteralWire(t, c, prefix)
					if got := c.line(); !strings.HasPrefix(got, "+ ") {
						t.Fatalf("literal continuation: %q", got)
					}
					prefix = ""
				}
				writeLiteralWire(t, c, prefix+name+" ALL\r\nN1 NOOP\r\n")
				const want = "X1 NO [BADCHARSET (US-ASCII UTF-8)] unsupported charset"
				if got := c.line(); got != want {
					t.Fatalf("charset refusal: %q, want %q", got, want)
				}
				requireOK(t, c.line(), "N1")
				if len(box.fetches) != 0 || len(box.updates) != 0 {
					t.Fatalf("charset refusal read or changed messages: %v %v", box.fetches, box.updates)
				}
			})
		}
	}
}

// TestCoreQuotedAndLiteralValuesRemainText ensures rejecting non-ASCII atoms
// does not reject legal UTF-8 quoted values, escaped quotes, or either literal
// form. Quotes and command-looking text inside a value never become keywords.
//
// checklist: IMAP-SYNTAX-10
func TestCoreQuotedAndLiteralValuesRemainText(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		c, _ := coreTextClient(t, enabled)
		for _, query := range []string{`SEARCH SUBJECT "Grüße"`, `SEARCH OR SUBJECT "no\"match" SUBJECT "Grüße"`} {
			requireSearch(t, c, query, "* SEARCH 1")
		}
		if !enabled {
			for _, charset := range []string{"US-ASCII", `"us-ascii"`, "utf-8", `"UtF-8"`} {
				requireSearch(t, c, "SEARCH CHARSET "+charset+` SUBJECT "Grüße"`, "* SEARCH 1")
			}
			lines, tagged := c.command("Q1", `SEARCH CHARSET "uſ-aſcii" ALL`)
			requireExactly(t, lines)
			if tagged != "Q1 NO [BADCHARSET (US-ASCII UTF-8)] unsupported charset" {
				t.Fatalf("quoted charset alias: %q", tagged)
			}
		}
		for _, sync := range []bool{false, true} {
			value := "Grüße"
			prefix := fmt.Sprintf("X1 SEARCH SUBJECT {%d+}\r\n", len(value))
			if sync {
				prefix = fmt.Sprintf("X1 SEARCH SUBJECT {%d}\r\n", len(value))
				writeLiteralWire(t, c, prefix)
				if got := c.line(); !strings.HasPrefix(got, "+ ") {
					t.Fatalf("literal continuation: %q", got)
				}
				prefix = ""
			}
			writeLiteralWire(t, c, prefix+value+"\r\n")
			if got := c.line(); got != "* SEARCH 1" {
				t.Fatalf("literal search: %q", got)
			}
			requireOK(t, c.line(), "X1")
		}
	}
}

// TestCoreSearchRefusalsKeepOneLine gives date and keyword positions literal
// data that resembles server replies. Those positions take no literal, so it is
// refused before its octets are read: one BAD line, then the server's own BYE,
// because an unread non-synchronizing literal leaves the stream unusable. The
// forged lines never become responses, the pipelined NOOP never runs, and no
// message is read or changed.
//
// checklist: IMAP-SEARCH-30
func TestCoreSearchRefusalsKeepOneLine(t *testing.T) {
	for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
		for _, query := range []string{"ON ", "SENTBEFORE ", "OR ALL "} {
			for _, value := range []string{"bad\r\n* BYE injected", "bad\nZ9 OK forged", "\xffbad"} {
				t.Run(fmt.Sprintf("%s/%s/%q", prefix, query, value), func(t *testing.T) {
					c, box := coreTextClient(t, false)
					writeLiteralWire(t, c, fmt.Sprintf("X1 %s %s{%d+}\r\n%s\r\nN1 NOOP\r\n", prefix, query, len(value), value))
					if got := c.line(); got != "X1 BAD Literal is not allowed at this argument position" {
						t.Fatalf("literal rejection: %q", got)
					}
					if got := c.line(); got != "* BYE Command literal rejected; stream cannot be reused" {
						t.Fatalf("unread literal: %q", got)
					}
					if !c.closed() {
						t.Fatal("the stream was reused after an unread literal")
					}
					if len(box.fetches) != 0 || len(box.updates) != 0 {
						t.Fatalf("invalid criterion read or changed messages: %v %v", box.fetches, box.updates)
					}
				})
			}
		}
	}
}

// TestCoreTextRejectionLiteralBoundaries refuses typed non-ASCII atoms before
// soliciting literals. An unread non-synchronizing literal closes with BYE;
// a synchronizing one leaves NOOP usable. The rule applies at the original
// command line, its continuation, and APPEND's separate literal reader.
//
// checklist: IMAP-SYNTAX-09 IMAP-SYNTAX-10
func TestCoreTextRejectionLiteralBoundaries(t *testing.T) {
	for _, sync := range []bool{false, true} {
		for _, position := range []string{"command", "args", "continuation", "append", "append continuation"} {
			t.Run(fmt.Sprintf("%s/sync=%t", position, sync), func(t *testing.T) {
				c, box := coreTextClient(t, true)
				prefix := "X1 SEARCH ſUBJECT "
				switch position {
				case "command":
					prefix = "X1 ſEARCH SUBJECT "
				case "continuation":
					prefix = "X1 SEARCH SUBJECT {1+}\r\nx ſUBJECT "
				case "append":
					prefix = "X1 APPEND Grüße "
				case "append continuation":
					prefix = "X1 APPEND {5+}\r\nINBOX ſEEN "
				}
				if sync {
					writeLiteralWire(t, c, prefix+"{1}\r\n")
				} else {
					writeLiteralWire(t, c, prefix+"{1+}\r\nx\r\nN1 NOOP\r\n")
				}
				if got := c.line(); !strings.HasPrefix(got, "X1 BAD ") {
					t.Fatalf("refusal before continuation: %q", got)
				}
				if sync {
					_, tagged := c.command("N1", "NOOP")
					requireOK(t, tagged, "N1")
				} else {
					if got := c.line(); !strings.HasPrefix(got, "* BYE ") {
						t.Fatalf("pending literal: %q, want BYE", got)
					}
					if !c.closed() {
						t.Fatal("unread literal did not close the connection")
					}
				}
				if len(box.fetches) != 0 || len(box.updates) != 0 {
					t.Fatalf("refused text read or changed messages: %v %v", box.fetches, box.updates)
				}
			})
		}
	}
}
