package rfctest

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/server"
)

type privateFailureBackend struct {
	reason string
	calls  int
}

func (b *privateFailureBackend) Authenticate(string, string) (imap.Mailbox, error) {
	b.calls++
	return nil, errors.New(b.reason)
}

type failureLimit struct {
	imap.NopLimiter
	failures, resets int
}

func (l *failureLimit) RecordAuthFail(string) { l.failures++ }
func (l *failureLimit) IsBanned(string) bool  { return l.failures >= 2 }
func (l *failureLimit) ResetAuth(string)      { l.resets++ }

func securityClient(t *testing.T, backend imap.Backend, limiter imap.Limiter) *client {
	t.Helper()
	peer, conn := net.Pipe()
	session := server.NewSession(conn, backend, "imap.test", nil, limiter)
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Handle()
	}()
	if err := peer.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, conn: peer, r: bufio.NewReader(peer), done: done}
	t.Cleanup(func() { _ = peer.Close(); <-done })
	c.line()
	return c
}

// Backend errors can distinguish a missing identity from a refused password;
// neither distinction, nor internal storage details, belongs on the wire.
// checklist: IMAP2-SEC-243 IMAP2-SEC-245
func TestAuthenticationFailureDetailsStayPrivate(t *testing.T) {
	for _, reason := range []string{"private: account does not exist", "private: password hash mismatch", "private: storage request failed"} {
		b := &privateFailureBackend{reason: reason}
		c := securityClient(t, b, imap.NopLimiter{})
		_, login := c.command("A1", "LOGIN nonexistent@example.test wrong-password")
		authenticate := c.authenticate("A1", "PLAIN", plainResponse("nonexistent@example.test", "wrong-password"))
		const expected = "A1 NO [AUTHENTICATIONFAILED] Invalid credentials"
		if login != expected || authenticate != expected || strings.Contains(login+authenticate, "private") {
			t.Fatalf("authentication refused with differing or private wire details: LOGIN=%q AUTHENTICATE=%q", login, authenticate)
		}
		if b.calls != 2 {
			t.Fatalf("backend authentication calls=%d, want2", b.calls)
		}
	}
}

// Both interactive command forms use the host's failure budget. Once that
// budget is exhausted, the connection closes instead of allowing more guesses.
// This does not claim account-level password-spraying detection by the engine.
// checklist: IMAP2-SEC-246
func TestAuthenticationFailureBudgetAppliesToBothForms(t *testing.T) {
	b := &privateFailureBackend{reason: "private: rejected"}
	limiter := &failureLimit{}
	c := securityClient(t, b, limiter)
	_, first := c.command("A1", "LOGIN nonexistent@example.test wrong-password")
	if first != "A1 NO [AUTHENTICATIONFAILED] Invalid credentials" || limiter.failures != 1 {
		t.Fatalf("first failure: %q, count=%d", first, limiter.failures)
	}
	second := c.authenticate("A2", "PLAIN", plainResponse("nonexistent@example.test", "wrong-password"))
	if second != "A2 NO Too many authentication failures" || limiter.failures != 2 || limiter.resets != 0 || b.calls != 2 {
		t.Fatalf("limit reply=%q failures=%d resets=%d backend=%d", second, limiter.failures, limiter.resets, b.calls)
	}
	if _, err := c.r.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("connection remains open after exhausted budget: %v", err)
	}
}
