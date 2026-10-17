package smtp

import (
	"errors"
	"testing"
)

const testSMTPAppPassword = "rm_AAAAAAAAAAAA_secretsecretsecretsecretsecretsecretsecretsec"

// appPasswordHarness signs in over submission with an app password, as
// credential 7.
func appPasswordHarness(t *testing.T, store *mockStore) (*smtpHarness, *mockBackend) {
	t.Helper()
	back := newMockBackend()
	back.appPassword, back.credentialID = testSMTPAppPassword, 7
	h := newSMTPHarness(t, back, store, true) // submission
	h.ehlo()
	if r := h.authPlain(back.user, testSMTPAppPassword); replyCode(r) != "235" {
		t.Fatalf("AUTH with an app password = %q", r)
	}
	return h, back
}

// TestSMTP_AppPasswordSends: a mail client that signs in with an app password
// sends as its mailbox, like one that signs in with the mailbox's password.
func TestSMTP_AppPasswordSends(t *testing.T) {
	store := newMockStore()
	h, _ := appPasswordHarness(t, store)
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<carol@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if final := h.dataBody("Subject: Hi\r\nFrom: Alice <alice@example.com>\r\n\r\nHello.\r\n"); replyCode(final) != "250" {
		t.Fatalf("DATA = %q", final)
	}
	if got := len(store.queued()); got != 1 {
		t.Errorf("queued %d, want 1", got)
	}
}

// TestSMTP_AppPasswordNeverSendsAsALinkedMailbox: an app password's scope is
// its own mailbox, so it may not send as a mailbox its account has linked,
// although the mailbox's password may.
func TestSMTP_AppPasswordNeverSendsAsALinkedMailbox(t *testing.T) {
	store := newMockStore()
	store.authorized["linked@example.com"] = true
	h, _ := appPasswordHarness(t, store)
	if r := h.cmd("MAIL FROM:<linked@example.com>"); replyCode(r) != "553" {
		t.Fatalf("MAIL FROM a linked mailbox with an app password = %q, want 553", r)
	}

	// The From header is held to the same.
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<carol@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if final := h.dataBody("Subject: Hi\r\nFrom: <linked@example.com>\r\n\r\nHello.\r\n"); replyCode(final) != "550" {
		t.Fatalf("a linked From header with an app password = %q, want 550", final)
	}
	if got := len(store.queued()); got != 0 {
		t.Errorf("queued %d, want none", got)
	}
}

// TestSMTP_RevokedAppPasswordStopsSending: revoking an app password stops a
// session already signed in with it at its next message.
func TestSMTP_RevokedAppPasswordStopsSending(t *testing.T) {
	store := newMockStore()
	h, _ := appPasswordHarness(t, store)
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM before revoking = %q", r)
	}
	h.cmd("RSET")

	store.mu.Lock()
	store.unusable = map[uint]bool{7: true}
	store.mu.Unlock()
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "530" {
		t.Fatalf("MAIL FROM after revoking = %q, want 530", r)
	}
	// The session is no longer signed in.
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "530" {
		t.Fatalf("MAIL FROM again = %q, want 530", r)
	}
}

// TestSMTP_AppPasswordCheckFailureIsTemporary: when the credential cannot be
// checked, the message is deferred, not refused.
func TestSMTP_AppPasswordCheckFailureIsTemporary(t *testing.T) {
	store := newMockStore()
	h, _ := appPasswordHarness(t, store)
	store.mu.Lock()
	store.credentialErr = errors.New("database down")
	store.mu.Unlock()
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "451" {
		t.Fatalf("MAIL FROM = %q, want 451", r)
	}
}

// TestSMTP_PasswordSessionIsNotCheckedAsACredential: the mailbox's own
// password signs in as before, with no credential to check.
func TestSMTP_PasswordSessionIsNotCheckedAsACredential(t *testing.T) {
	store := newMockStore()
	store.credentialErr = errors.New("must not be called")
	back := newMockBackend()
	h := newSMTPHarness(t, back, store, true)
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
}
