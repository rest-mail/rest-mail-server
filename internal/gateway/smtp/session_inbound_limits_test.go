package smtp

import (
	"fmt"
	"strings"
	"testing"

	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
)

// inboundData opens an inbound (port 25) transaction to a local mailbox and answers the reply to
// DATA with body, and whether a message was delivered.
func inboundData(t *testing.T, body string) (string, bool) {
	t.Helper()
	back := newMockBackend()
	back.local["alice@local.test"] = true
	h := newSMTPHarness(t, back, newMockStore(), false)
	h.ehlo()
	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<alice@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	final := h.dataBody(body)
	return final, back.lastDeliverReq() != nil
}

func withReceived(n int) string {
	return strings.Repeat("Received: from relay.test by relay.test; Tue, 3 Nov 2026 10:00:00 +0000\r\n", n) + testBody
}

// A message whose Received: fields, with the one this server stamps, pass the hop limit is a
// mail loop and refused for good at DATA (RFC 5321 §6.3, #1647); one at the limit is delivered.
func TestSMTP_HopLimit(t *testing.T) {
	limit := rmime.CurrentLimits().MaxReceived
	if final, delivered := inboundData(t, withReceived(limit-1)); replyCode(final) != "250" || !delivered {
		t.Fatalf("%d Received fields + ours: DATA = %q, delivered %v; want 250 and delivered", limit-1, final, delivered)
	}
	final, delivered := inboundData(t, withReceived(limit))
	if !strings.HasPrefix(final, "554 5.4.6") || delivered {
		t.Fatalf("%d Received fields + ours: DATA = %q, delivered %v; want 554 5.4.6 and nothing delivered", limit, final, delivered)
	}
}

// A message nested past the MIME depth limit is refused for good at DATA (#1641); ordinary
// nesting is delivered.
func TestSMTP_MIMEStructureLimit(t *testing.T) {
	nested := func(depth int) string {
		var head, tail strings.Builder
		head.WriteString("From: sender@remote.test\r\nTo: alice@local.test\r\nSubject: nested\r\nMIME-Version: 1.0\r\n")
		for i := 0; i < depth; i++ {
			fmt.Fprintf(&head, "Content-Type: multipart/mixed; boundary=b%d\r\n\r\n--b%d\r\n", i, i)
		}
		head.WriteString("Content-Type: text/plain\r\n\r\nbottom\r\n")
		for i := depth - 1; i >= 0; i-- {
			fmt.Fprintf(&tail, "--b%d--\r\n", i)
		}
		return head.String() + tail.String()
	}
	limit := rmime.CurrentLimits().MaxNestingDepth
	if final, delivered := inboundData(t, nested(5)); replyCode(final) != "250" || !delivered {
		t.Fatalf("depth 5: DATA = %q, delivered %v; want 250 and delivered", final, delivered)
	}
	final, delivered := inboundData(t, nested(limit+1))
	if !strings.HasPrefix(final, "554 5.6.0") || delivered {
		t.Fatalf("depth %d: DATA = %q, delivered %v; want 554 5.6.0 and nothing delivered", limit+1, final, delivered)
	}
}
