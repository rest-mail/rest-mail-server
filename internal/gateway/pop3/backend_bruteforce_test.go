package pop3

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/connlimiter"
)

// TestAuthenticate_PerAccountHardStop verifies the POP3 backend throttles
// brute-force per ACCOUNT and, once the account is banned, rejects further
// attempts WITHOUT calling the auth API (issue #180). This ban is independent of
// source IP, so it catches a distributed attack the library's per-IP guard cannot.
func TestAuthenticate_PerAccountHardStop(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized) // definitive credential rejection
	}))
	t.Cleanup(srv.Close)

	lim := connlimiter.New(connlimiter.Config{
		AuthMaxFails:    3,
		AuthBanWindow:   time.Minute,
		AuthBanDuration: time.Minute,
	})
	b := NewBackend(apiclient.New(srv.URL), lim)

	const user = "victim@example.com"
	for i := 0; i < 3; i++ {
		if _, err := b.Authenticate(user, "wrong"); err == nil {
			t.Fatalf("attempt %d: expected auth failure", i+1)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("API login calls before ban = %d, want 3", got)
	}

	// Account is now banned: the next attempt is a hard stop — no new API call.
	if _, err := b.Authenticate(user, "wrong"); err == nil {
		t.Fatal("banned account should still fail auth")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("banned account still hit the auth API: calls = %d, want 3 (hard stop)", got)
	}
}

// TestAuthenticate_TransientErrorNotBanned confirms a transient API error (not a
// 401/403) does not accrue against the per-account ban, so an API outage cannot
// lock an account out.
func TestAuthenticate_TransientErrorNotBanned(t *testing.T) {
	var status int32 = http.StatusServiceUnavailable
	var okBody = `{"data":{"access_token":"t","user":{"id":1,"email":"victim@example.com"}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := int(atomic.LoadInt32(&status))
		if code == http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(okBody))
			return
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)

	lim := connlimiter.New(connlimiter.Config{
		AuthMaxFails:    3,
		AuthBanWindow:   time.Minute,
		AuthBanDuration: time.Minute,
	})
	b := NewBackend(apiclient.New(srv.URL), lim)

	const user = "victim@example.com"
	for i := 0; i < 6; i++ {
		if _, err := b.Authenticate(user, "pw"); err == nil {
			t.Fatalf("attempt %d during outage: expected failure", i+1)
		}
	}
	if lim.IsUserBanned(user) {
		t.Fatal("transient API errors must not ban the account")
	}

	// API recovers: authentication succeeds (never banned).
	atomic.StoreInt32(&status, http.StatusOK)
	if _, err := b.Authenticate(user, "pw"); err != nil {
		t.Fatalf("post-recovery auth failed: %v", err)
	}
}
