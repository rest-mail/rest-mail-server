package smtp

import (
	"testing"
	"time"
)

// waitClosed waits for back to have closed want sessions: the server calls
// Logout after the connection's goroutine sees QUIT or the drop.
func waitClosed(t *testing.T, back *mockBackend, want int) (opened, closed []string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		opened, closed = back.sessions()
		if len(closed) >= want || time.Now().After(deadline) {
			return opened, closed
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A submission connection that signs in gets its own session with the API
// (#1586), opened once at AUTH and closed at QUIT.
func TestSMTPSubmissionSessionOpenedAtAuthClosedAtQuit(t *testing.T) {
	store := newMockStore()
	h, back := appPasswordHarness(t, store)
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	h.cmd("RSET")
	if opened, closed := back.sessions(); len(opened) != 1 || len(closed) != 0 {
		t.Fatalf("before QUIT: opened %v, closed %v; want one open session", opened, closed)
	}
	if r := h.cmd("QUIT"); replyCode(r) != "221" {
		t.Fatalf("QUIT = %q", r)
	}
	opened, closed := waitClosed(t, back, 1)
	if len(closed) != 1 || closed[0] != opened[0] {
		t.Fatalf("after QUIT: opened %v, closed %v; want the one session closed", opened, closed)
	}
}

// A submission connection that drops without QUIT still closes its session.
func TestSMTPSubmissionSessionClosedAtDisconnect(t *testing.T) {
	store := newMockStore()
	h, back := appPasswordHarness(t, store)
	_ = h.conn.Close()
	opened, closed := waitClosed(t, back, 1)
	if len(opened) != 1 || len(closed) != 1 || closed[0] != opened[0] {
		t.Fatalf("after the drop: opened %v, closed %v; want the one session closed", opened, closed)
	}
}

// Revoking the credential ends the submission connection's sign-in and its
// session at the next message.
func TestSMTPSubmissionRevokedCredentialClosesItsSession(t *testing.T) {
	store := newMockStore()
	h, back := appPasswordHarness(t, store)
	store.mu.Lock()
	store.unusable = map[uint]bool{7: true}
	store.mu.Unlock()
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "530" {
		t.Fatalf("MAIL FROM after revoking = %q, want 530", r)
	}
	if opened, closed := back.sessions(); len(opened) != 1 || len(closed) != 1 {
		t.Fatalf("after revoking: opened %v, closed %v; want the session closed", opened, closed)
	}
}

// Inbound delivery (no AUTH) opens no session: it acts for no account.
func TestSMTPInboundOpensNoSession(t *testing.T) {
	store := newMockStore()
	back := newMockBackend()
	back.local["bob@example.com"] = true
	h := newSMTPHarness(t, back, store, false)
	h.ehlo()
	if r := h.cmd("MAIL FROM:<someone@remote.example>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<bob@example.com>"); replyCode(r) != "250" {
		t.Fatalf("RCPT TO = %q", r)
	}
	h.cmd("QUIT")
	if opened, _ := back.sessions(); len(opened) != 0 {
		t.Fatalf("inbound delivery opened sessions: %v", opened)
	}
}
