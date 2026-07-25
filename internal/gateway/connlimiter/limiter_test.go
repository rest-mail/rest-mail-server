package connlimiter

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/restmail/restmail/internal/metrics"
)

// TestMetricsWiring verifies that, once a protocol is tagged, Accept/Release
// move the active_connections gauge and RecordAuthFail bumps the auth_failures
// counter. A unique protocol label keeps this isolated from other tests sharing
// the process-wide default registry.
func TestMetricsWiring(t *testing.T) {
	const proto = "connlimiter-test"
	l := New(Config{MaxPerIP: 5, MaxGlobal: 10})
	l.SetProtocol(proto)

	gauge := metrics.ActiveConnections.WithLabelValues(proto)
	authCtr := metrics.AuthFailures.WithLabelValues(proto)

	startGauge := testutil.ToFloat64(gauge)
	startAuth := testutil.ToFloat64(authCtr)

	if !l.Accept("1.2.3.4") {
		t.Fatal("expected Accept to succeed")
	}
	if !l.Accept("5.6.7.8") {
		t.Fatal("expected Accept to succeed")
	}
	if got := testutil.ToFloat64(gauge) - startGauge; got != 2 {
		t.Errorf("active_connections delta = %v, want 2", got)
	}

	l.Release("1.2.3.4")
	if got := testutil.ToFloat64(gauge) - startGauge; got != 1 {
		t.Errorf("active_connections delta after release = %v, want 1", got)
	}

	l.RecordAuthFail("1.2.3.4")
	l.RecordAuthFail("1.2.3.4")
	if got := testutil.ToFloat64(authCtr) - startAuth; got != 2 {
		t.Errorf("auth_failures delta = %v, want 2", got)
	}
}

// TestMetricsNotEmittedWithoutProtocol ensures a limiter with no protocol tag
// does not emit a metric under an empty label (which would pollute the
// registry). It exercises the guard by asserting the empty-label series stays
// flat across Accept/Release/RecordAuthFail.
func TestMetricsNotEmittedWithoutProtocol(t *testing.T) {
	l := New(Config{MaxPerIP: 5, MaxGlobal: 10})

	before := testutil.ToFloat64(metrics.ActiveConnections.WithLabelValues(""))
	beforeAuth := testutil.ToFloat64(metrics.AuthFailures.WithLabelValues(""))

	l.Accept("1.2.3.4")
	l.Release("1.2.3.4")
	l.RecordAuthFail("1.2.3.4")

	if got := testutil.ToFloat64(metrics.ActiveConnections.WithLabelValues("")); got != before {
		t.Errorf("empty-label active_connections changed: %v -> %v", before, got)
	}
	if got := testutil.ToFloat64(metrics.AuthFailures.WithLabelValues("")); got != beforeAuth {
		t.Errorf("empty-label auth_failures changed: %v -> %v", beforeAuth, got)
	}
}

func TestAcceptRelease(t *testing.T) {
	l := New(Config{MaxPerIP: 2, MaxGlobal: 5})

	if !l.Accept("1.2.3.4") {
		t.Fatal("expected Accept to succeed")
	}
	if !l.Accept("1.2.3.4") {
		t.Fatal("expected second Accept to succeed")
	}
	if l.Accept("1.2.3.4") {
		t.Fatal("expected third Accept from same IP to be rejected")
	}
	if !l.Accept("5.6.7.8") {
		t.Fatal("expected Accept from different IP to succeed")
	}

	l.Release("1.2.3.4")
	if !l.Accept("1.2.3.4") {
		t.Fatal("expected Accept after Release to succeed")
	}
}

func TestGlobalLimit(t *testing.T) {
	l := New(Config{MaxPerIP: 100, MaxGlobal: 3})

	l.Accept("1.1.1.1")
	l.Accept("2.2.2.2")
	l.Accept("3.3.3.3")

	if l.Accept("4.4.4.4") {
		t.Fatal("expected global limit to reject connection")
	}

	l.Release("1.1.1.1")
	if !l.Accept("4.4.4.4") {
		t.Fatal("expected Accept after global Release")
	}
}

func TestAuthBan(t *testing.T) {
	l := New(Config{
		MaxPerIP:        100,
		MaxGlobal:       100,
		AuthMaxFails:    3,
		AuthBanWindow:   10 * time.Second,
		AuthBanDuration: 1 * time.Second,
	})

	if l.IsBanned("1.2.3.4") {
		t.Fatal("should not be banned initially")
	}

	l.RecordAuthFail("1.2.3.4")
	l.RecordAuthFail("1.2.3.4")
	if l.IsBanned("1.2.3.4") {
		t.Fatal("should not be banned after 2 failures")
	}

	l.RecordAuthFail("1.2.3.4")
	if !l.IsBanned("1.2.3.4") {
		t.Fatal("should be banned after 3 failures")
	}

	time.Sleep(1100 * time.Millisecond)
	if l.IsBanned("1.2.3.4") {
		t.Fatal("ban should have expired")
	}
}

// TestAuthBanPerAccountAcrossIPs reproduces the distributed brute-force from
// issue #180: an attacker rotates source IPs so no single IP ever reaches the
// per-IP threshold, yet all the failures target ONE account. The per-IP tracker
// must stay silent while the per-account tracker bans the account.
func TestAuthBanPerAccountAcrossIPs(t *testing.T) {
	l := New(Config{
		MaxPerIP:        100,
		MaxGlobal:       100,
		AuthMaxFails:    3,
		AuthBanWindow:   10 * time.Second,
		AuthBanDuration: 1 * time.Second,
	})

	const victim = "victim@example.com"
	// Three failures, each from a DIFFERENT source IP (one failure per IP — well
	// under the per-IP threshold), all against the same account.
	ips := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
	for _, ip := range ips {
		l.RecordAuthFail(ip)         // per-IP, as the gateway records it
		l.RecordAuthFailUser(victim) // per-account, the new dimension
	}

	// No single IP crossed the per-IP threshold, so none is banned.
	for _, ip := range ips {
		if l.IsBanned(ip) {
			t.Fatalf("IP %s should NOT be banned (only 1 failure)", ip)
		}
	}
	// The account, having seen 3 failures across those IPs, IS banned.
	if !l.IsUserBanned(victim) {
		t.Fatal("account should be banned after 3 failures across rotating IPs")
	}

	// A fresh, never-seen IP attempting the banned account is still stopped by the
	// per-account ban (the hard-stop the gateways consult before calling Login).
	if l.IsBanned("10.0.0.99") {
		t.Fatal("fresh IP should not be per-IP banned")
	}
	if !l.IsUserBanned(victim) {
		t.Fatal("account ban must apply regardless of source IP")
	}

	// The account ban expires like the per-IP one.
	time.Sleep(1100 * time.Millisecond)
	if l.IsUserBanned(victim) {
		t.Fatal("account ban should have expired")
	}
}

// TestAuthBanPerAccountCaseInsensitive verifies the account key is normalized so
// mixed-case/whitespace variants of one login count as a single principal and an
// empty username is ignored.
func TestAuthBanPerAccountCaseInsensitive(t *testing.T) {
	l := New(Config{AuthMaxFails: 3, AuthBanWindow: 10 * time.Second, AuthBanDuration: time.Minute})

	l.RecordAuthFailUser("Victim@Example.com")
	l.RecordAuthFailUser("victim@example.com")
	l.RecordAuthFailUser("  VICTIM@EXAMPLE.COM  ")
	if !l.IsUserBanned("victim@example.com") {
		t.Fatal("case/whitespace variants of one account must aggregate into one ban")
	}

	// Empty username is a no-op, never banned.
	l.RecordAuthFailUser("")
	l.RecordAuthFailUser("")
	l.RecordAuthFailUser("")
	if l.IsUserBanned("") {
		t.Fatal("empty account key must never be banned")
	}
}

// TestResetAuthUser confirms a successful auth clears the per-account failure
// history, mirroring ResetAuth for IPs.
func TestResetAuthUser(t *testing.T) {
	l := New(Config{AuthMaxFails: 3, AuthBanWindow: 10 * time.Second, AuthBanDuration: time.Minute})

	l.RecordAuthFailUser("u@example.com")
	l.RecordAuthFailUser("u@example.com")
	l.ResetAuthUser("u@example.com")
	l.RecordAuthFailUser("u@example.com")

	if l.IsUserBanned("u@example.com") {
		t.Fatal("should not be banned after reset + 1 failure")
	}
}

func TestResetAuth(t *testing.T) {
	l := New(Config{
		MaxPerIP:        100,
		MaxGlobal:       100,
		AuthMaxFails:    3,
		AuthBanWindow:   10 * time.Second,
		AuthBanDuration: 30 * time.Second,
	})

	l.RecordAuthFail("1.2.3.4")
	l.RecordAuthFail("1.2.3.4")
	l.ResetAuth("1.2.3.4")
	l.RecordAuthFail("1.2.3.4")

	if l.IsBanned("1.2.3.4") {
		t.Fatal("should not be banned after reset + 1 failure")
	}
}
