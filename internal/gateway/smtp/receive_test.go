package smtp

import (
	"strings"
	"testing"
)

// CheckMailbox's negative answer covers both absent identities and accounts
// without receive permission. Neither can reach DATA or disclose its reason.
func TestSMTPReceiveGateUsesUnknownRecipientReply(t *testing.T) {
	back := newMockBackend()
	back.local["receiver@local.test"] = true
	h := newSMTPHarness(t, back, newMockStore(), false)
	h.ehlo()
	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatal(r)
	}
	if r := h.cmd("RCPT TO:<receiver@local.test>"); replyCode(r) != "250" {
		t.Fatal(r)
	}
	denied := h.cmd("RCPT TO:<sign-in-only@local.test>")
	unknown := h.cmd("RCPT TO:<unknown@local.test>")
	if denied != unknown || !strings.HasPrefix(denied, "550 5.1.1 ") || !strings.Contains(denied, "No such user") {
		t.Fatalf("denied=%q unknown=%q", denied, unknown)
	}
	if final := h.dataBody(testBody); replyCode(final) != "250" {
		t.Fatal(final)
	}
	if got := back.deliveredTo(); len(got) != 1 || got[0] != "receiver@local.test" {
		t.Fatalf("delivered=%v", got)
	}
}

func TestSMTPReceiveRevokedAfterRCPTNeverRelaysInboundMail(t *testing.T) {
	back := newMockBackend()
	address := "receiver@local.test"
	back.local[address] = true
	store := newMockStore()
	h := newSMTPHarness(t, back, store, false)
	h.ehlo()
	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatal(r)
	}
	if r := h.cmd("RCPT TO:<" + address + ">"); replyCode(r) != "250" {
		t.Fatal(r)
	}
	back.mu.Lock()
	back.local[address] = false
	back.mu.Unlock()
	if final := h.dataBody(testBody); !strings.HasPrefix(final, "550 5.1.1 ") {
		t.Fatalf("revoked recipient at DATA: %q", final)
	}
	if got := store.queued(); len(got) != 0 {
		t.Fatalf("inbound message was relayed: %+v", got)
	}
	if got := back.deliveredTo(); len(got) != 0 {
		t.Fatalf("delivered: %v", got)
	}
}
