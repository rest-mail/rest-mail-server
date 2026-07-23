package smtp

import (
	"reflect"
	"strings"
	"testing"
)

const testBody = "Subject: Hi\r\nFrom: Sender <sender@remote.test>\r\n\r\nHello there.\r\n"

// TestSMTP_MultiRecipientPartialDelivery_SingleReply reproduces the #49 fix: in a
// multi-recipient DATA where one recipient is committed and another fails, the
// transaction must return a SINGLE 250 — never a 4xx after a partial commit,
// which would make the client retry the whole message and duplicate the already
// delivered recipient.
func TestSMTP_MultiRecipientPartialDelivery_SingleReply(t *testing.T) {
	back := newMockBackend()
	back.local["alice@local.test"] = true
	back.local["bob@local.test"] = true
	back.deliverFail["bob@local.test"] = true // second recipient fails to deliver
	store := newMockStore()

	h := newSMTPHarness(t, back, store, false) // inbound (port 25)
	h.ehlo()

	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<alice@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT alice = %q", r)
	}
	if r := h.cmd("RCPT TO:<bob@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT bob = %q", r)
	}

	final := h.dataBody(testBody)
	if replyCode(final) != "250" {
		t.Errorf("partial delivery must yield a single 250, got %q", final)
	}
	// Only the succeeding recipient was committed; the failure did not roll it back.
	if got := back.deliveredTo(); !reflect.DeepEqual(got, []string{"alice@local.test"}) {
		t.Errorf("delivered = %v, want [alice@local.test]", got)
	}
}

// TestSMTP_SenderAuthorization exercises the submission-path MAIL FROM check:
// the authenticated user itself and a linked address are accepted; an unrelated
// address is rejected 553.
func TestSMTP_SenderAuthorization(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()
	store.authorized["linked@example.com"] = true // a linked mailbox of the account

	h := newSMTPHarness(t, back, store, true) // submission
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}

	// The authenticated identity itself: accepted without any store lookup.
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
		t.Errorf("self sender should be accepted, got %q", r)
	}
	// A linked address the store authorizes: accepted.
	if r := h.cmd("MAIL FROM:<linked@example.com>"); replyCode(r) != "250" {
		t.Errorf("linked sender should be accepted, got %q", r)
	}
	// An unauthorized address: rejected 553.
	if r := h.cmd("MAIL FROM:<mallory@example.com>"); replyCode(r) != "553" {
		t.Errorf("unauthorized sender should be rejected 553, got %q", r)
	}
}

// TestSMTP_LocalVsRemoteRouting proves DATA routes each recipient by locality:
// a local recipient is delivered via the API, a remote one is enqueued for the
// outbound worker — in one transaction with a single 250.
func TestSMTP_LocalVsRemoteRouting(t *testing.T) {
	back := newMockBackend()
	back.local["bob@local.test"] = true // local; carol@remote.test is not
	store := newMockStore()

	h := newSMTPHarness(t, back, store, true) // submission
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<bob@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT local = %q", r)
	}
	// Submission accepts a non-local recipient for outbound queueing.
	if r := h.cmd("RCPT TO:<carol@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT remote = %q", r)
	}

	final := h.dataBody(testBody)
	if replyCode(final) != "250" {
		t.Errorf("DATA final = %q, want 250", final)
	}

	// Local recipient delivered via the API.
	if got := back.deliveredTo(); !reflect.DeepEqual(got, []string{"bob@local.test"}) {
		t.Errorf("delivered = %v, want [bob@local.test]", got)
	}
	// Remote recipient enqueued for outbound, with the destination domain parsed.
	queued := store.queued()
	if len(queued) != 1 {
		t.Fatalf("enqueued %d messages, want 1: %+v", len(queued), queued)
	}
	if queued[0].Recipient != "carol@remote.test" || queued[0].Domain != "remote.test" {
		t.Errorf("enqueued = %+v, want recipient carol@remote.test domain remote.test", queued[0])
	}
	if queued[0].Sender != "alice@example.com" {
		t.Errorf("enqueued sender = %q, want alice@example.com", queued[0].Sender)
	}
}

// TestSMTP_LongLineAccepted guards old-engine parity on line length: the
// hand-rolled engine had no per-line limit, while go-smtp defaults to 2000
// bytes even during DATA. Real-world unwrapped 8-bit/HTML mail carries longer
// lines (Postfix wraps rather than rejects them), so a message with a single
// >2000-byte body line must still be accepted and delivered.
func TestSMTP_LongLineAccepted(t *testing.T) {
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

	longLine := strings.Repeat("a", 5000) // well past go-smtp's 2000-byte default
	body := "Subject: Long\r\nFrom: Sender <sender@remote.test>\r\n\r\n" + longLine + "\r\n"
	final := h.dataBody(body)
	if replyCode(final) != "250" {
		t.Errorf("DATA with >2000-byte line = %q, want 250", final)
	}
	if got := back.deliveredTo(); !reflect.DeepEqual(got, []string{"alice@local.test"}) {
		t.Errorf("delivered = %v, want [alice@local.test]", got)
	}
}

// TestSMTP_EhloAdvertisements guards the untouched EHLO capabilities: the SIZE
// value matches the enforced limit, AUTH is offered on submission, and the
// RESTMAIL server-to-server upgrade hint is advertised.
func TestSMTP_EhloAdvertisements(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()
	h := newSMTPHarness(t, back, store, true) // submission

	lines, final := h.readReplyAfter("EHLO client.test")
	if replyCode(final) != "250" {
		t.Fatalf("EHLO final = %q", final)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"SIZE 10485760", // == maxMessageSize (10 MiB), matches DATA enforcement
		"AUTH PLAIN LOGIN",
		"RESTMAIL https://smtp.test/restmail",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("EHLO missing %q; got:\n%s", want, joined)
		}
	}
}
