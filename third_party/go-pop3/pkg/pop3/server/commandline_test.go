package server

import (
	"strings"
	"testing"
	"time"
)

// TestPOP3_CommandLineTooLongIsBounded is the regression guard for the pre-auth
// memory-DoS in issue #9. A hostile client can stream an arbitrarily large
// "command line" with no CRLF; the old reader (bufio.Reader.ReadString('\n'))
// buffered all of it without limit, waiting forever for a newline. The bounded
// reader must instead stop at a fixed cap and answer -ERR rather than growing.
//
// Against the unbounded code this test fails: the server buffers the flood, never
// finds a '\n', and never replies, so readLine hits its deadline. Against the
// bounded code the server caps the line and replies -ERR promptly.
func TestPOP3_CommandLineTooLongIsBounded(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newPOP3Harness(t, m)

	// Far larger than any legitimate POP3 command (RFC 1939 §3 keeps them tiny),
	// and deliberately carries no CRLF so an unbounded reader never completes a
	// line. Stream it from a goroutine: the server stops reading and closes once
	// it hits the cap, so the write cannot (and need not) complete.
	huge := strings.Repeat("A", 1<<20) // 1 MiB
	go func() {
		_ = h.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = h.cw.WriteString(huge)
		_ = h.cw.Flush()
	}()

	if got := h.readLine(); !strings.HasPrefix(got, "-ERR") {
		t.Errorf("over-long command line: got %q, want -ERR (line must be bounded, not buffered)", got)
	}
}

// TestPOP3_LongButBoundedCommandStillWorks guards the other edge: a command line
// that is long-ish but still terminated and under the cap must be processed
// normally, so the fix does not reject legitimate traffic (e.g. long addresses
// or app-password tokens).
func TestPOP3_LongButBoundedCommandStillWorks(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newPOP3Harness(t, m)

	// A ~200-byte unknown command: well under the cap and CRLF-terminated. The
	// server should answer (with -ERR "Unknown command"), proving normal framing
	// is intact for long-but-bounded input.
	long := "XCMD " + strings.Repeat("z", 200)
	if got := h.cmd("%s", long); !strings.HasPrefix(got, "-ERR") {
		t.Errorf("long-but-bounded command: got %q, want a normal -ERR reply", got)
	}
	// Session must remain usable afterwards. Probe with CAPA, an AUTHORIZATION-state
	// command (RFC 2449): this harness never authenticates, and NOOP is now rejected
	// before authentication (issue #11), so it can no longer serve as a pre-auth
	// liveness check here.
	if got := h.cmd("CAPA"); !strings.HasPrefix(got, "+OK") {
		t.Errorf("session unusable after long-but-bounded command: CAPA = %q", got)
	}
	_ = h.readDotBody()
}
