package bancheck

import (
	"testing"

	"github.com/restmail/restmail/internal/gateway/connlimiter"
)

func TestWire_NilDatabase_NoOp(t *testing.T) {
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 10, MaxGlobal: 100})

	// Wire with nil database should be a no-op (no panic, no ban checker set).
	Wire(limiter, nil, "smtp")

	// Limiter should still accept connections since no ban checker was set.
	if !limiter.Accept("1.2.3.4") {
		t.Fatal("expected Accept to succeed when Wire was called with nil database")
	}
	limiter.Release("1.2.3.4")
}

func TestWire_NilDatabase_IsBanned_False(t *testing.T) {
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 10, MaxGlobal: 100})
	Wire(limiter, nil, "smtp")

	// With nil database, IsBanned should return false (no persistent ban checker).
	if limiter.IsBanned("1.2.3.4") {
		t.Fatal("expected IsBanned to return false when Wire was called with nil database")
	}
}

func TestWire_SetsBanChecker_OnLimiter(t *testing.T) {
	// We cannot test the full DB path without a real gorm.DB, but we can
	// verify the integration point: Wire should set a BanChecker on the
	// limiter. We verify this indirectly by using SetBanChecker directly
	// (which Wire calls internally) and confirming it affects Accept/IsBanned.

	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 10, MaxGlobal: 100})

	// Simulate what Wire does: set a ban checker that always bans "9.9.9.9".
	limiter.SetBanChecker(func(ip, protocol string) bool {
		return ip == "9.9.9.9"
	}, "smtp")

	// The banned IP should be rejected by Accept.
	if limiter.Accept("9.9.9.9") {
		t.Fatal("expected Accept to reject banned IP")
	}

	// The banned IP should be reported as banned.
	if !limiter.IsBanned("9.9.9.9") {
		t.Fatal("expected IsBanned to return true for banned IP")
	}

	// A non-banned IP should still be accepted.
	if !limiter.Accept("1.2.3.4") {
		t.Fatal("expected Accept to succeed for non-banned IP")
	}
	limiter.Release("1.2.3.4")

	if limiter.IsBanned("1.2.3.4") {
		t.Fatal("expected IsBanned to return false for non-banned IP")
	}
}

func TestWire_BanChecker_ReceivesProtocol(t *testing.T) {
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 10, MaxGlobal: 100})

	var receivedProtocol string
	limiter.SetBanChecker(func(ip, protocol string) bool {
		receivedProtocol = protocol
		return false
	}, "imap")

	// Trigger the ban checker via Accept.
	limiter.Accept("1.2.3.4")
	limiter.Release("1.2.3.4")

	if receivedProtocol != "imap" {
		t.Fatalf("expected protocol 'imap', got %q", receivedProtocol)
	}
}

func TestWire_BanChecker_ReceivesIP(t *testing.T) {
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 10, MaxGlobal: 100})

	var receivedIP string
	limiter.SetBanChecker(func(ip, protocol string) bool {
		receivedIP = ip
		return false
	}, "pop3")

	limiter.Accept("192.168.1.50")
	limiter.Release("192.168.1.50")

	if receivedIP != "192.168.1.50" {
		t.Fatalf("expected IP '192.168.1.50', got %q", receivedIP)
	}
}

func TestWire_BanChecker_BlocksAllProtocolsWhenSet(t *testing.T) {
	// Simulate a ban checker that bans an IP across all protocols.
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 10, MaxGlobal: 100})

	bannedIPs := map[string]bool{"10.0.0.1": true}
	limiter.SetBanChecker(func(ip, protocol string) bool {
		return bannedIPs[ip]
	}, "smtp")

	// Banned IP cannot connect.
	if limiter.Accept("10.0.0.1") {
		t.Fatal("expected banned IP to be rejected")
	}

	// Unbanned IP can connect.
	if !limiter.Accept("10.0.0.2") {
		t.Fatal("expected unbanned IP to be accepted")
	}
	limiter.Release("10.0.0.2")

	// After removing the ban, the IP should be accepted.
	delete(bannedIPs, "10.0.0.1")
	if !limiter.Accept("10.0.0.1") {
		t.Fatal("expected previously-banned IP to be accepted after un-banning")
	}
	limiter.Release("10.0.0.1")
}
