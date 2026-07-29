package smtp

import (
	"strings"
	"testing"
	"time"
)

// firstHeaderName returns the name of the first header of a raw RFC 5322
// message (the token before the first ':' on the first line).
func firstHeaderName(raw string) string {
	line := raw
	if i := strings.IndexAny(raw, "\r\n"); i >= 0 {
		line = raw[:i]
	}
	if c := strings.IndexByte(line, ':'); c >= 0 {
		return line[:c]
	}
	return line
}

// assertReceivedTrace asserts raw carries a well-formed Received trace header as
// its very first header, with the expected from/by/with clauses, an id, and a
// parseable RFC 5322 date.
func assertReceivedTrace(t *testing.T, raw, wantWith, wantFor string) {
	t.Helper()
	if got := firstHeaderName(raw); got != "Received" {
		t.Fatalf("first header = %q, want Received; message begins:\n%q", got, head(raw))
	}
	// The Received header is the first (possibly folded) header; take everything
	// up to the first non-folded line boundary.
	recv := raw
	if end := endOfFirstHeader(raw); end >= 0 {
		recv = raw[:end]
	}
	for _, want := range []string{
		"from client.test",              // HELO name
		"by smtp.test (RESTMAIL) with ", // our host + product token
		"with " + wantWith,              // RFC 3848 protocol keyword
		" id ",                          // queue/message id clause
	} {
		if !strings.Contains(recv, want) {
			t.Errorf("Received header missing %q; got:\n%s", want, recv)
		}
	}
	if wantFor != "" && !strings.Contains(recv, "for <"+wantFor+">") {
		t.Errorf("Received header missing for-clause for %q; got:\n%s", wantFor, recv)
	}
	// The date follows the final "; " and must parse as an RFC 5322 date-time.
	semi := strings.LastIndex(recv, "; ")
	if semi < 0 {
		t.Fatalf("Received header has no date separator; got:\n%s", recv)
	}
	dateStr := strings.TrimSpace(recv[semi+2:])
	if _, err := time.Parse("Mon, 02 Jan 2006 15:04:05 -0700", dateStr); err != nil {
		t.Errorf("Received date %q does not parse as RFC 5322 date-time: %v", dateStr, err)
	}
}

// endOfFirstHeader returns the offset at which the first header ends: the index
// of the first CRLF (or LF) that is NOT followed by folding whitespace.
func endOfFirstHeader(raw string) int {
	i := 0
	for i < len(raw) {
		nl := strings.IndexByte(raw[i:], '\n')
		if nl < 0 {
			return len(raw)
		}
		abs := i + nl
		next := abs + 1
		if next >= len(raw) || (raw[next] != ' ' && raw[next] != '\t') {
			return abs + 1
		}
		i = next
	}
	return len(raw)
}

func head(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// TestSMTP_InboundStampsReceived proves the inbound-MX acceptance path prepends a
// Received trace header to the stored message. RED before the fix: the message
// was delivered verbatim with no Received header.
func TestSMTP_InboundStampsReceived(t *testing.T) {
	back := newMockBackend()
	back.local["alice@local.test"] = true
	store := newMockStore()

	h := newSMTPHarness(t, back, store, false) // inbound (port 25)
	h.ehlo()

	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<alice@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if final := h.dataBody(testBody); replyCode(final) != "250" {
		t.Fatalf("DATA = %q, want 250", final)
	}

	req := back.lastDeliverReq()
	if req == nil {
		t.Fatal("no message delivered")
	}
	// Inbound, unauthenticated, no TLS in the harness → ESMTP.
	assertReceivedTrace(t, string(req.RawMessage), "ESMTP", "alice@local.test")
}

// TestSMTP_SubmissionOutboundStampsReceived proves the authenticated submission
// path prepends a Received header to the message enqueued for outbound relay.
// RED before the fix: the queued RawMessage was the client's bytes verbatim.
func TestSMTP_SubmissionOutboundStampsReceived(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()
	// testBody's From: header (sender@remote.test) is an authorized linked
	// address, so the #181 From-header check accepts the submission.
	store.authorized["sender@remote.test"] = true

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
	if final := h.dataBody(testBody); replyCode(final) != "250" {
		t.Fatalf("DATA = %q, want 250", final)
	}

	queued := store.queued()
	if len(queued) != 1 {
		t.Fatalf("enqueued %d, want 1", len(queued))
	}
	// Authenticated over TLS → ESMTPSA (RFC 3848). It was ESMTPA while the harness ran
	// in the clear, which no listener does: submission is implicit TLS.
	assertReceivedTrace(t, queued[0].RawMessage, "ESMTPSA", "carol@remote.test")
}
