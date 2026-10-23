package pop3

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pop3srv "github.com/rest-mail/go-pop3"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

const utf8Raw = "From: Jörg <joerg@bücher.example>\r\nSubject: Grüße\r\n\r\nHallo, Jörg.\r\n"

// utf8API answers the calls a POP3 session makes for one account whose INBOX
// holds one message with raw UTF-8 headers.
func utf8API(t *testing.T) *apiclient.Client {
	t.Helper()
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("/api/v1/auth/protocol-sign-in", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":{"access_token":"tok","user":{"id":1,"email":"jorg@example.test"}}}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		reply(w, fmt.Sprintf(`{"data":[{"id":7,"raw_size":%d}]}`, len(utf8Raw)))
	})
	mux.HandleFunc("/api/v1/messages/7", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":{"id":7}}`)
	})
	mux.HandleFunc("/api/v1/messages/7/raw", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "message/rfc822")
		_, _ = io.WriteString(w, utf8Raw)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return apiclient.New(srv.URL)
}

type pop3Client struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func dialUTF8(t *testing.T) *pop3Client {
	t.Helper()
	client, server := net.Pipe()
	sess := pop3srv.NewSession(server, NewBackend(utf8API(t), nil), nil, nil)
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
	c := &pop3Client{t: t, conn: client, r: bufio.NewReader(client)}
	c.line() // greeting
	return c
}

func (c *pop3Client) line() string {
	c.t.Helper()
	l, err := c.r.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return l
}

// do sends a command and returns its status line, failing the test on -ERR.
func (c *pop3Client) do(command string) string {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.conn, "%s\r\n", command); err != nil {
		c.t.Fatalf("write: %v", err)
	}
	l := c.line()
	if !strings.HasPrefix(l, "+OK") {
		c.t.Fatalf("%s: %q", command, l)
	}
	return l
}

// multi sends a command with a multi-line answer and returns its body, up to
// the terminating ".".
func (c *pop3Client) multi(command string) string {
	c.t.Helper()
	c.do(command)
	var b strings.Builder
	for {
		l := c.line()
		if l == ".\r\n" {
			return b.String()
		}
		b.WriteString(strings.TrimPrefix(l, "."))
	}
}

// The POP3 gateway offers UTF8 (RFC 6856) and serves the stored UTF-8 headers
// as they are to a client that sent UTF8 before logging in; any other client
// still gets them downgraded (RFC 6857).
func TestUTF8ThroughTheSession(t *testing.T) {
	plain := dialUTF8(t)
	if caps := plain.multi("CAPA"); !strings.Contains(caps, "UTF8\r\n") {
		t.Fatalf("CAPA lacks UTF8: %q", caps)
	}
	plain.do("USER jorg@example.test")
	plain.do("PASS pw")
	got := plain.multi("RETR 1")
	if !strings.Contains(got, "Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=") || strings.Contains(got, "Grüße") {
		t.Fatalf("RETR without UTF8 did not downgrade: %q", got)
	}

	eai := dialUTF8(t)
	eai.do("UTF8")
	eai.do("USER jorg@example.test")
	eai.do("PASS pw")
	if list := eai.do("LIST 1"); !strings.Contains(list, fmt.Sprintf(" %d", len(utf8Raw))) {
		t.Fatalf("LIST 1: %q", list)
	}
	if got := eai.multi("RETR 1"); got != utf8Raw {
		t.Fatalf("RETR after UTF8 served:\n%q\nwant the stored message\n%q", got, utf8Raw)
	}
}
