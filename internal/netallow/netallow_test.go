package netallow

import (
	"net"
	"net/http"
	"testing"
)

func TestAllowlist(t *testing.T) {
	// One valid CIDR, one bare IP, one junk entry (must be dropped, not widen).
	a := New("test", []string{"10.0.0.0/8", "192.168.1.5", "not-a-cidr", "  "})

	cases := []struct {
		ip   string
		want bool
	}{
		{"10.1.2.3", true},     // inside CIDR
		{"192.168.1.5", true},  // bare-IP /32
		{"192.168.1.6", false}, // outside the /32
		{"203.0.113.1", false}, // public
	}
	for _, tc := range cases {
		if got := a.Allowed(net.ParseIP(tc.ip)); got != tc.want {
			t.Errorf("Allowed(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}

	if a.Allowed(nil) {
		t.Error("Allowed(nil) = true, want false (fail-closed)")
	}
}

func TestEmptyAllowlistDeniesAll(t *testing.T) {
	a := New("test", nil)
	if !a.Empty() {
		t.Fatal("New(nil) not Empty")
	}
	if a.Allowed(net.ParseIP("127.0.0.1")) {
		t.Error("empty allowlist allowed loopback; want deny-all")
	}
}

func TestDefaultInternalCIDRsCoverInternalDenyPublic(t *testing.T) {
	a := New("test", DefaultInternalCIDRs)
	for _, ip := range []string{"127.0.0.1", "10.4.5.6", "172.16.9.9", "192.168.0.2", "::1"} {
		if !a.Allowed(net.ParseIP(ip)) {
			t.Errorf("default allowlist denied internal %s", ip)
		}
	}
	for _, ip := range []string{"203.0.113.5", "8.8.8.8"} {
		if a.Allowed(net.ParseIP(ip)) {
			t.Errorf("default allowlist allowed public %s", ip)
		}
	}
}

func TestRealClientIP(t *testing.T) {
	trusted := New("test", []string{"10.0.0.0/8"})

	newReq := func(remote, xff string) *http.Request {
		r := &http.Request{RemoteAddr: remote, Header: http.Header{}}
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	t.Run("direct peer when no forwarded header", func(t *testing.T) {
		ip := RealClientIP(newReq("203.0.113.7:5000", ""), trusted)
		if ip.String() != "203.0.113.7" {
			t.Fatalf("got %v, want 203.0.113.7", ip)
		}
	})

	t.Run("forwarded header ignored from untrusted peer", func(t *testing.T) {
		// Untrusted public peer sets a spoofed internal XFF — must be ignored.
		ip := RealClientIP(newReq("203.0.113.7:5000", "127.0.0.1"), trusted)
		if ip.String() != "203.0.113.7" {
			t.Fatalf("spoofed XFF honored: got %v, want 203.0.113.7", ip)
		}
	})

	t.Run("forwarded header honored from trusted proxy", func(t *testing.T) {
		// Direct peer is a trusted proxy; XFF's right-most non-proxy is the origin.
		ip := RealClientIP(newReq("10.0.0.9:5000", "203.0.113.9, 10.0.0.9"), trusted)
		if ip.String() != "203.0.113.9" {
			t.Fatalf("got %v, want 203.0.113.9", ip)
		}
	})

	t.Run("trusted proxy with no forwarded header falls back to peer", func(t *testing.T) {
		ip := RealClientIP(newReq("10.0.0.9:5000", ""), trusted)
		if ip.String() != "10.0.0.9" {
			t.Fatalf("got %v, want 10.0.0.9", ip)
		}
	})

	t.Run("undeterminable peer returns nil", func(t *testing.T) {
		if ip := RealClientIP(newReq("garbage", ""), trusted); ip != nil {
			t.Fatalf("got %v, want nil", ip)
		}
	})

	t.Run("nil trusted proxies never honors forwarded header", func(t *testing.T) {
		ip := RealClientIP(newReq("10.0.0.9:5000", "203.0.113.9"), nil)
		if ip.String() != "10.0.0.9" {
			t.Fatalf("got %v, want 10.0.0.9 (peer)", ip)
		}
	})
}
