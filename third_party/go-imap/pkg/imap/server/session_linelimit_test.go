package server

import (
	"strings"
	"testing"
)

// TestCommandLineLengthLimit proves an unauthenticated client cannot force
// unbounded buffering by sending an arbitrarily long command line. Before the
// fix, ReadString('\n') accumulated the whole line and it was parsed as a normal
// command (here CAPABILITY, whose trailing args are ignored) — so the server
// answered OK and had buffered the entire payload. After the fix, a line over
// MaxCommandLineLength is rejected with a BAD "too long" response without the
// whole line ever being retained.
func TestCommandLineLengthLimit(t *testing.T) {
	mock := newMockBackend()
	h := newIMAPHarness(t, mock) // consumes the greeting

	// 64 KiB of padding on an otherwise valid command line — far over any
	// sane cap. Literal is hardcoded (not MaxCommandLineLength) so this test
	// compiles and demonstrates RED against the pre-fix code too.
	huge := strings.Repeat("A", 64*1024)
	h.send("a1 CAPABILITY %s", huge)

	line := h.readLine()
	if !strings.Contains(line, "BAD") || !strings.Contains(strings.ToLower(line), "too long") {
		t.Fatalf("over-long command line: got %q, want a BAD ... too long rejection", line)
	}

	// The connection must remain usable: a normal-length command still works.
	if _, status := h.command("a2", "CAPABILITY"); !strings.Contains(status, "OK") {
		t.Fatalf("CAPABILITY after over-long line: status = %q, want OK", status)
	}
}

// TestCommandLineWithinLimitAccepted guards against over-restriction: a command
// line comfortably under the cap is processed normally.
func TestCommandLineWithinLimitAccepted(t *testing.T) {
	mock := newMockBackend()
	h := newIMAPHarness(t, mock)

	// A ~1 KiB tag, well under the 8 KiB default; CAPABILITY takes no arguments.
	pad := strings.Repeat("A", 1024)
	untagged, status := h.command(pad, "CAPABILITY")
	if !strings.Contains(status, "OK") {
		t.Fatalf("under-limit CAPABILITY: status = %q, want OK", status)
	}
	if len(untagged) == 0 || !strings.HasPrefix(untagged[0], "* CAPABILITY") {
		t.Fatalf("under-limit CAPABILITY: untagged = %v, want a * CAPABILITY line", untagged)
	}
}

// TestLineTailAcrossChunks checks the "+}" detection on an over-long line's
// tail however the marker and its terminator are split across reads.
func TestLineTailAcrossChunks(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"a {5+}\r\n", true},
		{"a {5+}\n", true},
		{"a {5}\r\n", false},
		{"a +\r\n", false},
		{"a x\r\n", false},
	} {
		for split := 0; split <= len(tc.line); split++ {
			var tail []byte
			tail = lineTail(tail, []byte(tc.line[:split]))
			for i := split; i < len(tc.line); i++ {
				tail = lineTail(tail, []byte{tc.line[i]})
			}
			if got := endsNonSyncLiteral(tail); got != tc.want {
				t.Errorf("%q split at %d: got %t, want %t", tc.line, split, got, tc.want)
			}
		}
	}
}
