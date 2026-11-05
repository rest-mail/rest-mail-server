package smtp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/outbound"
)

// Submission keeps what a client sent, so a row can hold any mix of line ends with a Bcc field in
// its head (#1970). The row stays as it was sent, and what leaves it has no Bcc field.
func TestSubmissionQueuesMixedLineEndsExactly(t *testing.T) {
	const from = "From: alice@example.com"
	for name, message := range map[string]string{
		"LF head, CRLF blank later": from + "\nBcc: secret@c.test\nSubject: s\n\nbody\n\r\n\r\nmore",
		"one LF in the head":        from + "\r\nBcc: secret@c.test\nSubject: s\r\n\r\nbody",
		"bare CR in the head":       from + "\r\nSubject: s\rBcc: secret@c.test\r\nTo: c@d.test\r\n\r\nbody",
		"all LF":                    from + "\nBcc: secret@c.test\nSubject: s\n\nbody\n",
	} {
		t.Run(name, func(t *testing.T) {
			back := newMockBackend()
			store := newMockStore()
			h := newSMTPHarness(t, back, store, true) // submission, as port 465
			h.ehlo()
			if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
				t.Fatalf("AUTH = %q", r)
			}
			if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
				t.Fatalf("MAIL FROM = %q", r)
			}
			if r := h.cmd("RCPT TO:<carol@remote.test>"); replyCode(r) != "250" {
				t.Fatalf("RCPT = %q", r)
			}
			if r := h.cmd("DATA"); !strings.HasPrefix(r, "354") {
				t.Fatalf("DATA = %q", r)
			}
			fmt.Fprint(h.cw, message+"\r\n.\r\n")
			if err := h.cw.Flush(); err != nil {
				t.Fatal(err)
			}
			if _, final := h.readReply(); replyCode(final) != "250" {
				t.Fatalf("end of DATA = %q, want 250: the client can't produce this row", final)
			}

			queued := store.queued()
			if len(queued) != 1 {
				t.Fatalf("queued %d, want 1", len(queued))
			}
			row := queued[0].RawMessage
			if !strings.HasSuffix(row, message+"\r\n") {
				t.Fatalf("the row isn't what the client sent: %q", row)
			}
			if sent := outbound.StripBcc(row); strings.Contains(sent, "secret@") {
				t.Fatalf("the Bcc would leave this row: %q", sent)
			}
		})
	}
}
