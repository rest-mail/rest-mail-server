package queue

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// TestBlockedOutboundIP documents the outbound-delivery policy: unlike the
// webhook filter (which allows RFC1918 so it can reach sibling containers),
// outbound mail delivery blocks every non-public class, because a public MX or
// RESTMAIL endpoint should never resolve to one of them.
func TestBlockedOutboundIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},       // loopback
		{"::1", true},             // loopback v6
		{"169.254.169.254", true}, // cloud instance metadata (link-local)
		{"0.0.0.0", true},         // unspecified
		{"10.0.0.5", true},        // RFC1918 — blocked for outbound (unlike webhook filter)
		{"172.20.0.3", true},      // RFC1918
		{"192.168.1.10", true},    // RFC1918
		{"fd00::1", true},         // IPv6 unique-local
		{"8.8.8.8", false},        // public
		{"93.184.216.34", false},  // public
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test ip %q", c.ip)
		}
		if got := blockedOutboundIP(ip); got != c.blocked {
			t.Errorf("blockedOutboundIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}

// TestOutboundDialControl covers the dial-control hook that runs on the resolved
// IP:port (defeating DNS rebinding): default-deny refuses non-public addresses,
// and the explicit opt-in permits them.
func TestOutboundDialControl(t *testing.T) {
	deny := outboundDialControl(false)
	if err := deny("tcp", "169.254.169.254:80", nil); err == nil {
		t.Error("expected metadata IP to be blocked")
	}
	if err := deny("tcp", "127.0.0.1:25", nil); err == nil {
		t.Error("expected loopback to be blocked")
	}
	if err := deny("tcp", "10.1.2.3:25", nil); err == nil {
		t.Error("expected RFC1918 to be blocked for outbound delivery")
	}
	if err := deny("tcp", "8.8.8.8:25", nil); err != nil {
		t.Errorf("expected public IP to be allowed, got %v", err)
	}

	allow := outboundDialControl(true)
	if err := allow("tcp", "127.0.0.1:25", nil); err != nil {
		t.Errorf("opt-in should permit loopback, got %v", err)
	}
	if err := allow("tcp", "10.1.2.3:25", nil); err != nil {
		t.Errorf("opt-in should permit RFC1918, got %v", err)
	}
}

// TestOutboundGuardedDialer exercises the dialer used by the SMTP delivery path
// (tryRESTMAIL / deliverToHost): the guarded dial to a loopback listener is
// refused by default and permitted once the worker is opted in.
func TestOutboundGuardedDialer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	deny := &Worker{}
	if _, err := deny.newGuardedDialer(2 * time.Second).Dial("tcp", ln.Addr().String()); err == nil {
		t.Fatal("expected guarded dialer to refuse loopback SMTP dial")
	}

	allow := &Worker{allowPrivateDest: true}
	conn, err := allow.newGuardedDialer(2 * time.Second).Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("opt-in should allow loopback dial, got %v", err)
	}
	_ = conn.Close()
}

func testItem() models.OutboundQueue {
	return models.OutboundQueue{
		Sender:     "alice@example.com",
		Recipient:  "bob@example.net",
		RawMessage: "From: alice@example.com\r\nTo: bob@example.net\r\n\r\nhi\r\n",
	}
}

// TestDeliverRESTMAILHTTPSRefusesLoopbackHTTPS is the red-green guard for the
// RESTMAIL POST path. Before the fix, an attacker-advertised endpoint resolving
// to loopback (here an httptest TLS server on 127.0.0.1) was POSTed to and the
// call succeeded — blind SSRF. The guarded client must now refuse the connection.
// It uses only the pre-existing deliverRESTMAILHTTPS method and pre-existing
// worker fields, so it fails against the unpatched worker and passes after it.
func TestDeliverRESTMAILHTTPSRefusesLoopbackHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// tlsInsecure so the self-signed httptest cert is accepted — isolating the
	// SSRF guard as the reason the call must fail, not certificate verification.
	w := &Worker{sendDeadline: defaultSendDeadline, tlsInsecure: true}
	if err := w.deliverRESTMAILHTTPS(srv.URL, testItem(), false); err == nil {
		t.Fatal("expected RESTMAIL POST to a loopback endpoint to be refused")
	}
}

// TestDeliverRESTMAILHTTPSAllowsLoopbackWhenOptedIn proves the dev/testbed
// opt-in still permits delivery to a private/loopback endpoint (RESTMAIL between
// containers), so the guard does not break the local test stack.
func TestDeliverRESTMAILHTTPSAllowsLoopbackWhenOptedIn(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := &Worker{sendDeadline: defaultSendDeadline, tlsInsecure: true, allowPrivateDest: true}
	if err := w.deliverRESTMAILHTTPS(srv.URL, testItem(), false); err != nil {
		t.Fatalf("opt-in should permit loopback RESTMAIL delivery, got %v", err)
	}
}

// TestDeliverRESTMAILHTTPSRejectsCleartext verifies the raw message is never
// POSTed over a non-HTTPS scheme, even when private destinations are allowed —
// the endpoint URL is attacker-influenced (from an MX's RESTMAIL EHLO keyword).
func TestDeliverRESTMAILHTTPSRejectsCleartext(t *testing.T) {
	w := &Worker{sendDeadline: defaultSendDeadline, allowPrivateDest: true}
	err := w.deliverRESTMAILHTTPS("http://example.com", testItem(), false)
	if err == nil {
		t.Fatal("expected cleartext http RESTMAIL endpoint to be refused")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error should explain the https requirement, got %v", err)
	}
}
