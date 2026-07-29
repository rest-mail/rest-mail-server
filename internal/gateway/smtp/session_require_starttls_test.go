package smtp

import (
	"strings"
	"testing"
)

// Port 25 is the one listener that cannot be implicit TLS and still receive mail from
// other MTAs: relay begins in cleartext and upgrades. So it is the one place a cleartext
// session can get as far as a command, and the line is drawn at the message itself —
// STARTTLS is advertised, and nothing is accepted until it has been used.
//
// RED before this change: a peer could skip STARTTLS entirely and hand over a whole
// message in the clear, which is exactly the legacy behaviour rest-mail should not have.
func TestSMTP_InboundRequiresSTARTTLSBeforeMailFrom(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()

	h := newCleartextSMTPHarness(t, back, store, false) // inbound relay, as port 25
	h.ehlo()

	r := h.cmd("MAIL FROM:<sender@peer.test>")
	if replyCode(r) != "530" {
		t.Fatalf("MAIL FROM on a cleartext session = %q, want 530", r)
	}
	// The reply has to name the remedy: a peer reading this must know to issue
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
