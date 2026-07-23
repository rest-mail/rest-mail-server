package smtp

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/connlimiter"
)

// TestLimitListener_PerIPLimitAndRelease proves the connection limiter still
// acts at accept level under go-smtp: an over-limit connection is closed
// before any SMTP handling (no greeting), and closing an admitted connection
// releases its slot.
func TestLimitListener_PerIPLimitAndRelease(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 1, MaxGlobal: 10})
	srv := NewServer("smtp.test", back, nil, store, limiter).newSMTPServer(false)

	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(&limitListener{Listener: base, limiter: limiter}) }()
	t.Cleanup(func() { _ = srv.Close() })

	readGreeting := func(c net.Conn) (string, error) {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		return bufio.NewReader(c).ReadString('\n')
	}

	// First connection is admitted and greeted.
	c1, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if g, err := readGreeting(c1); err != nil || !strings.HasPrefix(g, "220") {
		t.Fatalf("first conn greeting = %q, %v", g, err)
	}

	// A second concurrent connection from the same IP exceeds MaxPerIP: the
	// limiter closes it before any SMTP handling, so no greeting arrives.
	c2, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if g, err := readGreeting(c2); err == nil {
		t.Fatalf("over-limit conn should be closed without a greeting, got %q", g)
	}
	_ = c2.Close()

	// Closing the first connection releases its slot; a fresh connection is
	// admitted again (release happens when the server notices the close, so
	// poll briefly).
	_ = c1.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c3, err := net.Dial("tcp", base.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		g, err := readGreeting(c3)
		_ = c3.Close()
		if err == nil && strings.HasPrefix(g, "220") {
			return // slot was released
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot never released; last greeting %q, err %v", g, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
