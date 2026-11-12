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

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

const utf8Raw = "From: Jörg <joerg@bücher.example>\r\nSubject: Grüße\r\n\r\nHallo, Jörg.\r\n"

// storeAPI answers the calls a POP3 session makes for one account whose INBOX
// holds raw.
func storeAPI(t *testing.T, raw string) *apiclient.Client {
	t.Helper()
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("/api/v1/gateway-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		reply(w, `{"data":{"token":"rmgs_tok","protocol":"pop3","user":{"id":1,"email":"jorg@example.test"}}}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		reply(w, fmt.Sprintf(`{"data":[{"id":7,"raw_size":%d,"downgraded_size":%d,"pop3_utf8_size":%d}]}`,
			len(raw), rmail.DowngradedSize(raw), rmail.POP3WireSize(raw)))
	})
	mux.HandleFunc("/api/v1/messages/7", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":{"id":7}}`)
	})
	mux.HandleFunc("/api/v1/messages/7/raw", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "message/rfc822")
		_, _ = io.WriteString(w, raw)
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
	return dialStore(t, utf8Raw)
}

func dialStore(t *testing.T, raw string) *pop3Client {
	t.Helper()
	client, server := net.Pipe()
	sess := pop3server.NewSession(server, NewBackend(storeAPI(t, raw), nil), nil, nil)
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

// RETR without UTF8 keeps what the RFC 6857 downgrade used to drop or rename: group
// names and comments in an address field, and an unquoted 8-bit parameter, which keeps
// the real type beside it. The body stays, and after UTF8 the message is as stored.
func TestRetrKeepsAddressTextAndParameters(t *testing.T) {
	const raw = "From: Jörg <j@example.com> (Büro)\r\n" +
		"To: Grüppe: a@example.com, b@example.com;\r\n" +
		"Cc: Ann <a@example.com> (kept), Jörg <j@example.com>\r\n" +
		"Content-Type: application/pdf; name=Jörg.pdf; other=keep\r\n\r\nHallo\r\n"
	const want = "From: =?UTF-8?Q?J=C3=B6rg?= <j@example.com> (=?UTF-8?Q?B=C3=BCro?=)\r\n" +
		"To: =?UTF-8?Q?Gr=C3=BCppe?= : a@example.com, b@example.com;\r\n" +
		"Cc: Ann <a@example.com> (kept), =?UTF-8?Q?J=C3=B6rg?= <j@example.com>\r\n" +
		"Content-Type: application/pdf;\r\n name*=UTF-8''J%C3%B6rg.pdf;\r\n other=keep\r\n\r\nHallo\r\n"

	plain := dialStore(t, raw)
	plain.do("USER jorg@example.test")
	plain.do("PASS pw")
	if got := plain.multi("RETR 1"); got != want {
		t.Fatalf("RETR without UTF8:\n got %q\nwant %q", got, want)
	}

	eai := dialStore(t, raw)
	eai.do("UTF8")
	eai.do("USER jorg@example.test")
	eai.do("PASS pw")
	if got := eai.multi("RETR 1"); got != raw {
		t.Fatalf("RETR after UTF8:\n got %q\nwant %q", got, raw)
	}
}
