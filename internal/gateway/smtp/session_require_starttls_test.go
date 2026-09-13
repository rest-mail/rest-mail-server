package smtp

import (
	"strings"
	"testing"
)

// Port 25 takes mail in the clear, because a publicly-referenced SMTP server has to.
//
// RFC 3207 §4: "A publicly-referenced SMTP server MUST NOT require use of the STARTTLS
// extension in order to deliver mail locally." Refusing the transaction until STARTTLS
// had been used made rest-mail unreachable for any sender that does not upgrade, which
// is a delivery failure, not a security win (issue #292). STARTTLS is still advertised
// on every cleartext session, the arrival is recorded as plaintext, and penalising
// plaintext is the inbound pipeline's job.
//
// This test asserted the opposite until #292; submission stays TLS-only, below.
func TestSMTP_InboundAcceptsPlaintextMailFrom(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()

	h := newCleartextSMTPHarness(t, back, store, false) // inbound relay, as port 25
	h.ehlo()

	if r := h.cmd("MAIL FROM:<sender@peer.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM on a cleartext port 25 session = %q, want 250", r)
	}
}

// Submission is not the public MX: it is authenticated client traffic, where the client
// is ours and can be required to use TLS. It stays TLS-only, and says how to upgrade.
func TestSMTP_SubmissionStillRefusesPlaintext(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()

	h := newCleartextSMTPHarness(t, back, store, true) // submission, not upgraded
	h.ehlo()

	r := h.cmd("MAIL FROM:<sender@peer.test>")
	if replyCode(r) != "530" {
		t.Fatalf("MAIL FROM on a cleartext submission session = %q, want 530", r)
	}
	// The reply has to name the remedy: a client reading this must know to issue
	// STARTTLS rather than that it has been blocked for some unstated reason.
	if !strings.Contains(strings.ToUpper(r), "STARTTLS") {
		t.Errorf("the refusal does not mention STARTTLS: %q", r)
	}
	if got := len(store.queued()); got != 0 {
		t.Errorf("nothing may be queued from a refused cleartext session, queued %d", got)
	}
}

// STARTTLS must still be advertised — a peer cannot upgrade a session whose EHLO does
// not offer it, and refusing MAIL FROM without advertising the way forward would make
// rest-mail simply unreachable.
func TestSMTP_InboundAdvertisesSTARTTLS(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()

	h := newCleartextSMTPHarness(t, back, store, false)
	lines, final := h.readReplyAfter("EHLO peer.test")
	if replyCode(final) != "250" {
		t.Fatalf("EHLO = %q", final)
	}
	if !strings.Contains(strings.ToUpper(strings.Join(append(lines, final), "\n")), "STARTTLS") {
		t.Errorf("EHLO does not advertise STARTTLS:\n%s", strings.Join(append(lines, final), "\n"))
	}
}

// The commands needed to GET to STARTTLS keep working; the gate is on the transaction,
// not on the conversation.
func TestSMTP_CleartextPreambleStillWorks(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()

	h := newCleartextSMTPHarness(t, back, store, false)
	h.ehlo()
	for _, c := range []struct {
		cmd  string
		want string
	}{
		{"NOOP", "250"},
		{"RSET", "250"},
	} {
		if r := h.cmd(c.cmd); replyCode(r) != c.want {
			t.Errorf("%s = %q, want %s", c.cmd, r, c.want)
		}
	}
}
