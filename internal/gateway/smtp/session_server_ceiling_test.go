package smtp

import "testing"

// denySend makes the store refuse mail:send to the account, as removing it
// from the server's capability ceiling does, or allows it again.
func denySend(store *mockStore, accountID uint, denied bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.sendDenied = map[uint]bool{accountID: denied}
}

// sendsFromOwnAddress submits one message as alice and reports the MAIL FROM
// reply code.
func sendsFromOwnAddress(t *testing.T, h *smtpHarness) string {
	t.Helper()
	r := h.cmd("MAIL FROM:<alice@example.com>")
	h.cmd("RSET")
	return replyCode(r)
}

// TestSMTP_ServerCeilingStopsAnOpenSession: a submission session signed in
// while the server allowed sending keeps sending while it stays allowed, is
// refused at its next MAIL FROM once the ceiling removes mail:send, and sends
// again once it is restored. Both kinds of credential are held to it.
func TestSMTP_ServerCeilingStopsAnOpenSession(t *testing.T) {
	for _, kind := range []string{"password", "app password"} {
		t.Run(kind, func(t *testing.T) {
			store := newMockStore()
			back := newMockBackend()
			secret := back.pass
			if kind == "app password" {
				back.appPassword, back.credentialID = testSMTPAppPassword, 7
				secret = testSMTPAppPassword
			}
			h := newSMTPHarness(t, back, store, true)
			h.ehlo()
			if r := h.authPlain(back.user, secret); replyCode(r) != "235" {
				t.Fatalf("AUTH = %q", r)
			}
			if code := sendsFromOwnAddress(t, h); code != "250" {
				t.Fatalf("MAIL FROM while allowed = %s, want 250", code)
			}
			if code := sendsFromOwnAddress(t, h); code != "250" {
				t.Fatalf("MAIL FROM while still allowed = %s, want 250", code)
			}

			denySend(store, back.accountID, true)
			if code := sendsFromOwnAddress(t, h); code != "553" {
				t.Fatalf("MAIL FROM after the ceiling removed mail:send = %s, want 553", code)
			}

			denySend(store, back.accountID, false)
			if code := sendsFromOwnAddress(t, h); code != "250" {
				t.Fatalf("MAIL FROM after mail:send was restored = %s, want 250", code)
			}
		})
	}
}

// TestSMTP_ServerCeilingHoldsTheFromHeader: the ceiling is read again for the
// From header, so a change between MAIL FROM and DATA is not missed.
func TestSMTP_ServerCeilingHoldsTheFromHeader(t *testing.T) {
	store := newMockStore()
	back := newMockBackend()
	h := newSMTPHarness(t, back, store, true)
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
	denySend(store, back.accountID, true)
	if final := h.dataBody("Subject: Hi\r\nFrom: Alice <alice@example.com>\r\n\r\nHello.\r\n"); replyCode(final) != "550" {
		t.Fatalf("DATA after the ceiling removed mail:send = %q, want 550", final)
	}
	if got := len(store.queued()); got != 0 {
		t.Errorf("queued %d, want none", got)
	}
}
