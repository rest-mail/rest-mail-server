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

// nulStored holds a NUL in a header, in text and in a raw attachment.
const nulStored = "Subject: a\x00b\r\nX-Note: Gr\xc3\xbc\x00\xc3\x9fe\r\n" +
	"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
	"--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHal\x00lo\r\n" +
	"--B\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n\r\n\x00\x01\xff\x00\r\n" +
	"--B--\r\n"

// nulSession signs in to a mailbox holding raw, with the sizes the API measures.
func nulSession(t *testing.T, raw string, utf8 bool) *pop3Client {
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
		reply(w, fmt.Sprintf(`{"data":[{"id":7,"raw_size":%d,"downgraded_size":%d}]}`, len(raw), rmail.DowngradedSize(raw)))
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

	client, server := net.Pipe()
	sess := pop3server.NewSession(server, NewBackend(apiclient.New(srv.URL), nil), nil, nil)
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
	if utf8 {
		c.do("UTF8")
	}
	c.do("USER jorg@example.test")
	c.do("PASS pw")
	return c
}

// RETR sends a space for a NUL in a header or in text, one octet for one, so LIST's
// size is what RETR sends. Data keeps its octets: POP3 has no other way to reach them.
func TestRetrNUL(t *testing.T) {
	for _, utf8 := range []bool{false, true} {
		t.Run(fmt.Sprintf("utf8=%v", utf8), func(t *testing.T) {
			c := nulSession(t, nulStored, utf8)
			list := c.do("LIST 1")
			got := c.multi("RETR 1")
			if want := fmt.Sprintf(" %d", len(got)); !strings.Contains(list, want) {
				t.Fatalf("LIST 1 = %q, RETR sent %d octets", list, len(got))
			}
			if !strings.Contains(got, "Subject: a b\r\n") || !strings.Contains(got, "Hal lo\r\n") {
				t.Fatalf("RETR = %q", got)
			}
			if !strings.Contains(got, "\r\n\r\n\x00\x01\xff\x00\r\n--B--") {
				t.Fatalf("RETR changed the attachment: %q", got)
			}
			if strings.Contains(got, "=00") {
				t.Fatalf("RETR spells a NUL in a header: %q", got)
			}
			if rest := strings.ReplaceAll(got, "\x00\x01\xff\x00", ""); strings.IndexByte(rest, 0) >= 0 {
				t.Fatalf("a NUL outside the attachment reached the client: %q", got)
			}
		})
	}
}

// TOP is cut from the same octets RETR sends.
func TestTopNUL(t *testing.T) {
	const raw = "Subject: a\x00b\r\n\r\nline\x00one\r\nline two\r\nline three\r\n"
	c := nulSession(t, raw, false)
	if got, want := c.multi("TOP 1 1"), "Subject: a b\r\n\r\nline one\r\n"; got != want {
		t.Fatalf("TOP 1 1 = %q, want %q", got, want)
	}
	if got, want := c.multi("TOP 1 0"), "Subject: a b\r\n\r\n"; got != want {
		t.Fatalf("TOP 1 0 = %q, want %q", got, want)
	}
}

// A message with no NUL is sent as it was before.
func TestRetrNoNULUnchanged(t *testing.T) {
	const raw = "Subject: plain\r\nContent-Type: application/pdf\r\nContent-Transfer-Encoding: binary\r\n\r\n\xff\xfe\r\n"
	c := nulSession(t, raw, true)
	if got := c.multi("RETR 1"); got != raw {
		t.Fatalf("RETR = %q, want %q", got, raw)
	}
}
