package imap

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	imapsrv "github.com/rest-mail/go-imap"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

const utf8Raw = "From: Jörg <joerg@bücher.example>\r\nSubject: Grüße\r\n\r\nHallo, Jörg.\r\n"

// utf8API answers the calls an IMAP session makes for one account holding one
// message whose headers are raw UTF-8.
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
	mux.HandleFunc("/api/v1/accounts/1/folders", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":[{"name":"INBOX"}]}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		reply(w, fmt.Sprintf(`{"data":[{"id":7,"subject":"Grüße","raw_size":%d}]}`, len(utf8Raw)))
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

type imapClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func dialUTF8(t *testing.T) *imapClient {
	t.Helper()
	client, server := net.Pipe()
	sess := imapsrv.NewSession(server, NewBackend(utf8API(t), nil), "imap.test", nil, imapsrv.NopLimiter{})
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
	c := &imapClient{t: t, conn: client, r: bufio.NewReader(client)}
	c.read() // greeting
	return c
}

// read returns one response line, with any literal it announces appended.
func (c *imapClient) read() string {
	c.t.Helper()
	line, err := c.r.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	if i := strings.LastIndex(line, "{"); i >= 0 && strings.HasSuffix(line, "}\r\n") {
		n, err := strconv.Atoi(line[i+1 : len(line)-3])
		if err == nil {
			buf := make([]byte, n)
			if _, err := io.ReadFull(c.r, buf); err != nil {
				c.t.Fatalf("literal: %v", err)
			}
			return line + string(buf) + c.read()
		}
	}
	return line
}

// do sends a command and returns everything up to and including its tagged
// completion.
func (c *imapClient) do(tag, command string) string {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.conn, "%s %s\r\n", tag, command); err != nil {
		c.t.Fatalf("write: %v", err)
	}
	var out strings.Builder
	for {
		l := c.read()
		out.WriteString(l)
		if strings.HasPrefix(l, tag+" ") {
			if !strings.HasPrefix(l, tag+" OK") {
				c.t.Fatalf("%s %s: %q", tag, command, out.String())
			}
			return out.String()
		}
	}
}

// The IMAP gateway offers UTF8=ACCEPT (RFC 6855) and, once a client enables
// it, serves the stored UTF-8 headers as they are; a client that has not
// enabled it still gets them downgraded (RFC 6857).
func TestUTF8AcceptThroughTheSession(t *testing.T) {
	plain := dialUTF8(t)
	plain.do("a1", "LOGIN jorg@example.test pw")
	if caps := plain.do("a2", "CAPABILITY"); !strings.Contains(caps, "UTF8=ACCEPT") {
		t.Fatalf("CAPABILITY after login lacks UTF8=ACCEPT: %q", caps)
	}
	plain.do("a3", "SELECT INBOX")
	got := plain.do("a4", "FETCH 1 BODY.PEEK[HEADER.FIELDS (SUBJECT)]")
	if !strings.Contains(got, "Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=") || strings.Contains(got, "Grüße") {
		t.Fatalf("FETCH without ENABLE did not downgrade: %q", got)
	}

	eai := dialUTF8(t)
	eai.do("b1", "LOGIN jorg@example.test pw")
	if enabled := eai.do("b2", "ENABLE UTF8=ACCEPT"); !strings.Contains(enabled, "* ENABLED UTF8=ACCEPT") {
		t.Fatalf("ENABLE: %q", enabled)
	}
	eai.do("b3", "SELECT INBOX")
	got = eai.do("b4", "FETCH 1 (RFC822.SIZE BODY.PEEK[])")
	if !strings.Contains(got, utf8Raw) {
		t.Fatalf("FETCH after ENABLE did not serve the stored message: %q", got)
	}
	// Served as stored, the message is exactly the size the session reported.
	if !strings.Contains(got, fmt.Sprintf("RFC822.SIZE %d", len(utf8Raw))) {
		t.Fatalf("RFC822.SIZE does not match the served octets: %q", got)
	}
}
