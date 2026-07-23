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
