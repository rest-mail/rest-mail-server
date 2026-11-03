package rfctest

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/server"
)

type appendRecord struct {
	folder string
	flags  imap.FlagUpdate
	raw    string
	date   time.Time
}

// RFC 3501 §9 defines a message literal as CHAR8 octets, which exclude NUL.
// The complete command has been consumed when validation runs, so a rejected
// message must leave the next command framed correctly.
//
// checklist: IMAP-APPEND-27
// checklist: IMAP2-SEC-219
func TestAppendRejectsNULMessageOctet(t *testing.T) {
	for _, raw := range []string{"\x00abc", "ab\x00cd", "abc\x00"} {
		for _, nonSync := range []bool{false, true} {
			t.Run(fmt.Sprintf("nul=%d/nonSync=%t", strings.IndexByte(raw, 0), nonSync), func(t *testing.T) {
				box := &appendMailbox{}
				c := selectionClient(t, box)
				marker := fmt.Sprintf("{%d}", len(raw))
				if nonSync {
					marker = fmt.Sprintf("{%d+}", len(raw))
				}
				if _, err := fmt.Fprintf(c.conn, "A1 APPEND INBOX %s\r\n", marker); err != nil {
					t.Fatal(err)
				}
				if !nonSync {
					if line := c.line(); !strings.HasPrefix(line, "+ ") {
						t.Fatalf("continuation: %q", line)
					}
				}
				if _, err := io.WriteString(c.conn, raw+"\r\nN1 NOOP\r\n"); err != nil {
					t.Fatal(err)
				}
				if line := c.line(); !strings.HasPrefix(line, "A1 BAD ") {
					t.Fatalf("NUL append: %q", line)
				}
				requireOK(t, c.line(), "N1")
				if len(box.records) != 0 {
					t.Fatalf("NUL reached backend: %+v", box.records)
				}
			})
		}
	}
}

// RFC 3501 §9 quoted-specials permits escaping only DQUOTE and backslash.
// A malformed mailbox is refused before the message-literal continuation.
//
// checklist: IMAP-APPEND-28
func TestAppendQuotedMailboxGrammar(t *testing.T) {
	for _, tc := range []struct{ wire, folder string }{
		{`"with space"`, "with space"},
		{`"a\"b"`, `a"b`},
		{`"a\\b"`, `a\b`},
	} {
		t.Run(tc.wire, func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			_, tagged := appendCommand(t, c, tc.wire, "msg")
			requireOK(t, tagged, "A1")
			if len(box.records) != 1 || box.records[0].folder != tc.folder {
				t.Fatalf("folder: %+v, want %q", box.records, tc.folder)
			}
		})
	}
	for _, wire := range []string{`"a\qb"`, `"a\ b"`, `"a\`, `"a"b"`, "\"a\x00b\"", "\"a\x80b\""} {
		t.Run(fmt.Sprintf("invalid/%q", wire), func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			_, tagged := c.command("A1", "APPEND %s {3}", wire)
			if !strings.HasPrefix(tagged, "A1 BAD ") {
				t.Fatalf("invalid mailbox: %q", tagged)
			}
			if len(box.records) != 0 {
				t.Fatalf("invalid mailbox stored: %+v", box.records)
			}
			_, tagged = c.command("N1", "NOOP")
			requireOK(t, tagged, "N1")
		})
	}
	t.Run("non-synchronizing refusal", func(t *testing.T) {
		box := &appendMailbox{}
		c := selectionClient(t, box)
		if _, err := io.WriteString(c.conn, "A1 APPEND \"a\\qb\" {3+}\r\nabc\r\n"); err != nil {
			t.Fatal(err)
		}
		if line := c.line(); !strings.HasPrefix(line, "A1 BAD ") {
			t.Fatalf("invalid mailbox: %q", line)
		}
		if line := c.line(); !strings.HasPrefix(line, "* BYE ") {
			t.Fatalf("unread literal refusal: %q", line)
		}
		if len(box.records) != 0 {
			t.Fatalf("invalid mailbox stored: %+v", box.records)
		}
	})
}

type appendMailbox struct {
	mailbox
	records   []appendRecord
	messages  []imap.Message
	fail      bool
	dateCalls int
}

func (m *appendMailbox) Messages(string) ([]imap.Message, error) {
	return slices.Clone(m.messages), nil
}

func (m *appendMailbox) Append(folder string, flags imap.FlagUpdate, raw []byte) error {
	if m.fail {
		return errors.New("private append failure")
	}
	m.records = append(m.records, appendRecord{folder: folder, flags: flags, raw: string(raw)})
	m.messages = append(m.messages, imap.Message{UID: uint32(10 + len(m.messages)), Size: len(raw)})
	return nil
}

type datedAppendMailbox struct{ *appendMailbox }

func (m *datedAppendMailbox) AppendWithDate(folder string, flags imap.FlagUpdate, date time.Time, raw []byte) (uint32, error) {
	m.dateCalls++
	if err := m.Append(folder, flags, raw); err != nil {
		return 0, err
	}
	m.records[len(m.records)-1].date = date
	return m.messages[len(m.messages)-1].UID, nil
}

// appendCommand waits for the synchronizing continuation, then writes the exact
// literal followed by the command's own CRLF (not part of the message).
func appendCommand(t *testing.T, c *client, args, raw string) ([]string, string) {
	t.Helper()
	if _, err := fmt.Fprintf(c.conn, "A1 APPEND %s {%d}\r\n", args, len(raw)); err != nil {
		t.Fatal(err)
	}
	if line := c.line(); !strings.HasPrefix(line, "+ ") {
		t.Fatalf("continuation: %q", line)
	}
	if _, err := io.WriteString(c.conn, raw+"\r\n"); err != nil {
		t.Fatal(err)
	}
	return appendCompletion(t, c)
}

func appendCompletion(t *testing.T, c *client) ([]string, string) {
	t.Helper()
	var lines []string
	for {
		line := c.line()
		if strings.HasPrefix(line, "A1 ") {
			return lines, line
		}
		if !strings.HasPrefix(line, "* ") {
			t.Fatalf("unexpected APPEND response: %q", line)
		}
		lines = append(lines, line)
	}
}

// TestAppendExample uses RFC 3501 §6.3.11's A003 message and envelope. The RFC's
// final blank client line supplies the command terminator after 310 octets.
//
// checklist: IMAP-APPEND-01 IMAP-APPEND-02 IMAP-RESP-17
func TestAppendExample(t *testing.T) {
	raw := "Date: Mon, 7 Feb 1994 21:52:25 -0800 (PST)\r\n" +
		"From: Fred Foobar <foobar@Blurdybloop.COM>\r\n" +
		"Subject: afternoon meeting\r\n" +
		"To: mooch@owatagu.siam.edu\r\n" +
		"Message-Id: <B27397-0100000@Blurdybloop.COM>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: TEXT/PLAIN; CHARSET=US-ASCII\r\n\r\n" +
		"Hello Joe, do you think we can meet at 3:30 tomorrow?\r\n"
	if len(raw) != 310 {
		t.Fatalf("RFC fixture length: %d", len(raw))
	}
	box := &appendMailbox{}
	c := selectionClient(t, box)
	// Use the RFC's A003 command tag and 310-octet literal marker.
	if _, err := io.WriteString(c.conn, "A003 APPEND saved-messages (\\Seen) {310}\r\n"); err != nil {
		t.Fatal(err)
	}
	if line := c.line(); !strings.HasPrefix(line, "+ ") {
		t.Fatalf("continuation: %q", line)
	}
	if _, err := io.WriteString(c.conn, raw+"\r\n"); err != nil {
		t.Fatal(err)
	}
	requireOK(t, c.line(), "A003")
	if len(box.records) != 1 {
		t.Fatalf("records: %+v", box.records)
	}
	got := box.records[0]
	if got.folder != "saved-messages" || got.raw != raw || got.flags.Seen == nil || !*got.flags.Seen {
		t.Fatalf("append: %+v", got)
	}
}

// checklist: IMAP-APPEND-03 IMAP-APPEND-04 IMAP-APPEND-31
func TestAppendLiteralOctets(t *testing.T) {
	for _, raw := range []string{"", "Subject: draft\r\n\r\n", "Subject: 8bit\r\n\r\ncaf\xc3\xa9\xff\r\n.hello\r\nZ1 LOGOUT\r\n", "no final newline"} {
		t.Run(fmt.Sprintf("%d", len(raw)), func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			_, tagged := appendCommand(t, c, `"My (Saved) Folder"`, raw)
			requireOK(t, tagged, "A1")
			if len(box.records) != 1 || box.records[0].folder != "My (Saved) Folder" || box.records[0].raw != raw {
				t.Fatalf("records: %+v", box.records)
			}
			f := box.records[0].flags
			if f.Seen != nil || f.Answered != nil || f.Flagged != nil || f.Draft != nil {
				t.Fatalf("default flags: %+v", f)
			}
			_, tagged = c.command("N1", "NOOP")
			requireOK(t, tagged, "N1")
		})
	}
}

// checklist: IMAP-APPEND-05 IMAP-APPEND-06
func TestAppendFlagsAndDate(t *testing.T) {
	box := &datedAppendMailbox{&appendMailbox{}}
	c := selectionClient(t, box)
	_, tagged := appendCommand(t, c, `inbox (\Seen \Answered \Flagged \Draft) " 7-Feb-1994 21:52:25 -0800"`, "draft")
	requireOK(t, tagged, "A1")
	if len(box.records) != 1 {
		t.Fatalf("records: %+v", box.records)
	}
	got := box.records[0]
	for name, value := range map[string]*bool{"Seen": got.flags.Seen, "Answered": got.flags.Answered, "Flagged": got.flags.Flagged, "Draft": got.flags.Draft} {
		if value == nil || !*value {
			t.Errorf("%s: %v", name, value)
		}
	}
	want := time.Date(1994, 2, 7, 21, 52, 25, 0, time.FixedZone("", -8*3600))
	_, offset := got.date.Zone()
	if got.folder != "INBOX" || !got.date.Equal(want) || offset != -8*3600 {
		t.Fatalf("append: %+v", got)
	}
}

// checklist: IMAP-APPEND-07 IMAP-APPEND-08
// checklist: IMAP2-CMD-131
func TestAppendSelectedMailbox(t *testing.T) {
	for _, mode := range []string{"SELECT", "EXAMINE"} {
		for _, dest := range []string{"INBOX", "Archive"} {
			t.Run(mode+"/"+dest, func(t *testing.T) {
				box := &appendMailbox{}
				c := selectionClient(t, box)
				_, tagged := c.command("S1", "%s INBOX", mode)
				requireOK(t, tagged, "S1")
				lines, tagged := appendCommand(t, c, dest, "new")
				requireOK(t, tagged, "A1")
				if dest == "INBOX" {
					requireLine(t, lines, "* 1 EXISTS")
					// New sequence numbers must resolve immediately after the notification.
					lines, tagged = c.command("F1", "FETCH 1 UID")
					requireOK(t, tagged, "F1")
					requireLine(t, lines, "* 1 FETCH (UID 10 FLAGS ())")
				} else if len(lines) != 0 {
					t.Fatalf("other mailbox notifications: %q", lines)
				}
			})
		}
	}
}

// checklist: IMAP-APPEND-09
func TestAppendBackendFailure(t *testing.T) {
	box := &appendMailbox{fail: true}
	c := selectionClient(t, box)
	_, tagged := c.command("S1", "SELECT INBOX")
	requireOK(t, tagged, "S1")
	lines, tagged := appendCommand(t, c, "INBOX", "not stored")
	if !strings.HasPrefix(tagged, "A1 NO ") || len(lines) != 0 || strings.Contains(tagged, "private") {
		t.Fatalf("failed append: %q %q", lines, tagged)
	}
	if len(box.records) != 0 {
		t.Fatalf("stored after refusal: %+v", box.records)
	}
	box.fail = false
	lines, tagged = appendCommand(t, c, "INBOX", "retry")
	requireOK(t, tagged, "A1")
	requireLine(t, lines, "* 1 EXISTS")
	if len(box.records) != 1 || box.records[0].raw != "retry" {
		t.Fatalf("retry: %+v", box.records)
	}
}

// checklist: IMAP-APPEND-10
func TestAppendInvalidArguments(t *testing.T) {
	for _, args := range []string{"", "INBOX", "INBOX {+3}", "INBOX {-1}", "INBOX {3x}", "INBOX {}", "INBOX {3} garbage", "INBOX garbage {3}", "INBOX {3}{3}", "INBOX {4294967296}", "INBOX {999999999999999999999999}", "INBOX (\\Seen {3}", `INBOX "bad date" {3}`} {
		t.Run(args, func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			_, tagged := c.command("A1", "APPEND %s", args)
			if !strings.HasPrefix(tagged, "A1 BAD ") {
				t.Fatalf("invalid arguments: %q", tagged)
			}
			if len(box.records) != 0 {
				t.Fatalf("invalid append stored: %+v", box.records)
			}
			_, tagged = c.command("N1", "NOOP")
			requireOK(t, tagged, "N1")
		})
	}
}

// checklist: IMAP-APPEND-11
func TestAppendRefusedBeforeContinuation(t *testing.T) {
	for _, args := range []string{`INBOX (\Bogus) {3}`, "INBOX {10485761}"} {
		t.Run(args, func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			_, tagged := c.command("A1", "APPEND %s", args)
			if !strings.HasPrefix(tagged, "A1 NO ") {
				t.Fatalf("refused append: %q", tagged)
			}
			if len(box.records) != 0 {
				t.Fatal("refused append stored")
			}
			_, tagged = c.command("N1", "NOOP")
			requireOK(t, tagged, "N1")
		})
	}
}

// checklist: IMAP-APPEND-12
func TestAppendNonSynchronizing(t *testing.T) {
	box := &appendMailbox{}
	c := selectionClient(t, box)
	done := make(chan error, 1)
	go func() { _, err := io.WriteString(c.conn, "A1 APPEND INBOX {3+}\r\nabc\r\nN1 NOOP\r\n"); done <- err }()
	t.Cleanup(func() { _ = c.conn.Close(); <-done })
	_, tagged := appendCompletion(t, c)
	requireOK(t, tagged, "A1")
	requireOK(t, c.line(), "N1")
	if len(box.records) != 1 || box.records[0].raw != "abc" {
		t.Fatalf("append: %+v", box.records)
	}
}

// checklist: IMAP-APPEND-13
func TestAppendMalformedTerminator(t *testing.T) {
	for _, tail := range []string{"xx", "\nX", " \r", "\rX"} {
		t.Run(fmt.Sprintf("%q", tail), func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			if _, err := io.WriteString(c.conn, "A1 APPEND INBOX {3}\r\n"); err != nil {
				t.Fatal(err)
			}
			if line := c.line(); !strings.HasPrefix(line, "+ ") {
				t.Fatalf("continuation: %q", line)
			}
			if _, err := io.WriteString(c.conn, "abc"+tail); err != nil {
				t.Fatal(err)
			}
			line := c.line()
			if !strings.HasPrefix(line, "A1 BAD ") {
				t.Fatalf("invalid terminator: %q", line)
			}
			if !c.closed() {
				t.Fatal("invalid framing left connection open")
			}
			if len(box.records) != 0 {
				t.Fatalf("invalid append stored: %+v", box.records)
			}
		})
	}
}

// checklist: IMAP-APPEND-14
func TestAppendDisconnectBeforeCompletion(t *testing.T) {
	for _, body := range []string{"a", "abc", "abc\r"} {
		t.Run(fmt.Sprintf("%q", body), func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			if _, err := io.WriteString(c.conn, "A1 APPEND INBOX {3}\r\n"); err != nil {
				t.Fatal(err)
			}
			if line := c.line(); !strings.HasPrefix(line, "+ ") {
				t.Fatalf("continuation: %q", line)
			}
			if _, err := io.WriteString(c.conn, body); err != nil {
				t.Fatal(err)
			}
			_ = c.conn.Close()
			<-c.done
			if len(box.records) != 0 {
				t.Fatalf("incomplete command stored: %+v", box.records)
			}
		})
	}
}

// checklist: IMAP-APPEND-15
func TestAppendFlagsCaseInsensitive(t *testing.T) {
	box := &appendMailbox{}
	c := selectionClient(t, box)
	_, tagged := appendCommand(t, c, `INBOX (\sEeN \aNsWeReD \fLaGgEd \dRaFt)`, "draft")
	requireOK(t, tagged, "A1")
	if len(box.records) != 1 {
		t.Fatalf("records: %+v", box.records)
	}
	f := box.records[0].flags
	for name, value := range map[string]*bool{"Seen": f.Seen, "Answered": f.Answered, "Flagged": f.Flagged, "Draft": f.Draft} {
		if value == nil || !*value {
			t.Errorf("%s: %v", name, value)
		}
	}
}

// checklist: IMAP-APPEND-16
func TestAppendRequiresAuthentication(t *testing.T) {
	box := &appendMailbox{}
	c := dialMailbox(t, box)
	c.line()
	_, tagged := c.command("A1", "APPEND INBOX {3}")
	if !strings.HasPrefix(tagged, "A1 NO ") {
		t.Fatalf("unauthenticated APPEND: %q", tagged)
	}
	if len(box.records) != 0 {
		t.Fatal("unauthenticated APPEND stored")
	}
	c.login()
	_, tagged = appendCommand(t, c, "INBOX", "abc")
	requireOK(t, tagged, "A1")
	if len(box.records) != 1 {
		t.Fatalf("records after login: %+v", box.records)
	}
}

// TestAppendRejectedNonSynchronizing proves that early refusal never promotes
// an in-flight message (or a later command) into a new command. Header-only
// cases also prove that refusal does not wait to drain an attacker-sized body.
//
// checklist: IMAP-APPEND-22 IMAP-APPEND-24 IMAP-APPEND-26 IMAP-APPEND-30 IMAP-LITMINUS-04
func TestAppendRejectedNonSynchronizing(t *testing.T) {
	for _, tc := range []struct {
		name, args, status string
		unauthenticated    bool
	}{
		{"authentication", "INBOX", "NO", true},
		{"mailbox wildcard", "a%b", "BAD", false},
		{"mailbox control", "a\x01b", "BAD", false},
		{"mailbox high octet", "a\x80b", "BAD", false},
		{"malformed flags", `INBOX (\Seen`, "BAD", false},
		{"flag separators", `INBOX (\Seen  \Draft)`, "BAD", false},
		{"argument separators", "INBOX ", "BAD", false},
		{"unsupported flags", `INBOX (\Bogus)`, "NO", false},
		{"invalid date", `INBOX "bad date"`, "BAD", false},
		{"unterminated date", `INBOX "bad date`, "BAD", false},
		{"extra argument", "INBOX garbage", "BAD", false},
		// Over LITERAL-'s 4096 octets, a non-synchronizing literal is BAD
		// with TOOBIG (RFC 7888 §5), before its size is weighed.
		{"oversized", "INBOX", "BAD", false},
		{"maximum size", "INBOX", "BAD", false},
	} {
		for _, pipelined := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pipelined=%t", tc.name, pipelined), func(t *testing.T) {
				box := &appendMailbox{}
				c := dialMailbox(t, box)
				c.line()
				wantAuth := 0
				if !tc.unauthenticated {
					c.login()
					wantAuth = 1
				}
				// If interpreted as commands, these bytes would authenticate and
				// append a second message. None may run, including from bufio's
				// read-ahead buffer after the underlying connection is closed.
				payload := "X1 LOGIN " + testUser + " " + testPass + "\r\n" +
					"X2 APPEND INBOX {3+}\r\nabc\r\n"
				size := uint64(len(payload))
				switch tc.name {
				case "oversized":
					size = 10485761
				case "maximum size":
					size = 4294967295
				}
				wire := fmt.Sprintf("A1 APPEND %s {%d+}\r\n", tc.args, size)
				if pipelined {
					wire += payload + "\r\nN1 NOOP\r\n"
				}
				written := make(chan error, 1)
				go func() { _, err := io.WriteString(c.conn, wire); written <- err }()
				t.Cleanup(func() { _ = c.conn.Close(); <-written })
				if line := c.line(); !strings.HasPrefix(line, "A1 "+tc.status+" ") {
					t.Fatalf("refusal: %q", line)
				}
				if line := c.line(); !strings.HasPrefix(line, "* BYE ") || !strings.Contains(line, "APPEND") && !strings.HasPrefix(line, "* BYE [TOOBIG] ") {
					t.Fatalf("closing explanation: %q", line)
				}
				if !c.closed() {
					t.Fatal("refused literal left connection open or dispatched more commands")
				}
				<-c.done
				if len(box.records) != 0 || c.backend.calls != wantAuth {
					t.Fatalf("payload executed: records=%+v auth=%d (want %d)", box.records, c.backend.calls, wantAuth)
				}
			})
		}
	}
}

// Backend refusal occurs after the complete literal was consumed: this stream
// remains usable, unlike an early refusal with an unread in-flight payload.
//
// checklist: IMAP-APPEND-09 IMAP-APPEND-12 IMAP-APPEND-22
func TestAppendNonSynchronizingBackendFailure(t *testing.T) {
	box := &appendMailbox{fail: true}
	c := selectionClient(t, box)
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(c.conn, "A1 APPEND INBOX {3+}\r\nabc\r\nN1 NOOP\r\n")
		written <- err
	}()
	t.Cleanup(func() { _ = c.conn.Close(); <-written })
	lines, tagged := appendCompletion(t, c)
	if !strings.HasPrefix(tagged, "A1 NO ") || len(lines) != 0 {
		t.Fatalf("backend refusal: %q %q", lines, tagged)
	}
	requireOK(t, c.line(), "N1")
	_, tagged = c.command("N2", "NOOP")
	requireOK(t, tagged, "N2")
	if len(box.records) != 0 {
		t.Fatalf("failed append stored: %+v", box.records)
	}
}

// A mailbox name may be sent as a literal astring before the message literal
// (RFC 3501 §§4.3, 6.3.11, 9), synchronizing or LITERAL+.
//
// checklist: IMAP-APPEND-21
func TestAppendLiteralMailbox(t *testing.T) {
	for _, tc := range []struct{ name, folder string }{
		{"Drafts", "Drafts"},
		{"inbox", "INBOX"},
		{"with space", "with space"},
		{`a"b\c`, `a"b\c`},
		{"{3}", "{3}"},
	} {
		for _, nameSync := range []bool{true, false} {
			for _, flags := range []string{"", `(\Seen) `, `(\Seen) "17-Jul-1996 02:44:25 -0700" `} {
				t.Run(fmt.Sprintf("%q/sync=%t/%q", tc.name, nameSync, flags), func(t *testing.T) {
					box := &datedAppendMailbox{&appendMailbox{}}
					c := selectionClient(t, box)
					marker := fmt.Sprintf("{%d}", len(tc.name))
					if !nameSync {
						marker = fmt.Sprintf("{%d+}", len(tc.name))
					}
					if _, err := fmt.Fprintf(c.conn, "A1 APPEND %s\r\n", marker); err != nil {
						t.Fatal(err)
					}
					if nameSync {
						if line := c.line(); !strings.HasPrefix(line, "+ ") {
							t.Fatalf("mailbox continuation: %q", line)
						}
					}
					if _, err := fmt.Fprintf(c.conn, "%s %s{3}\r\n", tc.name, flags); err != nil {
						t.Fatal(err)
					}
					if line := c.line(); !strings.HasPrefix(line, "+ ") {
						t.Fatalf("message continuation: %q", line)
					}
					if _, err := io.WriteString(c.conn, "abc\r\n"); err != nil {
						t.Fatal(err)
					}
					_, tagged := appendCompletion(t, c)
					requireOK(t, tagged, "A1")
					if len(box.records) != 1 || box.records[0].folder != tc.folder || box.records[0].raw != "abc" {
						t.Fatalf("append: %+v, want folder %q", box.records, tc.folder)
					}
					if seen := box.records[0].flags.Seen; (flags != "") != (seen != nil && *seen) {
						t.Fatalf("flags %q not applied: %+v", flags, box.records[0].flags)
					}
					if strings.Contains(flags, "1996") != !box.records[0].date.IsZero() {
						t.Fatalf("date %q not applied: %v", flags, box.records[0].date)
					}
					_, tagged = c.command("N1", "NOOP")
					requireOK(t, tagged, "N1")
				})
			}
		}
	}
	for _, tc := range []struct{ desc, wire string }{
		{"oversized name", fmt.Sprintf("A1 APPEND {%d}\r\n", server.MaxCommandLineLength+1)},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			if _, err := io.WriteString(c.conn, tc.wire); err != nil {
				t.Fatal(err)
			}
			if line := c.line(); !strings.HasPrefix(line, "A1 BAD ") {
				t.Fatalf("refused before continuation: %q", line)
			}
			_, tagged := c.command("N1", "NOOP")
			requireOK(t, tagged, "N1")
		})
	}
	for _, tc := range []struct{ desc, cont, response string }{
		{"NUL in name", "a\x00b {3}", "A1 BAD "},
		{"missing separator", "abc{3}", "A1 BAD "},
		{"doubled separator", "abc  {3}", "A1 BAD "},
		{"no message literal", "abc", "A1 BAD "},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			name, rest, _ := strings.Cut(tc.cont, " ")
			if tc.desc == "missing separator" || tc.desc == "no message literal" {
				name, rest = "abc", strings.TrimPrefix(tc.cont, "abc")
			} else {
				rest = " " + rest
			}
			if _, err := fmt.Fprintf(c.conn, "A1 APPEND {%d}\r\n", len(name)); err != nil {
				t.Fatal(err)
			}
			if line := c.line(); !strings.HasPrefix(line, "+ ") {
				t.Fatalf("mailbox continuation: %q", line)
			}
			if _, err := fmt.Fprintf(c.conn, "%s%s\r\n", name, rest); err != nil {
				t.Fatal(err)
			}
			if line := c.line(); !strings.HasPrefix(line, tc.response) {
				t.Fatalf("got %q, want %q", line, tc.response)
			}
			if len(box.records) != 0 {
				t.Fatalf("invalid APPEND stored: %+v", box.records)
			}
			_, tagged := c.command("N1", "NOOP")
			requireOK(t, tagged, "N1")
		})
	}
	t.Run("non-synchronizing message after refusal closes", func(t *testing.T) {
		box := &appendMailbox{}
		c := selectionClient(t, box)
		if _, err := io.WriteString(c.conn, "A1 APPEND {3+}\r\nabc  {3+}\r\nxyz\r\n"); err != nil {
			t.Fatal(err)
		}
		if line := c.line(); !strings.HasPrefix(line, "A1 BAD ") {
			t.Fatalf("doubled separator: %q", line)
		}
		if line := c.line(); !strings.HasPrefix(line, "* BYE ") {
			t.Fatalf("unread literal refusal: %q", line)
		}
		if len(box.records) != 0 {
			t.Fatalf("invalid APPEND stored: %+v", box.records)
		}
	})
}
