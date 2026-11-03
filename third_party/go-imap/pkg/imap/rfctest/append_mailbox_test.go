package rfctest

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// TestAppendUnquotedMailboxGrammar enumerates the RFC 3501 §9 ASTRING-CHAR
// octet grammar through Session. CR/LF are command framing, so they are tested
// by the existing command-line tests rather than inserted in a mailbox token.
// ASTRING-CHAR adds ']' back to ATOM-CHAR; '}' and '+' are also legal. A lone
// '&' is an ASTRING-CHAR but not modified UTF-7 (§5.1.3), so "a&b" names no
// mailbox: NO before the continuation rather than BAD.
//
// checklist: IMAP-APPEND-30
func TestAppendUnquotedMailboxGrammar(t *testing.T) {
	for octet := 0; octet <= 255; octet++ {
		if octet == '\r' || octet == '\n' {
			continue
		}
		// Independent expansion of CHAR minus atom-specials, plus ']'.
		valid := octet >= 0x21 && octet <= 0x7e
		switch octet {
		case '(', ')', '{', '%', '*', '"', '\\':
			valid = false
		}
		for _, flags := range []string{"", ` (\Seen)`} {
			for _, nonSync := range []bool{false, true} {
				t.Run(fmt.Sprintf("byte-%02x/flags=%t/nonSync=%t", octet, flags != "", nonSync), func(t *testing.T) {
					folder := "a" + string([]byte{byte(octet)}) + "b"
					box := &appendMailbox{}
					c := selectionClient(t, box)
					marker := "{3}"
					if nonSync {
						marker = "{3+}"
					}
					if _, err := fmt.Fprintf(c.conn, "A1 APPEND %s%s %s\r\n", folder, flags, marker); err != nil {
						t.Fatal(err)
					}
					if !valid || octet == '&' {
						want := "A1 BAD "
						if valid {
							want = "A1 NO "
						}
						if line := c.line(); !strings.HasPrefix(line, want) {
							t.Fatalf("invalid mailbox: %q, want %q before continuation", line, want)
						}
						if nonSync {
							if line := c.line(); !strings.HasPrefix(line, "* BYE ") {
								t.Fatalf("unread literal: %q, want BYE", line)
							}
							if !c.closed() {
								t.Fatal("unread literal left connection open")
							}
							<-c.done
						} else {
							_, tagged := c.command("N1", "NOOP")
							requireOK(t, tagged, "N1")
						}
						if len(box.records) != 0 {
							t.Fatalf("invalid mailbox stored: %+v", box.records)
						}
						return
					}
					if !nonSync {
						if line := c.line(); !strings.HasPrefix(line, "+ ") {
							t.Fatalf("continuation: %q", line)
						}
					}
					if _, err := io.WriteString(c.conn, "abc\r\n"); err != nil {
						t.Fatal(err)
					}
					lines, tagged := appendCompletion(t, c)
					requireOK(t, tagged, "A1")
					if len(lines) != 0 || len(box.records) != 1 || box.records[0].folder != folder || box.records[0].raw != "abc" {
						t.Fatalf("append: replies=%q records=%+v, want folder %q", lines, box.records, folder)
					}
					seen := box.records[0].flags.Seen
					if (flags != "") != (seen != nil && *seen) {
						t.Fatalf("flags %q not applied: %+v", flags, box.records[0].flags)
					}
					_, tagged = c.command("N1", "NOOP")
					requireOK(t, tagged, "N1")
				})
			}
		}
	}
}
