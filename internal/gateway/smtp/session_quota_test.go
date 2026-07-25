package smtp

import (
	"strings"
	"testing"
)

// TestSMTP_OverQuotaRecipient_Returns452 proves that an inbound message to a
// local recipient whose mailbox is over quota is answered 452 4.2.2 (RFC 3463
// §3.2, mailbox full) rather than an opaque 451 4.3.0. The API surfaces
// over-quota delivery as HTTP 422 (mailbox_full); the gateway must map that to
// the machine-readable mailbox-full status so the sending MTA understands the
// mailbox is full and retries, instead of receiving a generic temporary-failure
// signal that hides the cause.
func TestSMTP_OverQuotaRecipient_Returns452(t *testing.T) {
	back := newMockBackend()
	back.local["full@local.test"] = true        // local recipient: passes RCPT
	back.deliverStatus["full@local.test"] = 422 // mailbox over quota at delivery
	store := newMockStore()

	h := newSMTPHarness(t, back, store, false) // inbound (port 25), no auth
	h.ehlo()

	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<full@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}

	final := h.dataBody(testBody)

	if replyCode(final) != "452" {
		t.Errorf("DATA reply = %q, want 452 (RFC 3463 mailbox full)", final)
	}
	if !strings.Contains(final, "4.2.2") {
		t.Errorf("DATA reply = %q, want enhanced status 4.2.2", final)
	}
	// Nothing was committed.
	if d := back.deliveredTo(); len(d) != 0 {
		t.Errorf("delivered = %v, want none (over quota)", d)
	}
}
