package connlimiter

import (
	"testing"
	"time"
)

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
