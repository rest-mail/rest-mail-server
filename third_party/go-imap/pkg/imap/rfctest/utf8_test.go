package rfctest

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// utf8Mailbox is an account whose backend supports UTF8=ACCEPT (RFC 6855),
// recording appends like appendMailbox.
type utf8Mailbox struct {
	appendMailbox
	enables int
}

func (m *utf8Mailbox) EnableUTF8() error { m.enables++; return nil }

func utf8Client(t *testing.T) (*client, *utf8Mailbox) {
	t.Helper()
	box := &utf8Mailbox{}
	return selectionClient(t, box), box
}

func enableUTF8(t *testing.T, c *client) {
	t.Helper()
	lines, st := c.command("E1", "ENABLE UTF8=ACCEPT")
	requireOK(t, st, "E1")
	requireExactly(t, lines, "* ENABLED UTF8=ACCEPT")
}

// sendLiteral writes a command line ending in a synchronizing literal, waits
// for the continuation, then writes the octets and the rest of the line.
func sendLiteral(t *testing.T, c *client, line, octets, after string) {
	t.Helper()
	if _, err := fmt.Fprintf(c.conn, "%s{%d}\r\n", line, len(octets)); err != nil {
		t.Fatal(err)
	}
	if l := c.line(); !strings.HasPrefix(l, "+ ") {
		t.Fatalf("continuation for %q: %q", line, l)
	}
	if _, err := io.WriteString(c.conn, octets+after+"\r\n"); err != nil {
		t.Fatal(err)
	}
}

// RFC 6855 §3: a UTF8=ACCEPT server takes UTF-8 in quoted-strings and answers
// BAD to any 8-bit octet sequence that is not UTF-8, whether or not UTF-8 has
// been enabled. A synchronizing literal is refused before its continuation;
// literal octets themselves are not held to UTF-8. A backend without UTF8=ACCEPT
// requires literal framing for every 8-bit argument.
//
// checklist: IMAP-UTF8-01
func TestUTF8QuotedStrings(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			c, _ := utf8Client(t)
			if enabled {
				enableUTF8(t, c)
			}
			for _, name := range []string{"\xff", "Gr\xfc\xdfe", "\xc3", "\xed\xa0\x80", "\xc0\xaf"} {
				_, st := c.command("q1", "SUBSCRIBE \"%s\"", name)
				if !strings.HasPrefix(st, "q1 BAD ") {
					t.Fatalf("SUBSCRIBE with %q: %q, want BAD", name, st)
				}
			}
			// Well-formed UTF-8 is a string like any other: no such mailbox.
			_, st := c.command("q2", "SUBSCRIBE \"Grüße\"")
			if strings.HasPrefix(st, "q2 BAD ") {
				t.Fatalf("UTF-8 quoted mailbox: %q", st)
			}
			// Refused before the continuation: no octets were invited.
			if _, err := io.WriteString(c.conn, "q3 LIST \"\xff\" {1}\r\n"); err != nil {
				t.Fatal(err)
			}
			if l := c.line(); !strings.HasPrefix(l, "q3 BAD ") {
				t.Fatalf("invalid line before a literal: %q", l)
			}
			// A literal's own octets are exempt.
			sendLiteral(t, c, "q4 SUBSCRIBE ", "\xff\xfe", "")
			if l := c.line(); strings.HasPrefix(l, "q4 BAD ") {
				t.Fatalf("8-bit literal: %q", l)
			}
			requireOK(t, func() string { _, st := c.command("n1", "NOOP"); return st }(), "n1")
		})
	}

	base := selectionClient(t, &appendMailbox{})
	if _, st := base.command("b1", "SUBSCRIBE \"\xff\""); !strings.HasPrefix(st, "b1 BAD ") {
		t.Fatalf("base backend SUBSCRIBE: %q, want BAD without UTF8=ACCEPT", st)
	}
}

// RFC 6855 §3: mailbox names are Net-Unicode without C0 or C1 controls,
// DELETE, LINE SEPARATOR or PARAGRAPH SEPARATOR. CREATE and RENAME refuse such
// a name with NO, even when it arrives as a literal, and take any other UTF-8.
//
// checklist: IMAP-UTF8-02
func TestUTF8MailboxNames(t *testing.T) {
	c, _ := utf8Client(t)
	for _, name := range []string{"Grüße", "日本語", "Ärger/Post"} {
		_, st := c.command("c1", "CREATE \"%s\"", name)
		requireOK(t, st, "c1")
	}
	_, st := c.command("r1", "RENAME Work \"Arbeit für später\"")
	requireOK(t, st, "r1")

	for _, name := range []string{"a\x7fb", "a\u0085b", "a b", "a b", "a\u009fb"} {
		_, st := c.command("c2", "CREATE \"%s\"", name)
		if !strings.HasPrefix(st, "c2 NO ") {
			t.Fatalf("CREATE %q: %q, want NO", name, st)
		}
		_, st = c.command("r2", "RENAME Work \"%s\"", name)
		if !strings.HasPrefix(st, "r2 NO ") {
			t.Fatalf("RENAME to %q: %q, want NO", name, st)
		}
	}
	// Controls and malformed UTF-8 can only arrive in a literal.
	for _, name := range []string{"a\x01b", "a\tb", "\xff"} {
		sendLiteral(t, c, "c3 CREATE ", name, "")
		if l := c.line(); !strings.HasPrefix(l, "c3 NO ") {
			t.Fatalf("CREATE literal %q: %q, want NO", name, l)
		}
	}
}

// RFC 6855 §3: after ENABLE UTF8=ACCEPT a client must not label SEARCH with a
// CHARSET, and a server should answer such a command BAD. Before ENABLE the
// label is accepted as it always was.
//
// checklist: IMAP-UTF8-03
func TestUTF8SearchCharset(t *testing.T) {
	c, _ := utf8Client(t)
	if _, st := c.command("s0", "SELECT INBOX"); !strings.HasPrefix(st, "s0 OK") {
		t.Fatalf("SELECT: %q", st)
	}
	// ENABLE is for the authenticated state; select again afterwards.
	if _, st := c.command("s1", "SEARCH CHARSET UTF-8 ALL"); !strings.HasPrefix(st, "s1 OK") {
		t.Fatalf("SEARCH CHARSET before ENABLE: %q", st)
	}
	if _, st := c.command("u0", "UNSELECT"); !strings.HasPrefix(st, "u0 OK") {
		t.Fatalf("UNSELECT: %q", st)
	}
	enableUTF8(t, c)
	if _, st := c.command("s2", "SELECT INBOX"); !strings.HasPrefix(st, "s2 OK") {
		t.Fatalf("SELECT: %q", st)
	}
	for _, cmd := range []string{"SEARCH CHARSET UTF-8 ALL", "UID SEARCH charset US-ASCII ALL"} {
		if _, st := c.command("s3", "%s", cmd); !strings.HasPrefix(st, "s3 BAD ") {
			t.Fatalf("%s after ENABLE: %q, want BAD", cmd, st)
		}
	}
	if _, st := c.command("s4", "SEARCH SUBJECT \"Grüße\""); !strings.HasPrefix(st, "s4 OK") {
		t.Fatalf("UTF-8 SEARCH without CHARSET: %q", st)
	}
}

// RFC 6855 §4 with RFC 4466 §2.2: a UTF8=ACCEPT server takes the message of
// APPEND as "UTF8 (" literal8 ")", synchronizing or not, into a mailbox named
// by a UTF-8 quoted-string. A server without UTF8=ACCEPT has no such data item.
//
// checklist: IMAP-UTF8-04
func TestUTF8AppendDataItem(t *testing.T) {
	raw := "Subject: Grüße\r\nFrom: jörg@example.test\r\n\r\nHallo\r\n"
	for _, nonSync := range []bool{false, true} {
		t.Run(fmt.Sprintf("nonSync=%t", nonSync), func(t *testing.T) {
			c, box := utf8Client(t)
			enableUTF8(t, c)
			marker := fmt.Sprintf("~{%d}", len(raw))
			if nonSync {
				marker = fmt.Sprintf("~{%d+}", len(raw))
			}
			if _, err := fmt.Fprintf(c.conn, "A1 APPEND \"Entwürfe\" (\\Seen) utf8 (%s\r\n", marker); err != nil {
				t.Fatal(err)
			}
			if !nonSync {
				if l := c.line(); !strings.HasPrefix(l, "+ ") {
					t.Fatalf("continuation: %q", l)
				}
			}
			if _, err := io.WriteString(c.conn, raw+")\r\nN1 NOOP\r\n"); err != nil {
				t.Fatal(err)
			}
			requireOK(t, c.line(), "A1")
			requireOK(t, c.line(), "N1")
			if len(box.records) != 1 || box.records[0].raw != raw || box.records[0].folder != "Entwürfe" {
				t.Fatalf("appended %+v", box.records)
			}
		})
	}

	// The parenthesis must close the data item.
	c, box := utf8Client(t)
	enableUTF8(t, c)
	if _, err := io.WriteString(c.conn, "A2 APPEND INBOX UTF8 (~{2}\r\n"); err != nil {
		t.Fatal(err)
	}
	if l := c.line(); !strings.HasPrefix(l, "+ ") {
		t.Fatalf("continuation: %q", l)
	}
	// The next command's first octet stands where ")" belongs.
	if _, err := io.WriteString(c.conn, "hi\r\nN1 NOOP\r\n"); err != nil {
		t.Fatal(err)
	}
	if l := c.line(); !strings.HasPrefix(l, "A2 BAD ") {
		t.Fatalf("unclosed UTF8 data item: %q", l)
	}
	if len(box.records) != 0 {
		t.Fatalf("unclosed data item reached backend: %+v", box.records)
	}

	base := &appendMailbox{}
	b := selectionClient(t, base)
	if _, err := io.WriteString(b.conn, "B1 APPEND INBOX UTF8 (~{2}\r\n"); err != nil {
		t.Fatal(err)
	}
	if l := b.line(); !strings.HasPrefix(l, "B1 BAD ") {
		t.Fatalf("base backend UTF8 data item: %q", l)
	}
	if len(base.records) != 0 {
		t.Fatalf("base backend appended %+v", base.records)
	}
}

// RFC 6855 §4: until the client has enabled UTF-8, a UTF8=ACCEPT server answers
// NO to an APPEND whose message header fields carry an 8-bit octet. 8-bit text
// in the body alone is not refused, and after ENABLE the same message is taken.
//
// checklist: IMAP-UTF8-05
func TestUTF8AppendBeforeEnable(t *testing.T) {
	header8 := "Subject: Grüße\r\n\r\nhi\r\n"
	body8 := "Subject: hello\r\n\r\nGrüße\r\n"

	c, box := utf8Client(t)
	sendLiteral(t, c, "A1 APPEND INBOX ", header8, "")
	if l := c.line(); !strings.HasPrefix(l, "A1 NO ") {
		t.Fatalf("8-bit header before ENABLE: %q", l)
	}
	sendLiteral(t, c, "A2 APPEND INBOX UTF8 (~", header8, ")")
	if l := c.line(); !strings.HasPrefix(l, "A2 NO ") {
		t.Fatalf("8-bit header in UTF8 data before ENABLE: %q", l)
	}
	if len(box.records) != 0 {
		t.Fatalf("refused appends reached backend: %+v", box.records)
	}
	sendLiteral(t, c, "A3 APPEND INBOX ", body8, "")
	requireOK(t, c.line(), "A3")

	enableUTF8(t, c)
	sendLiteral(t, c, "A4 APPEND INBOX ", header8, "")
	requireOK(t, c.line(), "A4")
	if len(box.records) != 2 || box.records[1].raw != header8 {
		t.Fatalf("appended %+v", box.records)
	}
}
