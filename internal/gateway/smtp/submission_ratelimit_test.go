package smtp

import (
	"testing"
)

// submitOne runs one authenticated submission transaction (MAIL/RCPT/DATA) to a
// remote recipient and returns the final DATA reply line.
func submitOne(h *smtpHarness, from, to string) string {
	if r := h.cmd("MAIL FROM:<%s>", from); replyCode(r) != "250" {
		h.t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<%s>", to); replyCode(r) != "250" {
		h.t.Fatalf("RCPT TO = %q", r)
	}
	return h.dataBody("Subject: hi\r\nFrom: alice@example.com\r\n\r\nbody\r\n")
}

// TestSubmissionRateLimited is a red-green guard for #171: authenticated
// submission must be rate limited per account, so a single compromised
// credential cannot flood outbound mail. With a per-minute cap of 2, the third
// message in the window is refused with a transient 4xx while the first two are
// accepted. Before the fix there was no submission rate limit, so all three were
// accepted (250).
func TestSubmissionRateLimited(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()

	// Cap at 2/minute so the third submission trips the limit quickly.
	h := newSMTPHarness(t, back, store, true, func(s *Server) {
		s.SetSubmissionRateLimit(2, 100)
	})
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}

	if r := submitOne(h, "alice@example.com", "carol@remote.test"); replyCode(r) != "250" {
		t.Fatalf("1st submission should be accepted, got %q", r)
	}
	if r := submitOne(h, "alice@example.com", "carol@remote.test"); replyCode(r) != "250" {
		t.Fatalf("2nd submission should be accepted, got %q", r)
	}
	r := submitOne(h, "alice@example.com", "carol@remote.test")
	if replyCode(r) != "451" {
		t.Fatalf("3rd submission past the per-account cap must be refused 451, got %q", r)
	}

	// Exactly the two accepted submissions reached the outbound queue; the
	// throttled one committed nothing.
	if got := len(store.queued()); got != 2 {
		t.Fatalf("queued %d outbound messages, want 2 (throttled message must not enqueue)", got)
	}
}

// TestSubmissionRateLimit_PerAccountKey checks the limiter counts by account key
// so linked From addresses of one account share the cap (a compromised
// credential cannot multiply its allowance by rotating authorized senders).
func TestSubmissionRateLimit_PerAccountKey(t *testing.T) {
	l := newSubmissionRateLimiter(2, 100)
	if !l.Allow("acct:1") || !l.Allow("acct:1") {
		t.Fatal("first two under the cap should be allowed")
	}
	if l.Allow("acct:1") {
		t.Fatal("third over the cap should be denied")
	}
	// A different account is independent.
	if !l.Allow("acct:2") {
		t.Fatal("a different account must have its own budget")
	}
}

// TestSubmissionRateLimiter_NilAllowsAll documents that a session without a
// limiter (nil) is never throttled, so unit tests that build a bare session are
// unaffected.
func TestSubmissionRateLimiter_NilAllowsAll(t *testing.T) {
	var l *submissionRateLimiter
	for i := 0; i < 1000; i++ {
		if !l.Allow("acct:1") {
			t.Fatal("nil limiter must allow every request")
		}
	}
}
