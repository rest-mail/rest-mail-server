package rfctest

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap/server"
)

// writeLiteralWire permits replies while a long command is still being written.
// Refusal may close before all bytes are read, so an early write error is valid.
func writeLiteralWire(t *testing.T, c *client, wire string) {
	t.Helper()
	written := make(chan struct{})
	go func() {
		defer close(written)
		_, _ = io.WriteString(c.conn, wire)
	}()
	t.Cleanup(func() { _ = c.conn.Close(); <-written })
}

// TestCommandLiteralRefusalCloses proves resource refusals cannot turn unread
// literal bytes into commands. Header-only cases prove no draining is required.
//
// checklist: IMAP-LITERAL-02 IMAP-LITERAL-03 IMAP-LITERAL-04 IMAP-RESP-13
func TestCommandLiteralRefusalCloses(t *testing.T) {
	limit := server.MaxLiteralSize
	for _, tc := range []struct{ name, header string }{
		{"oversized", fmt.Sprintf("A1 LOGIN {%d+}\r\n", limit+1)},
		{"maximum uint32", "A1 LOGIN {4294967295+}\r\n"},
		{"unknown command", fmt.Sprintf("A1 FROBNICATE {%d+}\r\n", limit+1)},
		{"aggregate", fmt.Sprintf("A1 LOGIN {%d+}\r\n%s {%d+}\r\n", limit/2, strings.Repeat("a", limit/2), limit/2+1)},
		// Quoting doubles backslashes, taking assembled arguments over the
		// command cap before the next (small) literal can be read.
		{"expanded arguments", fmt.Sprintf("A1 LOGIN {%d+}\r\n%s {1+}\r\n", limit/2, strings.Repeat(`\`, limit/2))},
		{"overlong continuation", "A1 LOGIN {1+}\r\na " + strings.Repeat("x", server.MaxCommandLineLength+1) + " {1+}\r\n"},
	} {
		for _, pipelined := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pipelined=%t", tc.name, pipelined), func(t *testing.T) {
				c := dial(t)
				c.line()
				wire := tc.header
				if pipelined {
					wire += "X1 LOGIN " + testUser + " " + testPass + "\r\nN1 NOOP\r\n"
				}
				writeLiteralWire(t, c, wire)
				if line := c.line(); !strings.HasPrefix(line, "A1 BAD ") {
					t.Fatalf("refusal: %q", line)
				}
				if line := c.line(); !strings.HasPrefix(line, "* BYE ") || !strings.Contains(line, "literal") {
					t.Fatalf("closing explanation: %q", line)
				}
				if !c.closed() {
					t.Fatal("refused payload or following command was dispatched")
				}
				<-c.done
				if c.backend.calls != 0 {
					t.Fatalf("payload authenticated %d times", c.backend.calls)
				}
			})
		}
	}
}

// TestCommandLiteralSafeRefusal preserves synchronization when no literal bytes
// are in flight: before a synchronizing continuation, or after a whole command.
//
// checklist: IMAP-LITERAL-01 IMAP-LITERAL-05
func TestCommandLiteralSafeRefusal(t *testing.T) {
	limit := server.MaxLiteralSize
	for _, tc := range []struct{ name, wire string }{
		{"oversized synchronizing", fmt.Sprintf("A1 LOGIN {%d}\r\n", limit+1)},
		{"aggregate synchronizing", fmt.Sprintf("A1 LOGIN {%d+}\r\n%s {%d}\r\n", limit/2, strings.Repeat("a", limit/2), limit/2+1)},
		{"expanded before synchronizing", fmt.Sprintf("A1 LOGIN {%d+}\r\n%s {1}\r\n", limit/2, strings.Repeat(`\`, limit/2))},
		{"expanded complete command", fmt.Sprintf("A1 LOGIN {%d+}\r\n%s\r\n", limit/2, strings.Repeat(`\`, limit/2))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dial(t)
			c.line()
			writeLiteralWire(t, c, tc.wire+"N1 NOOP\r\n")
			if line := c.line(); !strings.HasPrefix(line, "A1 BAD ") {
				t.Fatalf("refusal: %q", line)
			}
			requireOK(t, c.line(), "N1")
			_, tagged := c.command("N2", "NOOP")
			requireOK(t, tagged, "N2")
			if c.backend.calls != 0 {
				t.Fatalf("refused command authenticated %d times", c.backend.calls)
			}
		})
	}
}

// TestCommandLiteralAccepted checks counted literal arguments preserve command
// framing and support both synchronization forms, including empty credentials.
//
// checklist: IMAP-LITERAL-06 IMAP-RESP-17
func TestCommandLiteralAccepted(t *testing.T) {
	for _, sync := range []bool{false, true} {
		t.Run(fmt.Sprintf("synchronizing=%t", sync), func(t *testing.T) {
			c := dial(t)
			c.line()
			// Empty literals must reach credential validation before LOGIN
			// succeeds: authenticated sessions reject any further LOGIN.
			writeLiteralWire(t, c, "A0 LOGIN {0+}\r\n {0+}\r\n\r\nN0 NOOP\r\n")
			if line := c.line(); !strings.HasPrefix(line, "A0 NO ") {
				t.Fatalf("empty credentials: %q", line)
			}
			requireOK(t, c.line(), "N0")
			if c.backend.calls != 1 {
				t.Fatalf("Authenticate called %d times for empty credentials, want 1", c.backend.calls)
			}
			if sync {
				writeLiteralWire(t, c, fmt.Sprintf("A1 LOGIN {%d}\r\n", len(testUser)))
				if line := c.line(); !strings.HasPrefix(line, "+ ") {
					t.Fatalf("first continuation: %q", line)
				}
				writeLiteralWire(t, c, fmt.Sprintf("%s {%d}\r\n", testUser, len(testPass)))
				if line := c.line(); !strings.HasPrefix(line, "+ ") {
					t.Fatalf("second continuation: %q", line)
				}
				writeLiteralWire(t, c, testPass+"\r\nN1 NOOP\r\n")
			} else {
				writeLiteralWire(t, c, fmt.Sprintf("A1 LOGIN {%d+}\r\n%s {%d+}\r\n%s\r\nN1 NOOP\r\n", len(testUser), testUser, len(testPass), testPass))
			}
			requireOK(t, c.line(), "A1")
			requireOK(t, c.line(), "N1")
			if c.backend.calls != 2 {
				t.Fatalf("Authenticate called %d times, want 2", c.backend.calls)
			}
		})
	}
}

// readFaultConn reports one read error at an exact wire boundary, then permits
// reads again. This detects a command loop that incorrectly retries an
// incomplete literal as though the unread suffix were a new command.
type readFaultConn struct {
	net.Conn
	remaining int
	failed    bool
}

func (c *readFaultConn) Read(p []byte) (int, error) {
	if !c.failed {
		if c.remaining == 0 {
			c.failed = true
			return 0, errors.New("injected literal read failure")
		}
		if len(p) > c.remaining {
			p = p[:c.remaining]
		}
	}
	n, err := c.Conn.Read(p)
	c.remaining -= n
	return n, err
}

// TestCommandLiteralReadFailureEnds ensures neither a failed body read nor a
// failed continuation read can resume by interpreting remaining bytes as commands.
//
// checklist: IMAP-LITERAL-07
func TestCommandLiteralReadFailureEnds(t *testing.T) {
	for _, tc := range []struct{ name, prefix string }{
		{"body", "A1 LOGIN {100+}\r\na"},
		{"continuation", "A1 LOGIN {1+}\r\na "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dialConn(t, nil, func(conn net.Conn) net.Conn {
				return &readFaultConn{Conn: conn, remaining: len(tc.prefix)}
			})
			c.line()
			writeLiteralWire(t, c, tc.prefix+"X1 LOGIN "+testUser+" "+testPass+"\r\nN1 NOOP\r\n")
			if !c.closed() {
				t.Fatal("session resumed after a failed literal read")
			}
			<-c.done
			if c.backend.calls != 0 {
				t.Fatalf("incomplete command authenticated %d times", c.backend.calls)
			}
		})
	}
}

// overlongLine pads an A1 LOGIN line so the whole line, CRLF included, is
// length octets ending in suffix. Lengths just past a 4 KiB read-buffer boundary
// split the marker and terminator across chunks.
func overlongLine(length int, suffix string) string {
	head := "A1 LOGIN "
	return head + strings.Repeat("x", length-len(head)-len(suffix)-2) + suffix + "\r\n"
}

// TestCommandLineOverflowLiteralCloses proves a discarded initial command line
// ending in a LITERAL+ marker cannot leave its in-flight octets to run as a
// command: the server answers BAD, sends BYE and ends the session.
//
// checklist: IMAP-LITERAL-08
func TestCommandLineOverflowLiteralCloses(t *testing.T) {
	payload := "X1 LOGIN " + testUser + " " + testPass + "\r\n"
	for _, extra := range []int{1, 2, 3, 4, 5, 4096} {
		t.Run(fmt.Sprintf("extra=%d", extra), func(t *testing.T) {
			c := dial(t)
			c.line()
			length := 2*4096 + extra
			writeLiteralWire(t, c, overlongLine(length, fmt.Sprintf(" {%d+}", len(payload)))+payload+"N1 NOOP\r\n")
			if line := c.line(); !strings.HasPrefix(line, "* BAD ") {
				t.Fatalf("refusal: %q", line)
			}
			if line := c.line(); !strings.HasPrefix(line, "* BYE ") || !strings.Contains(line, "literal") {
				t.Fatalf("closing explanation: %q", line)
			}
			if !c.closed() {
				t.Fatal("in-flight literal or following command was dispatched")
			}
			<-c.done
			if c.backend.calls != 0 {
				t.Fatalf("literal payload authenticated %d times", c.backend.calls)
			}
		})
	}
}

// TestCommandLineOverflowSafeRefusal keeps the session when a discarded line
// leaves nothing in flight: no marker, or a synchronizing marker whose
// continuation was never sent.
//
// checklist: IMAP-LITERAL-09
func TestCommandLineOverflowSafeRefusal(t *testing.T) {
	for _, tc := range []struct{ name, suffix string }{
		{"no marker", " pass"},
		{"synchronizing", " {5}"},
		{"brace without plus", " }"},
	} {
		for _, extra := range []int{1, 2, 3} {
			t.Run(fmt.Sprintf("%s/extra=%d", tc.name, extra), func(t *testing.T) {
				c := dial(t)
				c.line()
				writeLiteralWire(t, c, overlongLine(2*4096+extra, tc.suffix)+"N1 NOOP\r\n")
				if line := c.line(); !strings.HasPrefix(line, "* BAD ") {
					t.Fatalf("refusal: %q", line)
				}
				requireOK(t, c.line(), "N1")
				c.login()
			})
		}
	}
}

// overlongSuffixes are line endings an overlong discarded line may carry:
// none, a synchronizing marker, and a LITERAL+ marker.
var overlongSuffixes = []struct{ name, suffix string }{
	{"no marker", " pass"},
	{"synchronizing", " {5}"},
	{"non-synchronizing", " {5+}"},
}

// overlongResponse pads a line of base64-alphabet octets to length octets,
// CRLF included, ending in suffix.
func overlongResponse(length int, suffix string) string {
	return strings.Repeat("A", length-len(suffix)-2) + suffix + "\r\n"
}

// TestAuthenticateResponseOverflow proves an overlong AUTHENTICATE response is
// refused with a tagged BAD before any credential check, and the session stays
// usable. A SASL response is base64 only (RFC 3501 §9 "authenticate"), so a
// trailing "+}" is not a literal marker there and leaves nothing in flight.
//
// checklist: IMAP-LITERAL-10
func TestAuthenticateResponseOverflow(t *testing.T) {
	for _, tc := range overlongSuffixes {
		for _, extra := range []int{1, 2, 3} {
			t.Run(fmt.Sprintf("%s/extra=%d", tc.name, extra), func(t *testing.T) {
				c := dial(t)
				c.line()
				if _, err := io.WriteString(c.conn, "A1 AUTHENTICATE PLAIN\r\n"); err != nil {
					t.Fatal(err)
				}
				if line := c.line(); line != "+ " {
					t.Fatalf("continuation: %q", line)
				}
				writeLiteralWire(t, c, overlongResponse(2*4096+extra, tc.suffix)+"N1 NOOP\r\n")
				if line := c.line(); !strings.HasPrefix(line, "A1 BAD ") {
					t.Fatalf("refusal: %q", line)
				}
				requireOK(t, c.line(), "N1")
				if c.backend.calls != 0 {
					t.Fatalf("overlong response reached the backend %d times", c.backend.calls)
				}
				c.login()
			})
		}
	}
}

// TestIdleLineOverflow proves an overlong line while idling is ignored without
// ending IDLE: a following command-shaped line is still not run, DONE
// terminates IDLE and the next command runs. Only DONE is legal input during
// IDLE (RFC 2177 §3), so no line there can carry a literal.
//
// checklist: IMAP-LITERAL-11
func TestIdleLineOverflow(t *testing.T) {
	for _, tc := range overlongSuffixes {
		for _, extra := range []int{1, 2, 3} {
			t.Run(fmt.Sprintf("%s/extra=%d", tc.name, extra), func(t *testing.T) {
				c := dial(t)
				c.line()
				c.login()
				_, tagged := c.command("S1", "SELECT INBOX")
				requireOK(t, tagged, "S1")
				if _, err := io.WriteString(c.conn, "I1 IDLE\r\n"); err != nil {
					t.Fatal(err)
				}
				if line := c.line(); line != "+ idling" {
					t.Fatalf("continuation: %q", line)
				}
				writeLiteralWire(t, c, overlongResponse(2*4096+extra, tc.suffix)+"N0 NOOP\r\nDONE\r\nN1 NOOP\r\n")
				requireOK(t, c.line(), "I1")
				requireOK(t, c.line(), "N1")
			})
		}
	}
}
