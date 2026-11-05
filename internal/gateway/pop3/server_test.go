package pop3

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// startServer serves a plain listener, as the implicit-TLS one looks to a
// session, and dials it from source.
func startServer(t *testing.T, a *sessionAPI, source string) (*Server, *pop3Client) {
	t.Helper()
	api := httptest.NewServer(a.handler())
	t.Cleanup(api.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(NewBackend(apiclient.New(api.URL), nil), nil)
	if err := srv.Serve(listener); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(source)}, Timeout: 5 * time.Second}
	conn, err := dialer.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	c := &pop3Client{t: t, conn: conn, r: bufio.NewReader(conn)}
	c.line() // greeting
	return srv, c
}

// Each connection signs in as itself: the session-open call says which address
// the mail client connected from, whichever of them it is (#1957).
func TestPOP3SignInStatesTheClientsAddress(t *testing.T) {
	a := &sessionAPI{}
	_, c := startServer(t, a, "127.0.2.7")
	c.do("USER dana@example.test")
	if r := c.do("PASS app-password"); !strings.HasPrefix(r, "+OK") {
		t.Fatalf("PASS = %q", r)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var body struct {
		ClientIP string `json:"client_ip"`
	}
	if err := json.Unmarshal([]byte(a.openedB), &body); err != nil {
		t.Fatal(err)
	}
	if body.ClientIP != "127.0.2.7" {
		t.Fatalf("the open call says the client is at %q, want 127.0.2.7: %s", body.ClientIP, a.openedB)
	}
}

// A connection that has TLS already is not offered STLS, and may log in.
func TestPOP3SessionDoesNotOfferSTLS(t *testing.T) {
	_, c := startServer(t, &sessionAPI{}, "127.0.2.8")
	if _, err := fmt.Fprint(c.conn, "CAPA\r\n"); err != nil {
		t.Fatal(err)
	}
	for {
		line := c.line()
		if strings.HasPrefix(line, "STLS") {
			t.Fatal("CAPA offers STLS")
		}
		if strings.TrimSpace(line) == "." {
			return
		}
	}
}

// Shutdown waits for the session it finds, which then ends; Close ends it at once.
func TestPOP3ServerShutdownWaitsAndCloseEnds(t *testing.T) {
	srv, c := startServer(t, &sessionAPI{}, "127.0.2.9")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown with a session open = %v, want the deadline", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown once the session has gone = %v", err)
	}
	if _, err := c.r.ReadString('\n'); err == nil {
		t.Fatal("the connection is still open after Close")
	}
}

func TestPOP3ServerRefusesTLSWithoutACertificate(t *testing.T) {
	srv := NewServer(NewBackend(apiclient.New("http://127.0.0.1:1"), nil), nil)
	for name, cfg := range map[string]*tls.Config{"none": nil, "no certificate": {}} {
		if err := srv.ListenAndServeTLS(0, cfg); err == nil {
			t.Errorf("%s: the port opened", name)
		}
	}
}
