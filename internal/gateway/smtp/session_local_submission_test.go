package smtp

import (
	"errors"
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

// assertDeliveredAsSubmitted checks that the local copy of a submission was
// prepared like its remote copies, and carries no client for the inbound
// checks to judge. Delivered with the submitting client's address, the
// domain's SPF record failed it, and DMARC p=reject refused it (550).
func assertDeliveredAsSubmitted(t *testing.T, back *mockBackend) {
	t.Helper()
	req := back.lastDeliverReq()
	if req == nil {
		t.Fatal("nothing delivered to the local mailbox")
	}
	if req.ClientIP != "" || req.HeloName != "" {
		t.Errorf("delivered with client IP %q and HELO %q, want neither: the submitting client is not a mail server for SPF to judge", req.ClientIP, req.HeloName)
	}
	raw := string(req.RawMessage)
	if !strings.HasPrefix(raw, mockSignature) {
		t.Errorf("delivered unsigned; message begins %q", head(raw))
	}
	if strings.Contains(raw, "Bcc:") {
		t.Errorf("delivered with its Bcc header:\n%s", raw)
	}
}

// TestSMTP_SubmissionToALocalMailbox: a message submitted with the mailbox's
// password, to a mailbox on this server, is delivered as one sent from the
// webmail is: signed, Bcc removed, with no client IP or HELO.
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
	assertDeliveredAsSubmitted(t, back)
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
	assertDeliveredAsSubmitted(t, back)
}

// TestSMTP_SubmissionPreparedOnceForAllLocalRecipients: two local recipients
// get the same prepared copy, signed once.
func TestSMTP_SubmissionPreparedOnceForAllLocalRecipients(t *testing.T) {
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
	if got := back.deliveredTo(); len(got) != 2 {
		t.Fatalf("delivered to %v, want both", got)
	}
	if n := store.preparedCount(); n != 1 {
		t.Errorf("prepared %d times, want once", n)
	}
}

// TestSMTP_SubmissionThatCannotBeSignedIsDeferred: when the sender domain's
// key cannot be loaded, the local copy is deferred (451), not delivered
// unsigned, as the queue worker does with the remote copies.
func TestSMTP_SubmissionThatCannotBeSignedIsDeferred(t *testing.T) {
	back := newMockBackend()
	back.local["bob@example.com"] = true
	store := newMockStore()
	store.prepareErr = errors.New("dkim key undecryptable")
	h := newSMTPHarness(t, back, store, true)
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}
	if final := submitToLocal(t, h); replyCode(final) != "451" {
		t.Fatalf("DATA = %q, want 451", final)
	}
	if got := back.deliveredTo(); len(got) != 0 {
		t.Errorf("delivered to %v, want nothing", got)
	}
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
	if n := store.preparedCount(); n != 0 {
		t.Errorf("inbound mail prepared %d times, want never", n)
	}
	if strings.HasPrefix(string(req.RawMessage), mockSignature) {
		t.Error("inbound mail signed")
	}
}
