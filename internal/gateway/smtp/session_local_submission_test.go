package smtp

import (
	"strings"
	"testing"
)

// localSubmissionBody is a message from alice to a local mailbox, with a Bcc
// header a mail client may leave in.
const localSubmissionBody = "From: Alice <alice@example.com>\r\nTo: bob@example.com\r\nBcc: hidden@example.com\r\nSubject: Hi\r\n\r\nHello.\r\n"

// submitToLocal sends localSubmissionBody from alice to the local bob over a
// signed-in submission session, and returns the final DATA reply.
func submitToLocal(t *testing.T, h *smtpHarness) string {
	t.Helper()
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<bob@example.com>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	return h.dataBody(localSubmissionBody)
}

// assertQueuedAsSubmitted checks that a submission to a mailbox on this server
// went on the outbound queue, as one to any other server does, and did not
// reach the mailbox from here (#1564). The queue worker signs it and strips its
// Bcc header on the way out.
func assertQueuedAsSubmitted(t *testing.T, back *mockBackend, store *mockStore) {
	t.Helper()
	if got := back.deliveredTo(); len(got) != 0 {
		t.Errorf("delivered directly to %v, want nothing", got)
	}
	queued := store.enqueuedMessages()
	if len(queued) != 1 {
		t.Fatalf("queued %d messages, want 1", len(queued))
	}
	if queued[0].Recipient != "bob@example.com" || queued[0].Domain != "example.com" || queued[0].Sender != "alice@example.com" {
		t.Errorf("queued %+v, want alice to bob at example.com", queued[0])
	}
	if !strings.Contains(queued[0].RawMessage, "Subject: Hi") {
		t.Errorf("queued message is not the one submitted:\n%s", queued[0].RawMessage)
	}
}

// TestSMTP_SubmissionToALocalMailbox: a message submitted with the mailbox's
// password, to a mailbox on this server, goes on the outbound queue as one sent
// from the webmail does.
func TestSMTP_SubmissionToALocalMailbox(t *testing.T) {
	back := newMockBackend()
	back.local["bob@example.com"] = true
	store := newMockStore()
	h := newSMTPHarness(t, back, store, true)
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}
	if final := submitToLocal(t, h); replyCode(final) != "250" {
		t.Fatalf("DATA = %q", final)
	}
	assertQueuedAsSubmitted(t, back, store)
}

// TestSMTP_AppPasswordSendsToALocalMailbox: the same with an app password (the
// E2E stage 14 case, which DMARC refused with 550 5.7.1).
func TestSMTP_AppPasswordSendsToALocalMailbox(t *testing.T) {
	store := newMockStore()
	h, back := appPasswordHarness(t, store)
	back.mu.Lock()
	back.local["bob@example.com"] = true
	back.mu.Unlock()
	if final := submitToLocal(t, h); replyCode(final) != "250" {
		t.Fatalf("DATA = %q", final)
	}
	assertQueuedAsSubmitted(t, back, store)
}

// TestSMTP_SubmissionQueuedForEachLocalRecipient: two local recipients get one
// queue row each, and share one Sent reference.
func TestSMTP_SubmissionQueuedForEachLocalRecipient(t *testing.T) {
	back := newMockBackend()
	back.local["bob@example.com"] = true
	back.local["carol@example.com"] = true
	store := newMockStore()
	h := newSMTPHarness(t, back, store, true)
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}
	h.cmd("MAIL FROM:<alice@example.com>")
	h.cmd("RCPT TO:<bob@example.com>")
	h.cmd("RCPT TO:<carol@example.com>")
	if final := h.dataBody(localSubmissionBody); replyCode(final) != "250" {
		t.Fatalf("DATA = %q", final)
	}
	if got := back.deliveredTo(); len(got) != 0 {
		t.Errorf("delivered directly to %v, want nothing", got)
	}
	queued := store.enqueuedMessages()
	if len(queued) != 2 || queued[0].Recipient != "bob@example.com" || queued[1].Recipient != "carol@example.com" {
		t.Fatalf("queued %+v, want one row for each recipient", queued)
	}
	if queued[0].MessageID == nil || queued[1].MessageID == nil || *queued[0].MessageID != *queued[1].MessageID {
		t.Errorf("queue rows name Sent copies %v and %v, want one", queued[0].MessageID, queued[1].MessageID)
	}
}

// TestSMTP_SubmissionWithTheAPIDownIsStillQueued: where a submission goes no
// longer depends on the API's answer about a recipient, so DATA keeps nothing
// from the queue once RCPT accepted it. A key that cannot sign is the queue
// worker's to defer: internal/api's TestLocalSendThatCannotBeSigned.
func TestSMTP_SubmissionWithTheAPIDownIsStillQueued(t *testing.T) {
	back := newMockBackend()
	back.local["bob@example.com"] = true
	store := newMockStore()
	h := newSMTPHarness(t, back, store, true)
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}
	h.cmd("MAIL FROM:<alice@example.com>")
	h.cmd("RCPT TO:<bob@example.com>")
	back.mu.Lock()
	back.checkErr["bob@example.com"] = true
	back.mu.Unlock()
	if final := h.dataBody(localSubmissionBody); replyCode(final) != "250" {
		t.Fatalf("DATA = %q", final)
	}
	assertQueuedAsSubmitted(t, back, store)
}

// TestSMTP_InboundToALocalMailboxIsNotPrepared: mail from another server keeps
// its client IP and HELO for SPF, and is delivered as received.
func TestSMTP_InboundToALocalMailboxIsNotPrepared(t *testing.T) {
	back := newMockBackend()
	back.local["bob@example.com"] = true
	store := newMockStore()
	h := newSMTPHarness(t, back, store, false) // inbound MX
	h.ehlo()
	h.cmd("MAIL FROM:<sender@remote.test>")
	h.cmd("RCPT TO:<bob@example.com>")
	if final := h.dataBody("From: sender@remote.test\r\nTo: bob@example.com\r\nSubject: Hi\r\n\r\nHello.\r\n"); replyCode(final) != "250" {
		t.Fatalf("DATA = %q", final)
	}
	req := back.lastDeliverReq()
	if req == nil {
		t.Fatal("nothing delivered")
	}
	if req.ClientIP == "" || req.HeloName == "" {
		t.Errorf("inbound delivered with client IP %q and HELO %q, want both", req.ClientIP, req.HeloName)
	}
	if strings.HasPrefix(string(req.RawMessage), "DKIM-Signature:") {
		t.Error("inbound mail signed")
	}
}
