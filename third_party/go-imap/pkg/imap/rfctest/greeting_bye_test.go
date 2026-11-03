package rfctest

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap/server"
)

// refuseLimiter turns every connection away and counts what the Server asks.
type refuseLimiter struct {
	accepts, releases atomic.Int32
}

func (l *refuseLimiter) Accept(string) bool {
	l.accepts.Add(1)
	return false
}
func (l *refuseLimiter) Release(string)        { l.releases.Add(1) }
func (l *refuseLimiter) RecordAuthFail(string) {}
func (l *refuseLimiter) IsBanned(string) bool  { return false }
func (l *refuseLimiter) ResetAuth(string)      {}

// TestGreetingByeOnRefusal checks that a connection the server is unwilling to
// accept is greeted with an untagged BYE and then closed, instead of being
// dropped without any greeting.
//
// checklist: IMAP-GREET-02
func TestGreetingByeOnRefusal(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	limiter := &refuseLimiter{}
	srv := server.New("imap.test", &backend{}, nil, limiter)
	if err := srv.ListenAndServe(server.Ports{IMAP: port}); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)

	greeting, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read greeting: %v", err)
	}
	if !strings.HasPrefix(greeting, "* BYE ") || !strings.HasSuffix(greeting, "\r\n") {
		t.Fatalf("greeting = %q, want an untagged BYE line", greeting)
	}
	if rest, err := r.ReadString('\n'); err != io.EOF {
		t.Fatalf("after BYE got %q, %v; want the server to close the connection", rest, err)
	}
	if got := limiter.accepts.Load(); got != 1 {
		t.Fatalf("limiter Accept calls = %d, want 1", got)
	}
	if got := limiter.releases.Load(); got != 0 {
		t.Fatalf("limiter Release calls = %d, want 0 for a refused connection", got)
	}
}
