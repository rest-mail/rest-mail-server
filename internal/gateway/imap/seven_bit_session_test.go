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

// A name decoded from an encoded word and a parameter decoded from RFC 2231 reach a
// session without UTF-8 as encoded words, with no literal (RFC 3501 section 4.3.1),
// and a session that enabled UTF-8 as UTF-8 literals.
func TestDecodedStringsThroughTheGateway(t *testing.T) {
	const raw = "From: =?UTF-8?Q?J=C3=B6rg?= <j@example.com>\r\n" +
		"Content-Type: application/octet-stream; name*=utf-8''caf%C3%A9\r\n\r\ndata\r\n"
	c := dialStore(t, raw)
	c.do("a1", "LOGIN jorg@example.test pw")
	c.do("a2", "SELECT INBOX")
	got := c.do("a3", "FETCH 1 (ENVELOPE BODYSTRUCTURE)")
	for _, want := range []string{`(("=?UTF-8?Q?J=C3=B6rg?=" NIL "j" "example.com"))`, `("NAME" "=?UTF-8?Q?caf=C3=A9?=")`} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "{") || strings.IndexByte(got, 0xc3) >= 0 {
		t.Fatalf("8-bit text or a literal reached the session: %q", got)
	}

	c = dialStore(t, raw)
	c.do("a1", "LOGIN jorg@example.test pw")
	c.do("a2", "ENABLE UTF8=ACCEPT")
	c.do("a3", "SELECT INBOX")
	got = c.do("a4", "FETCH 1 (ENVELOPE BODYSTRUCTURE)")
	for _, want := range []string{"{5}\r\nJörg", "{5}\r\ncafé"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q lacks %q", got, want)
		}
	}
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

// The RFC 6857 downgrade of the top-level header keeps a parameter in a charset
// neither it nor the engine can read, beside one it has to convert, and the size
// RFC822.SIZE reports still counts what BODY[] sends.
func TestParameterInOtherCharsetThroughTheGateway(t *testing.T) {
	const raw = "Content-Type: application/x-y; name*=windows-1252''x%F6.bin; note=\"Gr\xc3\xbc\"\r\n" +
		"Content-Disposition: attachment; filename*=iso-8859-2''%A3.txt; note=\"caf\xe9\"\r\n\r\ndata\r\n"
	c := dialStore(t, raw)
	c.do("a1", "LOGIN jorg@example.test pw")
	c.do("a2", "SELECT INBOX")
	_, header := fetchedLiteral(t, c.do("a3", "FETCH 1 BODY.PEEK[HEADER]"))
	const want = "Content-Type: application/x-y;\r\n name*=windows-1252''x%F6.bin;\r\n note*=UTF-8''Gr%C3%BC\r\n" +
		"Content-Disposition: attachment;\r\n filename*=iso-8859-2''%A3.txt;\r\n note*=UNKNOWN-8BIT''caf%E9\r\n\r\n"
	if header != want {
		t.Fatalf("HEADER = %q\n  want %q", header, want)
	}
	got := c.do("a4", "FETCH 1 BODYSTRUCTURE")
	for _, want := range []string{
		`("NAME" "=?windows-1252?Q?x=F6=2Ebin?=" "NOTE" "=?UTF-8?Q?Gr=C3=BC?=")`,
		`("ATTACHMENT" ("FILENAME" "=?iso-8859-2?Q?=A3=2Etxt?=" "NOTE" "=?UNKNOWN-8BIT?Q?caf=E9?="))`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q lacks %q", got, want)
		}
	}
	framing, whole := fetchedLiteral(t, c.do("a5", "FETCH 1 (RFC822.SIZE BODY.PEEK[])"))
	if want := fmt.Sprintf("RFC822.SIZE %d ", len(whole)); !strings.Contains(framing, want) {
		t.Fatalf("%q lacks %q; BODY[] sent %d octets", framing, want, len(whole))
	}
}

// The RFC 6857 downgrade of an address field keeps group names and comments, so a
// session without UTF-8 reads them in the header and in ENVELOPE, and the size
// RFC822.SIZE reports counts the converted field. After UTF8=ACCEPT the field is as
// stored.
func TestAddressTextThroughTheGateway(t *testing.T) {
	const raw = "From: Jörg <j@example.com> (Büro)\r\n" +
		"To: Grüppe: a@example.com, b@example.com;\r\n" +
		"Cc: Ann <a@example.com> (kept), Jörg <j@example.com>\r\n\r\nHallo\r\n"
	const want = "From: =?UTF-8?Q?J=C3=B6rg?= <j@example.com> (=?UTF-8?Q?B=C3=BCro?=)\r\n" +
		"To: =?UTF-8?Q?Gr=C3=BCppe?= : a@example.com, b@example.com;\r\n" +
		"Cc: Ann <a@example.com> (kept), =?UTF-8?Q?J=C3=B6rg?= <j@example.com>\r\n\r\n"
	c := dialStore(t, raw)
	c.do("a1", "LOGIN jorg@example.test pw")
	c.do("a2", "SELECT INBOX")
	if _, got := fetchedLiteral(t, c.do("a3", "FETCH 1 BODY.PEEK[HEADER]")); got != want {
		t.Fatalf("HEADER:\n got %q\nwant %q", got, want)
	}
	envelope := c.do("a4", "FETCH 1 ENVELOPE")
	for _, part := range []string{
		`(NIL NIL "=?UTF-8?Q?Gr=C3=BCppe?=" NIL)`,
		`(NIL NIL "a" "example.com")`,
		`(NIL NIL "b" "example.com")`,
		`(("=?UTF-8?Q?J=C3=B6rg?=" NIL "j" "example.com"))`,
	} {
		if !strings.Contains(envelope, part) {
			t.Fatalf("%q lacks %q", envelope, part)
		}
	}
	framing, whole := fetchedLiteral(t, c.do("a5", "FETCH 1 (RFC822.SIZE BODY.PEEK[])"))
	if wantSize := fmt.Sprintf("RFC822.SIZE %d ", len(whole)); !strings.Contains(framing, wantSize) {
		t.Fatalf("%q lacks %q; BODY[] sent %d octets", framing, wantSize, len(whole))
	}

	c = dialStore(t, raw)
	c.do("a1", "LOGIN jorg@example.test pw")
	c.do("a2", "ENABLE UTF8=ACCEPT")
	c.do("a3", "SELECT INBOX")
	if _, got := fetchedLiteral(t, c.do("a4", "FETCH 1 BODY.PEEK[]")); got != raw {
		t.Fatalf("BODY[] after UTF8=ACCEPT:\n got %q\nwant %q", got, raw)
	}
}

// An unquoted 8-bit parameter survives the downgrade of the top-level header: the
// field keeps its name, type and other parameters, and BODYSTRUCTURE agrees.
func TestUnquotedParameterThroughTheGateway(t *testing.T) {
	const raw = "Content-Type: application/pdf; name=Jörg.pdf; other=keep\r\n\r\ndata\r\n"
	c := dialStore(t, raw)
	c.do("a1", "LOGIN jorg@example.test pw")
	c.do("a2", "SELECT INBOX")
	_, header := fetchedLiteral(t, c.do("a3", "FETCH 1 BODY.PEEK[HEADER]"))
	if want := "Content-Type: application/pdf;\r\n name*=UTF-8''J%C3%B6rg.pdf;\r\n other=keep\r\n\r\n"; header != want {
		t.Fatalf("HEADER = %q\n  want %q", header, want)
	}
	got := c.do("a4", "FETCH 1 BODYSTRUCTURE")
	if want := `"APPLICATION" "PDF" ("NAME" "=?UTF-8?Q?J=C3=B6rg=2Epdf?=" "OTHER" "keep")`; !strings.Contains(got, want) {
		t.Fatalf("%q lacks %q", got, want)
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
