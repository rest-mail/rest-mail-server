package imap

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// sessionAPI is an API that opens a connection session and records the
// Authorization header of every call made after it (#1586).
type sessionAPI struct {
	mu      sync.Mutex
	openedW string   // the Authorization the open call carried
	after   []string // the Authorization of every call after it
	closed  int
}

func (a *sessionAPI) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	record := func(r *http.Request) {
		a.mu.Lock()
		a.after = append(a.after, r.Header.Get("Authorization"))
		a.mu.Unlock()
	}
	mux.HandleFunc("POST /api/v1/gateway-sessions", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.openedW = r.Header.Get("Authorization")
		a.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		reply(w, `{"data":{"token":"rmgs_conn","protocol":"imap","credential_id":3,"credential_kind":"app_password","user":{"id":1,"email":"dana@example.test"}}}`)
	})
	mux.HandleFunc("DELETE /api/v1/gateway-sessions/current", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		a.mu.Lock()
		a.closed++
		a.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v1/accounts/1/folders", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		reply(w, `{"data":[{"name":"INBOX"}]}`)
	})
	mux.HandleFunc("GET /api/v1/accounts/1/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		reply(w, `{"data":[]}`)
	})
	return mux
}

func runSession(t *testing.T, api *apiclient.Client, idlePoll time.Duration, script func(c *imapClient)) {
	t.Helper()
	client, server := net.Pipe()
	sess := imapserver.NewSession(server, NewBackend(api, nil), "imap.test", nil, goimap.NopLimiter{})
	if idlePoll > 0 {
		sess.SetIdlePollInterval(idlePoll)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()
	if err := client.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	c := &imapClient{t: t, conn: client, r: bufio.NewReader(client)}
	c.read() // greeting
	script(c)
	_ = client.Close()
	<-done
}

// A connection signs in once and then sends only its session's token: the
// address and password never go to the API again, and the session is closed at
// LOGOUT.
func TestIMAPConnectionUsesItsSessionNotBasic(t *testing.T) {
	a := &sessionAPI{}
	srv := httptest.NewServer(a.handler(t))
	t.Cleanup(srv.Close)

	runSession(t, apiclient.New(srv.URL), 0, func(c *imapClient) {
		c.do("a1", "LOGIN dana@example.test app-password")
		c.do("a2", `LIST "" "*"`)
		c.do("a3", "SELECT INBOX")
		c.do("a4", "LOGOUT")
	})

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.openedW == "" || a.openedW[:6] != "Basic " {
		t.Fatalf("the session was not opened with the client's credential: %q", a.openedW)
	}
	if len(a.after) < 3 {
		t.Fatalf("expected the LIST, SELECT and close calls, got %d", len(a.after))
	}
	for i, got := range a.after {
		if got != "Bearer rmgs_conn" {
			t.Errorf("call %d after sign-in sent Authorization %q, want the session token", i, got)
		}
	}
	if a.closed != 1 {
		t.Errorf("the session was closed %d times, want once at LOGOUT", a.closed)
	}
}

// A connection that drops without LOGOUT still closes its session.
func TestIMAPDisconnectClosesItsSession(t *testing.T) {
	a := &sessionAPI{}
	srv := httptest.NewServer(a.handler(t))
	t.Cleanup(srv.Close)

	runSession(t, apiclient.New(srv.URL), 0, func(c *imapClient) {
		c.do("a1", "LOGIN dana@example.test app-password")
	})

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed != 1 {
		t.Errorf("the session was closed %d times, want once at disconnect", a.closed)
	}
}
