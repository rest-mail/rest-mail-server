package imap

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// sevenBitRaw needs converting on the way out: 8-bit headers at the top and in a
// part, and binary data under an 8-bit encoding.
const sevenBitRaw = "Subject: Gr\xc3\xbc\xc3\x9fe\r\nX-Latin: caf\xe9\r\n" +
	"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
	"--B\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Description: caf\xc3\xa9\r\n\r\nHallo J\xc3\xb6rg\r\n" +
	"--B\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n\r\n\xff\xfe\x01\r\n" +
	"--B--\r\n"

// storeAPI answers an IMAP session for one account holding raw, with the sizes
// the API measures when it stores a message.
func storeAPI(t *testing.T, raw string) *apiclient.Client {
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
		reply(w, fmt.Sprintf(`{"data":[{"id":7,"raw_size":%d,"downgraded_size":%d,"imap_size":%d,"imap_utf8_size":%d}]}`,
			len(raw), rmail.DowngradedSize(raw), rmail.IMAPWireSize(raw, false), rmail.IMAPWireSize(raw, true)))
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

func dialStore(t *testing.T, raw string) *imapClient {
	t.Helper()
	client, server := net.Pipe()
	sess := imapserver.NewSession(server, NewBackend(storeAPI(t, raw), nil), "imap.test", nil, goimap.NopLimiter{})
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

// fetchedLiteral splits "* 1 FETCH (... {n}\r\n<payload>)\r\n..." into the text
// before the literal and the payload.
func fetchedLiteral(t *testing.T, got string) (framing, payload string) {
	t.Helper()
	open := strings.Index(got, "{")
	end := strings.Index(got, "}\r\n")
	if open < 0 || end < open {
		t.Fatalf("no literal in %q", got)
	}
	n, err := strconv.Atoi(got[open+1 : end])
	if err != nil {
		t.Fatalf("literal size in %q: %v", got, err)
	}
	return got[:open], got[end+3 : end+3+n]
}

// RFC822.SIZE on the metadata path, with no body loaded, is the octet count BODY[]
// sends, in a session with UTF-8 enabled or not (RFC 3501 sections 2.3.4, 7.4.2).
func TestRFC822SizeMatchesConvertedBody(t *testing.T) {
	for _, utf8 := range []bool{false, true} {
		t.Run(fmt.Sprintf("utf8=%v", utf8), func(t *testing.T) {
			c := dialStore(t, sevenBitRaw)
			c.do("a1", "LOGIN jorg@example.test pw")
			if utf8 {
				c.do("a2", "ENABLE UTF8=ACCEPT")
			}
			c.do("a3", "SELECT INBOX")
			framing, whole := fetchedLiteral(t, c.do("a4", "FETCH 1 (RFC822.SIZE BODY.PEEK[])"))
			if want := fmt.Sprintf("RFC822.SIZE %d ", len(whole)); !strings.Contains(framing, want) {
				t.Fatalf("%q lacks %q; BODY[] sent %d octets", framing, want, len(whole))
			}
			if len(whole) == len(sevenBitRaw) {
				t.Fatal("nothing was converted, so this test proves nothing")
			}
			if strings.Contains(whole, "\xff") || strings.Contains(whole, "caf\xe9") {
				t.Fatalf("stored 8-bit data went out unconverted: %q", whole)
			}
			if !strings.Contains(whole, base64.StdEncoding.EncodeToString([]byte("\xff\xfe\x01"))) {
				t.Fatalf("binary data is not BASE64: %q", whole)
			}
			if !strings.Contains(whole, "Hallo J\xc3\xb6rg") {
				t.Fatalf("text naming a charset changed: %q", whole)
			}
		})
	}
}

// Without UTF-8 enabled the top-level header goes through the RFC 6857 downgrade
// first, and the engine converts what is left: octets that are not UTF-8 are
// UNKNOWN-8BIT, never labelled UTF-8, and a part's header is 7-bit as well.
func TestFetchHeadersThroughTheGateway(t *testing.T) {
	c := dialStore(t, sevenBitRaw)
	c.do("a1", "LOGIN jorg@example.test pw")
	c.do("a2", "SELECT INBOX")
	_, header := fetchedLiteral(t, c.do("a3", "FETCH 1 BODY.PEEK[HEADER]"))
	if !strings.Contains(header, "Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\n") {
		t.Fatalf("Subject: %q", header)
	}
	if !strings.Contains(header, "=?UNKNOWN-8BIT?Q?caf=E9?=") {
		t.Fatalf("not UTF-8 and not UNKNOWN-8BIT: %q", header)
	}
	_, mime := fetchedLiteral(t, c.do("a4", "FETCH 1 BODY.PEEK[1.MIME]"))
	if !strings.Contains(mime, "Content-Description: =?UTF-8?Q?caf=C3=A9?=\r\n") {
		t.Fatalf("1.MIME: %q", mime)
	}
}

// RFC 3516 BINARY is the way to the stored octets, whatever BODY sends.
func TestBinaryKeepsStoredOctetsThroughTheGateway(t *testing.T) {
	c := dialStore(t, sevenBitRaw)
	c.do("a1", "LOGIN jorg@example.test pw")
	c.do("a2", "SELECT INBOX")
	_, got := fetchedLiteral(t, c.do("a3", "FETCH 1 BINARY.PEEK[2]"))
	if got != "\xff\xfe\x01" {
		t.Fatalf("BINARY[2] = %q", got)
	}
}

// A message already 7-bit goes out byte for byte with its stored size, so a
// signature over it still verifies from FETCH output.
func TestSevenBitMessageIsServedAsStored(t *testing.T) {
	const raw = "DKIM-Signature: v=1; a=rsa-sha256; d=example.test; b=abc=\r\n" +
		"Subject: =?UTF-8?Q?Gr=C3=BC=C3=9Fe?=\r\n\r\nplain body\r\n"
	c := dialStore(t, raw)
	c.do("a1", "LOGIN jorg@example.test pw")
	c.do("a2", "SELECT INBOX")
	framing, whole := fetchedLiteral(t, c.do("a3", "FETCH 1 (RFC822.SIZE BODY.PEEK[])"))
	if whole != raw {
		t.Fatalf("BODY[] = %q, want %q", whole, raw)
	}
	if !strings.Contains(framing, fmt.Sprintf("RFC822.SIZE %d ", len(raw))) {
		t.Fatalf("RFC822.SIZE in %q", framing)
	}
}

func TestSevenBitParameterThroughTheGateway(t *testing.T) {
	for _, tc := range []struct{ name, stored, structure string }{
		{"repeated name", "a=1; other=keep; a=2", `("A" "1" "OTHER" "keep")`},
		{"gap in a continued value", "name*0=a; name*2=c", `("NAME" "ac")`},
		{"a star that isn't RFC 2231", "name*x=y; other=keep", `("NAME*X" "y" "OTHER" "keep")`},
	} {
		for head, downgraded := range map[string]string{
			"": "", "Subject: J\xc3\xb6\r\n": "Subject: =?utf-8?q?J=C3=B6?=\r\n",
		} {
			t.Run(tc.name+" after "+head, func(t *testing.T) {
				field := "Content-Type: application/pdf; " + tc.stored + "\r\n\r\n"
				c := dialStore(t, head+field+"data\r\n")
				c.do("a1", "LOGIN jorg@example.test pw")
				c.do("a2", "SELECT INBOX")
				if _, header := fetchedLiteral(t, c.do("a3", "FETCH 1 BODY.PEEK[HEADER]")); header != downgraded+field {
					t.Fatalf("HEADER = %q\n  want %q", header, downgraded+field)
				}
				got := c.do("a4", "FETCH 1 BODYSTRUCTURE")
				if want := `"APPLICATION" "PDF" ` + tc.structure; !strings.Contains(got, want) {
					t.Fatalf("%q lacks %q", got, want)
				}
			})
		}
	}
}
