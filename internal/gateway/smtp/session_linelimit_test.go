package smtp

import (
	"strings"
	"testing"
	"time"
)

// TestSMTP_OverlongPreAuthCommandLine_Rejected proves the command-line bound
// added on the go-smtp v0.28.3 bump: a single pre-auth command line past the
// 64 KiB MaxLineLength is rejected with 500 and the connection is closed,
// capping an unauthenticated peer's command-input memory. This is the
// complement of TestSMTP_LongLineAccepted — the DATA body stays unbounded while
// command lines are now capped (rest-mail-server #200 item 2).
func TestSMTP_OverlongPreAuthCommandLine_Rejected(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()

	h := newSMTPHarness(t, back, store, true) // submission

	// The line-length limit is enforced at read time, before the command is even
	// parsed, so the exact verb is immaterial; a NOOP padded well past 64 KiB is
	// one command line that exceeds MaxLineLength. Write it from a goroutine: on
	// the synchronous net.Pipe the server stops reading and writes its 500 the
	// moment the limit trips, so the client must be free to read that reply
	// instead of still blocking on the rest of the write.
	go func() {
		_ = h.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		long := "NOOP " + strings.Repeat("x", 70<<10) + "\r\n"
		_, _ = h.cw.WriteString(long)
		_ = h.cw.Flush()
	}()

	_, final := h.readReply()
	if replyCode(final) != "500" {
		t.Errorf("over-long pre-auth command line: final = %q, want 500 (line-length rejection)", final)
	}
}
