package smtp

import "testing"

// TestSMTP_FromHeaderSpoofingRejected is the red-green guard for #181: on the
// authenticated submission path the message From: header must belong to an
// identity the account is authorized for. Here the account authenticates as
// alice@example.com and uses an authorized envelope sender, but sets a From:
// header for ceo@example.com — an address it does not own. The transaction must
// be rejected (550) and nothing may be delivered or queued.
//
// RED before the fix: the From: header was never inspected, so the spoofed
// message was accepted (250) and enqueued for outbound relay.
func TestSMTP_FromHeaderSpoofingRejected(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()

	h := newSMTPHarness(t, back, store, true) // submission
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}
	// Envelope sender is the authenticated identity itself (accepted).
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<carol@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}

	// Header-level spoof: From: an address the account is not authorized for.
	spoofed := "Subject: Hi\r\nFrom: \"CEO\" <ceo@example.com>\r\n\r\nHello there.\r\n"
	final := h.dataBody(spoofed)
	if replyCode(final) != "550" {
		t.Fatalf("spoofed From must be rejected 550, got %q", final)
	}

	if got := len(store.queued()); got != 0 {
		t.Errorf("spoofed submission must enqueue nothing, queued %d", got)
	}
	if got := len(back.deliveredTo()); got != 0 {
		t.Errorf("spoofed submission must deliver nothing, delivered %v", back.deliveredTo())
	}
}

// TestSMTP_FromHeaderAuthorizedAccepted confirms the fix does not block
// legitimate submissions: a From: header matching the authenticated identity
// itself, and one matching a linked/authorized address, are both accepted.
func TestSMTP_FromHeaderAuthorizedAccepted(t *testing.T) {
	t.Run("self identity", func(t *testing.T) {
		back := newMockBackend()
		store := newMockStore()

		h := newSMTPHarness(t, back, store, true) // submission
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
		body := "Subject: Hi\r\nFrom: Alice <alice@example.com>\r\n\r\nHello there.\r\n"
		if final := h.dataBody(body); replyCode(final) != "250" {
			t.Fatalf("self From must be accepted, got %q", final)
		}
		if got := len(store.queued()); got != 1 {
			t.Errorf("authorized submission should enqueue 1, queued %d", got)
		}
	})

	t.Run("linked identity", func(t *testing.T) {
		back := newMockBackend()
		store := newMockStore()
		store.authorized["linked@example.com"] = true // an authorized linked address

		h := newSMTPHarness(t, back, store, true) // submission
		h.ehlo()
		if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
			t.Fatalf("AUTH = %q", r)
		}
		// Envelope sender is the linked (authorized) address.
		if r := h.cmd("MAIL FROM:<linked@example.com>"); replyCode(r) != "250" {
			t.Fatalf("MAIL FROM = %q", r)
		}
		if r := h.cmd("RCPT TO:<carol@remote.test>"); replyCode(r) != "250" {
			t.Fatalf("RCPT = %q", r)
		}
		body := "Subject: Hi\r\nFrom: Linked <linked@example.com>\r\n\r\nHello there.\r\n"
		if final := h.dataBody(body); replyCode(final) != "250" {
			t.Fatalf("linked From must be accepted, got %q", final)
		}
		if got := len(store.queued()); got != 1 {
			t.Errorf("authorized submission should enqueue 1, queued %d", got)
		}
	})
}
