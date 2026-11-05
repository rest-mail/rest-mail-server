package imap

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// nulStored is a message as the API stores it, holding a NUL in a header, in text and
// in a raw attachment. Imported mail can already hold one.
const nulStored = "Subject: a\x00b\r\nX-Note: Gr\xc3\xbc\x00\xc3\x9fe\r\n" +
	"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
	"--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHal\x00lo\r\n" +
	"--B\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n\r\n\x00\x01\xff\x00\r\n" +
	"--B--\r\n"

func dialNUL(t *testing.T, utf8 bool, raw string) *imapClient {
	t.Helper()
	c := dialStore(t, raw)
	c.do("a1", "LOGIN jorg@example.test pw")
	if utf8 {
		c.do("e1", "ENABLE UTF8=ACCEPT")
	}
	c.do("a2", "SELECT INBOX")
	return c
}

// FETCH through the gateway sends a space for a NUL in a header or in text, the size it
// reports is the size it sends, and the attachment decodes to the stored octets. BINARY
// still reads the stored octets afterwards, in the same session.
func TestFetchNULThroughTheGateway(t *testing.T) {
	for _, utf8 := range []bool{false, true} {
		t.Run(fmt.Sprintf("utf8=%v", utf8), func(t *testing.T) {
			c := dialNUL(t, utf8, nulStored)
			framing, whole := fetchedLiteral(t, c.do("a3", "FETCH 1 (RFC822.SIZE BODY.PEEK[])"))
			if strings.IndexByte(whole, 0) >= 0 {
				t.Fatalf("BODY[] holds a NUL: %q", whole)
			}
			if want := fmt.Sprintf("RFC822.SIZE %d ", len(whole)); !strings.Contains(framing, want) {
				t.Fatalf("%q lacks %q", framing, want)
			}
			if !strings.Contains(whole, "Subject: a b\r\n") || !strings.Contains(whole, "Hal lo") {
				t.Fatalf("BODY[] = %q", whole)
			}
			_, partial := fetchedLiteral(t, c.do("a4", "FETCH 1 BODY.PEEK[]<3.9>"))
			if partial != whole[3:12] {
				t.Fatalf("BODY[]<3.9> = %q, want %q", partial, whole[3:12])
			}
			_, attachment := fetchedLiteral(t, c.do("a5", "FETCH 1 BODY.PEEK[2]"))
			decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(attachment, "\r\n", ""))
			if err != nil || string(decoded) != "\x00\x01\xff\x00" {
				t.Fatalf("BODY[2] = %q decodes to %q, %v", attachment, decoded, err)
			}
			got := c.do("a6", "FETCH 1 BINARY.PEEK[2]")
			if !strings.Contains(got, "~{4}\r\n\x00\x01\xff\x00") {
				t.Fatalf("BINARY[2]: %q", got)
			}
			if !utf8 {
				return
			}
			// With UTF-8 enabled nothing rewrites the header, so this is the stored source.
			got = c.do("a7", "FETCH 1 BINARY.PEEK[]")
			if want := fmt.Sprintf("~{%d}\r\n%s", len(nulStored), nulStored); !strings.Contains(got, want) {
				t.Fatalf("BINARY[]: %q", got)
			}
		})
	}
}

// BINARY[] and BINARY[n] send the exact stored octets as a literal8, and a FETCH before
// them changes nothing.
func TestBinaryNULThroughTheGateway(t *testing.T) {
	const raw = "Subject: a\x00b\r\n\r\nte\x00xt\r\n"
	c := dialNUL(t, false, raw)
	_, body := fetchedLiteral(t, c.do("a3", "FETCH 1 BODY.PEEK[]"))
	if want := strings.ReplaceAll(raw, "\x00", " "); body != want {
		t.Fatalf("BODY[] = %q, want %q", body, want)
	}
	if got, want := c.do("a4", "FETCH 1 BINARY.PEEK[]"), fmt.Sprintf("~{%d}\r\n%s", len(raw), raw); !strings.Contains(got, want) {
		t.Fatalf("BINARY[]: %q lacks %q", got, want)
	}
	if got, want := c.do("a5", "FETCH 1 BINARY.PEEK[1]"), "~{7}\r\nte\x00xt\r\n"; !strings.Contains(got, want) {
		t.Fatalf("BINARY[1]: %q lacks %q", got, want)
	}
}

// The downgrade spells no NUL in an encoded word, and the header it writes reads the
// same as the engine's for the same field.
func TestFetchNULInEightBitHeaderThroughTheGateway(t *testing.T) {
	c := dialNUL(t, false, nulStored)
	_, header := fetchedLiteral(t, c.do("a3", "FETCH 1 BODY.PEEK[HEADER]"))
	if strings.Contains(header, "=00") || strings.IndexByte(header, 0) >= 0 {
		t.Fatalf("header spells a NUL: %q", header)
	}
}

// Decoded NULs in ENVELOPE and BODYSTRUCTURE go out as spaces, with no literal.
func TestFetchDecodedNULThroughTheGateway(t *testing.T) {
	const raw = "From: =?UTF-8?Q?a=00b?= <j@example.com>\r\n" +
		"Content-Type: application/pdf; name*=utf-8''c%00d\r\n\r\ndata\r\n"
	c := dialNUL(t, false, raw)
	got := c.do("a3", "FETCH 1 (ENVELOPE BODYSTRUCTURE)")
	for _, want := range []string{`(("a b" NIL "j" "example.com"))`, `("NAME" "c d")`} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q lacks %q", got, want)
		}
	}
	if strings.IndexByte(got, 0) >= 0 {
		t.Fatalf("a NUL reached the session: %q", got)
	}
}
