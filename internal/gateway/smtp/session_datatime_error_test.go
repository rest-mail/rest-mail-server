package smtp

import "testing"

// TestSMTP_DataTimeAPIError_LocalMailNotMisroutedOutbound reproduces #182: a
// transient API error at DATA time, while re-classifying a LOCAL inbound-MX
// recipient, must NOT cause the message to be enqueued to the outbound relay
// queue. Doing so would relay internal mail externally (a leak) or MX-deliver it
// back to ourselves. Instead the transaction must tempfail (4xx) so the sender
// retries once the API recovers, matching the RCPT-time path.
//
// The recipient is local and passes RCPT (CheckMailbox call 1 succeeds,
// Exists=true), then the API goes down before the DATA-time re-check
// (CheckMailbox call 2 errors). On the buggy code the error was treated as
// "not local" and the mail was enqueued for outbound with the LOCAL domain.
func TestSMTP_DataTimeAPIError_LocalMailNotMisroutedOutbound(t *testing.T) {
	back := newMockBackend()
	back.local["bob@local.test"] = true      // local recipient: passes RCPT
	back.checkErrAfter["bob@local.test"] = 1 // RCPT check OK; DATA re-check errors
	store := newMockStore()

	h := newSMTPHarness(t, back, store, false) // inbound (port 25), no auth
	h.ehlo()

	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<bob@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}

	final := h.dataBody(testBody)

	// Core leak assertion: local-destined mail must NEVER enter the outbound
	// relay queue on a classification error.
	if q := store.queued(); len(q) != 0 {
		t.Errorf("DATA-time API error misrouted local mail to outbound queue: enqueued %d, want 0 (%+v)", len(q), q)
	}
	// Nothing was committed, so the transaction must tempfail (4xx) to trigger a
	// sender retry — not report success.
	if replyCode(final) != "451" {
		t.Errorf("DATA reply = %q, want 451 tempfail (no recipient committed)", final)
	}
	// And nothing should have been delivered locally either.
	if d := back.deliveredTo(); len(d) != 0 {
		t.Errorf("delivered = %v, want none (delivery was never attempted after check error)", d)
	}
}
