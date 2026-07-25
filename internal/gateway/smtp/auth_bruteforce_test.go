package smtp

import (
	"context"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// noTarpit disables the escalating sleep so brute-force tests run instantly; the
// ban/hard-stop logic under test is independent of the tarpit delay.
func noTarpit(s *Server) {
	s.tarpitSleep = func(context.Context, time.Duration) {}
}

// TestSMTP_AuthBanHardStop is the core regression for issue #180: once a client
// is banned for repeated auth failures, further AUTH attempts must be rejected
// WITHOUT calling the auth API. Before the fix the session called api.Login on
// every attempt and only changed the reply to 421, so a banned attacker kept
// hammering the backend.
func TestSMTP_AuthBanHardStop(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()
	h := newSMTPHarness(t, back, store, true, noTarpit)
	h.ehlo()

	// Default AuthMaxFails is 5. Attempts 1-4 fail with 535; the 5th trips the ban
	// and returns 421. All five reach api.Login (credentials are actually checked).
	for i := 0; i < 4; i++ {
		if r := h.authPlain(back.user, "wrong"); replyCode(r) != "535" {
			t.Fatalf("attempt %d = %q, want 535", i+1, r)
		}
	}
	if r := h.authPlain(back.user, "wrong"); replyCode(r) != "421" {
		t.Fatalf("5th attempt (ban trip) = %q, want 421", r)
	}
	if got := back.loginCallCount(); got != 5 {
		t.Fatalf("Login calls before ban = %d, want 5", got)
	}

	// The client is now banned: a 6th attempt must be a hard stop — 421 and NO new
	// api.Login call. On the buggy code this attempt still called Login (count 6).
	if r := h.authPlain(back.user, "wrong"); replyCode(r) != "421" {
		t.Fatalf("post-ban attempt = %q, want 421", r)
	}
	if got := back.loginCallCount(); got != 5 {
		t.Fatalf("Login called %d times: a banned client's AUTH was still processed (want 5)", got)
	}
}

// TestSMTP_BannedAccountRejectsCorrectPassword proves the ban is a true hard stop
// and is enforced per ACCOUNT: once the account is banned, even the CORRECT
// password is rejected without verification (api.Login is never called), so a
// lucky correct guess cannot lift the ban. This also demonstrates the per-account
// dimension — the ban is seeded independently of any source IP.
func TestSMTP_BannedAccountRejectsCorrectPassword(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()
	h := newSMTPHarness(t, back, store, true, noTarpit)
	h.ehlo()

	// Seed a per-account ban as if it had accrued from failures spread across many
	// source IPs (the distributed-attack case the per-IP tracker cannot see).
	for i := 0; i < 5; i++ {
		h.limiter.RecordAuthFailUser(back.user)
	}

	// Correct credentials, but the account is banned: expect 421 and no Login call.
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "421" {
		t.Fatalf("banned-account AUTH with correct password = %q, want 421", r)
	}
	if got := back.loginCallCount(); got != 0 {
		t.Fatalf("Login called %d times for a banned account (want 0 — must reject without verifying)", got)
	}
}

// TestSMTP_TransientAPIErrorNotBanned verifies that a transient API/network error
// (anything that is not a definitive 401/403) never accrues against the ban, so a
// brief API outage cannot lock out a legitimate client. Before the fix every
// Login error counted, so five outage responses banned the IP for the window.
func TestSMTP_TransientAPIErrorNotBanned(t *testing.T) {
	back := newMockBackend()
	back.loginErr = &apiclient.APIError{StatusCode: 503, Body: "service unavailable"}
	store := newMockStore()
	h := newSMTPHarness(t, back, store, true, noTarpit)
	h.ehlo()

	// Well past the ban threshold: each attempt hits the outage and must return a
	// temporary failure (454), never a ban (421).
	for i := 0; i < 8; i++ {
		if r := h.authPlain(back.user, back.pass); replyCode(r) != "454" {
			t.Fatalf("attempt %d during outage = %q, want 454 (transient, not counted)", i+1, r)
		}
	}

	// API recovers: the (never-banned) client authenticates successfully.
	back.mu.Lock()
	back.loginErr = nil
	back.mu.Unlock()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("post-recovery AUTH = %q, want 235 (client must not have been banned)", r)
	}
}
