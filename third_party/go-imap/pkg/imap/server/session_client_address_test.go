package server

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// addressedBackend records the address each sign-in was made for.
type addressedBackend struct {
	*mockBackend
	asked []string
}

func (b *addressedBackend) ForClient(ip string) imap.Backend {
	b.asked = append(b.asked, ip)
	return b.mockBackend
}

// A ClientAddressBackend is told the address the client connected from, at sign-in.
func TestClientAddressBackendLearnsTheAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	backend := &addressedBackend{mockBackend: newMockBackend()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		NewSession(conn, backend, "imap.test", nil, imap.NopLimiter{}).Handle()
	}()

	dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.1.9")}, Timeout: 5 * time.Second}
	conn, err := dialer.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	if _, err := r.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "a1 LOGIN %s %s\r\n", backend.user, backend.pass); err != nil {
		t.Fatal(err)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "a1 OK") {
		t.Fatalf("LOGIN = %q", line)
	}
	_ = conn.Close()
	<-done
	if len(backend.asked) != 1 || backend.asked[0] != "127.0.1.9" {
		t.Fatalf("the backend was told %v, want the client's 127.0.1.9 once", backend.asked)
	}
}
