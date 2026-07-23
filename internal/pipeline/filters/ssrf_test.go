package filters

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBlockedDialIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},           // loopback
		{"::1", true},                 // loopback (v6)
		{"169.254.169.254", true},     // AWS/GCP instance metadata
		{"0.0.0.0", true},             // unspecified
		{"10.0.0.5", false},           // RFC1918 — internal bridge, allowed
		{"172.20.0.3", false},         // RFC1918
		{"192.168.1.10", false},       // RFC1918
		{"8.8.8.8", false},            // public, allowed
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test ip %q", c.ip)
		}
		if got := blockedDialIP(ip); got != c.blocked {
			t.Errorf("blockedDialIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}

func TestGuardedDialControl(t *testing.T) {
	if err := guardedDialControl("tcp", "169.254.169.254:80", nil); err == nil {
		t.Error("expected metadata IP to be blocked")
	}
	if err := guardedDialControl("tcp", "127.0.0.1:8080", nil); err == nil {
		t.Error("expected loopback to be blocked")
	}
	if err := guardedDialControl("tcp", "10.1.2.3:443", nil); err != nil {
		t.Errorf("expected RFC1918 to be allowed, got %v", err)
	}
}

func TestGuardedHTTPClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// srv.URL is http://127.0.0.1:PORT — the guard must refuse to connect,
	// so an admin-configured webhook can never be used to reach the loopback
	// interface (the classic "webhook to my own admin API" SSRF).
	client := newGuardedHTTPClient(2 * time.Second)
	if _, err := client.Get(srv.URL); err == nil {
		t.Fatal("expected guarded client to refuse loopback connection")
	}
}
