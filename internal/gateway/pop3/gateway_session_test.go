package pop3

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// sessionAPI is an API that opens a POP3 connection session and records the
// Authorization header of every call made after it (#1586).
type sessionAPI struct {
	mu      sync.Mutex
	opened  int
	openedW string   // the Authorization the open call carried (none)
	openedB string   // the open call's body
	after   []string // the Authorization of every call after it
	closed  int
}

func (a *sessionAPI) handler() http.Handler {
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		a.mu.Lock()
		a.after = append(a.after, r.Header.Get("Authorization"))
		a.mu.Unlock()
	}
	mux.HandleFunc("POST /api/v1/gateway-sessions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.opened++
		a.openedW = r.Header.Get("Authorization")
		a.openedB = string(body)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"data":{"token":"rmgs_pop","protocol":"pop3","credential_id":3,"credential_kind":"app_password","user":{"id":1,"email":"dana@example.test"}}}`)
	})
	mux.HandleFunc("DELETE /api/v1/gateway-sessions/current", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		a.mu.Lock()
		a.closed++
		a.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v1/accounts/1/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	})
	return mux
}

// A POP3 connection signs in once, with the address and password in the open
// call's body (never HTTP Basic), and every call after it sends only the
// session's token.
func TestPOP3ConnectionUsesItsSessionNotBasic(t *testing.T) {
	a := &sessionAPI{}
	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)

	client, server := net.Pipe()
	sess := pop3server.NewSession(server, NewBackend(apiclient.New(srv.URL), nil), nil, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()
	if err := client.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	c := &pop3Client{t: t, conn: client, r: bufio.NewReader(client)}
	c.line() // greeting
	if r := c.do("USER dana@example.test"); !strings.HasPrefix(r, "+OK") {
		t.Fatalf("USER = %q", r)
	}
	if r := c.do("PASS app-password"); !strings.HasPrefix(r, "+OK") {
		t.Fatalf("PASS = %q", r)
	}
	if r := c.do("STAT"); !strings.HasPrefix(r, "+OK") {
		t.Fatalf("STAT = %q", r)
	}
	c.do("QUIT")
	_ = client.Close()
	<-done

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.opened != 1 || a.openedW != "" {
		t.Fatalf("the session was opened %d times, with Authorization %q (want once, with none)", a.opened, a.openedW)
	}
	if !strings.Contains(a.openedB, `"address":"dana@example.test"`) || !strings.Contains(a.openedB, `"password":"app-password"`) ||
		!strings.Contains(a.openedB, `"protocol":"pop3"`) {
		t.Fatalf("the open call's body does not carry the client's credential: %s", a.openedB)
	}
	if len(a.after) == 0 {
		t.Fatal("no call after sign-in")
	}
	for i, got := range a.after {
		if got != "Bearer rmgs_pop" {
			t.Errorf("call %d after sign-in sent Authorization %q, want the session token", i, got)
		}
	}
}

// The POP3 mailbox closes its session when it is closed. (The POP3 library
// does not yet call it at QUIT or disconnect; until it does, an unclosed
// session is closed by the API's idle sweep.)
func TestPOP3MailboxCloseEndsItsSession(t *testing.T) {
	a := &sessionAPI{}
	srv := httptest.NewServer(a.handler())
	t.Cleanup(srv.Close)

	mb, err := NewBackend(apiclient.New(srv.URL), nil).Authenticate("dana@example.test", "app-password")
	if err != nil {
		t.Fatal(err)
	}
	closer, ok := mb.(io.Closer)
	if !ok {
		t.Fatal("the POP3 mailbox has no Close")
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed != 1 || len(a.after) != 1 || a.after[0] != "Bearer rmgs_pop" {
		t.Fatalf("closed %d times with %v; want once, with the session token", a.closed, a.after)
	}
}
