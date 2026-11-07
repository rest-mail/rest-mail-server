package imap

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
)

// banTestPassword is the only password the counting API accepts.
const banTestPassword = "right"

// countingAPI is an auth API that accepts banTestPassword, refuses anything
// else with 401, and counts every sign-in request it sees.
func countingAPI(t *testing.T) (*apiclient.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/gateway-sessions" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password != banTestPassword {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"token":"rmgs_tok","user":{"id":1,"email":"a@example.com"}}}`)
	}))
	t.Cleanup(srv.Close)
	return apiclient.New(srv.URL), &calls
}

func banLimiter(maxFails int) *connlimiter.Limiter {
	return connlimiter.New(connlimiter.Config{
		AuthMaxFails:    maxFails,
		AuthBanWindow:   time.Minute,
		AuthBanDuration: time.Minute,
	})
}

// banClient is one IMAP client connection for the ban tests.
type banClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func (c *banClient) line() string {
	c.t.Helper()
	l, err := c.r.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return strings.TrimRight(l, "\r\n")
}

// command sends one command line and returns every line up to and including
// the tagged response.
func (c *banClient) command(tag, cmd string) []string {
	c.t.Helper()
	if _, err := io.WriteString(c.conn, tag+" "+cmd+"\r\n"); err != nil {
		c.t.Fatalf("write: %v", err)
	}
	var lines []string
	for {
		l := c.line()
		lines = append(lines, l)
		if strings.HasPrefix(l, tag+" ") {
			return lines
		}
	}
}

// closed reports whether the server closed its end of the connection.
func (c *banClient) closed() bool {
	c.t.Helper()
	_, err := c.r.ReadByte()
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe)
}

// pipeSession runs one gateway session over net.Pipe, wired as cmd/imap-gateway
// wires it: the same connlimiter is the backend's per-account limiter and the
// server's per-IP limiter. The session sees the client address "pipe". The
// greeting has been read when it returns.
func pipeSession(t *testing.T, api *apiclient.Client, lim *connlimiter.Limiter) *banClient {
	t.Helper()
	client, server := net.Pipe()
	sess := imapserver.NewSession(server, NewBackend(api, lim), "imap.test", nil, lim)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	if err := client.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	c := &banClient{t: t, conn: client, r: bufio.NewReader(client)}
	if g := c.line(); !strings.HasPrefix(g, "* OK") {
		t.Fatalf("greeting = %q", g)
	}
	return c
}

// requireBanRefusal checks the reply a banned address gets to a sign-in
// command: an untagged BYE, then a tagged NO with the RFC 5530 UNAVAILABLE
// code (a temporary refusal, not a verdict on the password), then the server
// closes the connection.
func requireBanRefusal(t *testing.T, c *banClient, tag string, lines []string) {
	t.Helper()
	want := []string{
		"* BYE Too many authentication failures, try again later",
		tag + " NO [UNAVAILABLE] Too many authentication failures, try again later",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("banned sign-in reply = %q, want %q", lines, want)
	}
	if !c.closed() {
		t.Fatal("connection stays open after refusing a banned address")
	}
}

// A banned address that opens a fresh connection is refused in the greeting
// and never reaches the auth API, even though it holds the right password
// (#1678). This is the reproduction from the issue, over a real TCP listener.
func TestBannedAddressRefusedAtConnect(t *testing.T) {
	api, calls := countingAPI(t)
	lim := banLimiter(1)
	lim.RecordAuthFail("127.0.0.1")
	if !lim.IsBanned("127.0.0.1") {
		t.Fatal("setup: 127.0.0.1 is not banned")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	srv := imapserver.New("imap.test", NewBackend(api, lim), nil, lim)
	srv.Serve(imapserver.Listeners{IMAP: listener})
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	c := &banClient{t: t, conn: conn, r: bufio.NewReader(conn)}

	greeting := c.line()
	if !strings.HasPrefix(greeting, "* BYE ") {
		reply := c.command("a1", "LOGIN a@example.com "+banTestPassword)
		t.Fatalf("banned address greeted with %q; LOGIN with the right password got %q (API calls %d)",
			greeting, reply, calls.Load())
	}
	if !c.closed() {
		t.Fatal("connection stays open after the BYE greeting")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("auth API calls = %d, want 0", got)
	}
}

// A session opened before its address was banned cannot sign in afterwards,
// with LOGIN or AUTHENTICATE, and the credential never reaches the auth API.
func TestBannedAddressRefusedBeforeCredentialCheck(t *testing.T) {
	plain := base64.StdEncoding.EncodeToString([]byte("\x00a@example.com\x00" + banTestPassword))
	for name, cmd := range map[string]string{
		"LOGIN":        "LOGIN a@example.com " + banTestPassword,
		"AUTHENTICATE": "AUTHENTICATE PLAIN " + plain,
	} {
		t.Run(name, func(t *testing.T) {
			api, calls := countingAPI(t)
			lim := banLimiter(1)
			c := pipeSession(t, api, lim)

			lim.RecordAuthFail("pipe") // another connection from this address used up its budget

			requireBanRefusal(t, c, "a1", c.command("a1", cmd))
			if got := calls.Load(); got != 0 {
				t.Fatalf("auth API calls = %d, want 0", got)
			}
		})
	}
}

// An address with no ban signs in with the right password.
func TestUnbannedAddressSignsIn(t *testing.T) {
	api, calls := countingAPI(t)
	c := pipeSession(t, api, banLimiter(1))

	lines := c.command("a1", "LOGIN a@example.com "+banTestPassword)
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "a1 OK ") {
		t.Fatalf("LOGIN = %q, want OK", lines)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("auth API calls = %d, want 1", got)
	}
}

// The configured number of failed guesses bans the address: the guess that
// reaches the limit is answered with the ban refusal, and from then on the
// right password is refused without asking the API.
func TestBanFollowsConfiguredFailures(t *testing.T) {
	api, calls := countingAPI(t)
	lim := banLimiter(2)

	c := pipeSession(t, api, lim)
	lines := c.command("a1", "LOGIN a@example.com wrong")
	if want := "a1 NO [AUTHENTICATIONFAILED] Invalid credentials"; len(lines) != 1 || lines[0] != want {
		t.Fatalf("first failure = %q, want %q", lines, want)
	}
	requireBanRefusal(t, c, "a2", c.command("a2", "LOGIN b@example.com wrong"))
	if got := calls.Load(); got != 2 {
		t.Fatalf("auth API calls after two guesses = %d, want 2", got)
	}

	c = pipeSession(t, api, lim)
	requireBanRefusal(t, c, "a1", c.command("a1", "LOGIN a@example.com "+banTestPassword))
	if got := calls.Load(); got != 2 {
		t.Fatalf("banned address reached the auth API: calls = %d, want 2", got)
	}
}
