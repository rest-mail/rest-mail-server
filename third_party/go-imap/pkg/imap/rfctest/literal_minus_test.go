package rfctest

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// TestLiteralMinus: the server advertises LITERAL- and not LITERAL+, since it
// "MUST NOT advertise both" (RFC 7888 §5). A non-synchronizing literal, {N+},
// gets no continuation and its octets and the line after it belong to the
// same command (§3), as in the RFC's LOGIN example.
//
// checklist: IMAP-LITMINUS-01 IMAP-LITMINUS-02
// checklist: IMAP2-CORE-26
func TestLiteralMinus(t *testing.T) {
	c := dial(t)
	c.line()
	requireCaps(t, c.capabilities("C1"), []string{"LITERAL-"}, []string{"LITERAL+"})

	// C: A001 LOGIN {11+}
	// C: FRED FOOBAR {7+}
	// C: fat man
	// S: A001 OK LOGIN completed
	wire := fmt.Sprintf("A001 LOGIN {%d+}\r\n%s {%d+}\r\n%s\r\n", len(testUser), testUser, len(testPass), testPass)
	if _, err := io.WriteString(c.conn, wire); err != nil {
		t.Fatal(err)
	}
	if got := c.line(); !strings.HasPrefix(got, "A001 OK ") {
		t.Fatalf("LOGIN with non-synchronizing literals: %q, want OK with no continuation", got)
	}
}

// TestLiteralMinusLimit: with LITERAL-, a non-synchronizing literal "MUST NOT
// be larger than 4096 bytes" (RFC 7888 §5). One of 4096 octets is taken, as a
// command argument and as APPEND's message; one of 4097 has its octets in
// flight, so the server refuses the command with BAD and ends the session with
// BYE (§4's second choice), both carrying TOOBIG, which APPEND's refusal MUST
// and the BYE SHOULD. The octets never run as commands. A synchronizing literal
// of any size APPEND takes is unaffected.
//
// checklist: IMAP-LITMINUS-03 IMAP-LITMINUS-04
// checklist: IMAP2-CORE-26
func TestLiteralMinusLimit(t *testing.T) {
	// Octets that would sign in and append if they were read as commands.
	smuggled := func(n int) string {
		cmd := "X1 LOGIN " + testUser + " " + testPass + "\r\nX2 APPEND INBOX {3+}\r\nabc\r\n"
		return cmd + strings.Repeat("x", n-len(cmd))
	}
	for _, tc := range []struct {
		name string
		wire func(n int) string
	}{
		{"argument", func(n int) string {
			return fmt.Sprintf("A1 SELECT {%d+}\r\n%s\r\n", n, smuggled(n))
		}},
		{"APPEND mailbox", func(n int) string {
			return fmt.Sprintf("A1 APPEND {%d+}\r\n%s {3}\r\n", n, smuggled(n))
		}},
		{"APPEND message", func(n int) string {
			return fmt.Sprintf("A1 APPEND INBOX {%d+}\r\n%s\r\n", n, smuggled(n))
		}},
	} {
		t.Run(tc.name+"/4097", func(t *testing.T) {
			box := &appendMailbox{}
			c := dialMailbox(t, box)
			c.line()
			c.login()
			written := make(chan error, 1)
			go func() { _, err := io.WriteString(c.conn, tc.wire(4097)); written <- err }()
			t.Cleanup(func() { _ = c.conn.Close(); <-written })
			if got := c.line(); !strings.HasPrefix(got, "A1 BAD [TOOBIG] ") {
				t.Fatalf("refusal: %q", got)
			}
			if got := c.line(); !strings.HasPrefix(got, "* BYE [TOOBIG] ") {
				t.Fatalf("closing: %q", got)
			}
			if !c.closed() {
				t.Fatal("the session went on after an oversized non-synchronizing literal")
			}
			<-c.done
			if len(box.records) != 0 || c.backend.calls != 1 {
				t.Fatalf("the literal's octets ran: records=%+v sign-ins=%d", box.records, c.backend.calls)
			}
		})
	}

	box := &appendMailbox{}
	c := dialMailbox(t, box)
	c.line()
	c.login()
	name := strings.Repeat("n", 4096)
	if _, err := fmt.Fprintf(c.conn, "A2 STATUS {4096+}\r\n%s (MESSAGES)\r\n", name); err != nil {
		t.Fatal(err)
	}
	for got := c.line(); !strings.HasPrefix(got, "A2 "); got = c.line() {
		if strings.HasPrefix(got, "+") {
			t.Fatalf("a continuation for a non-synchronizing literal: %q", got)
		}
	}
	message := "Subject: hi\r\n\r\n" + strings.Repeat("b", 4096-len("Subject: hi\r\n\r\n"))
	if _, err := fmt.Fprintf(c.conn, "A1 APPEND INBOX {4096+}\r\n%s\r\n", message); err != nil {
		t.Fatal(err)
	}
	if _, tagged := appendCompletion(t, c); !strings.HasPrefix(tagged, "A1 OK") {
		t.Fatalf("4096-octet non-synchronizing APPEND: %q", tagged)
	}
	big := "Subject: big\r\n\r\n" + strings.Repeat("b", 20000)
	if _, tagged := appendCommand(t, c, "INBOX", big); !strings.HasPrefix(tagged, "A1 OK") {
		t.Fatalf("synchronizing APPEND over 4096 octets: %q", tagged)
	}
	if len(box.records) != 2 || box.records[0].raw != message || box.records[1].raw != big {
		t.Fatalf("appended %d messages", len(box.records))
	}
}
