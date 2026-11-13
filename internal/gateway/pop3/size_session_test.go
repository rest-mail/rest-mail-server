package pop3

import (
	"fmt"
	"strings"
	"testing"
)

// sizeShapes are stored messages whose octets RETR doesn't send as they are, or that sit
// next to a form it does. Each goes through a session with UTF-8 enabled and one without.
const boundaryParts = "--%[1]s\r\nContent-Type: text/plain\r\n\r\nHö\r\n--%[1]s\r\nContent-Type: application/pdf\r\n\r\n\xff\xfe\r\n--%[1]s--\r\n"

var sizeShapes = []struct{ name, raw string }{
	{"CRLF throughout", "Subject: hi\r\n\r\nbody\r\n"},
	{"bare LF", "Subject: hi\n\nline one\nline two\n"},
	{"bare LF in the body only", "Subject: hi\r\n\r\nline one\nline two\r\n"},
	{"bare LF under an 8-bit header", "Subject: Grüße\n\nline one\nline two\n"},
	{"bare CR", "Subject: hi\r\n\r\nline\rone\r\n"},
	{"bare CR under an 8-bit header", "Subject: Grüße\r\n\r\nline\rone\r\n"},
	{"CR before CRLF", "Subject: hi\r\n\r\nline\r\r\n"},
	{"CR after LF", "Subject: hi\r\n\r\nline\n\rone\r\n"},
	{"no final line break", "Subject: hi\r\n\r\nbody"},
	{"no final line break under an 8-bit header", "Subject: Grüße\r\n\r\nbody"},
	{"ends in a bare CR", "Subject: hi\r\n\r\nbody\r"},
	{"ends in a bare CR under an 8-bit header", "Subject: Grüße\r\n\r\nbody\r"},
	{"ends in a bare LF", "Subject: hi\r\n\r\nbody\n"},
	{"8-bit boundary", "Content-Type: multipart/mixed; boundary=\"Jö\"\r\n\r\n" + fmt.Sprintf(boundaryParts, "Jö")},
	{"8-bit boundary with bare LF", strings.ReplaceAll("Content-Type: multipart/mixed; boundary=\"Jö\"\r\n\r\n"+fmt.Sprintf(boundaryParts, "Jö"), "\r\n", "\n")},
	{"8-bit boundary with no final line break", "Content-Type: multipart/mixed; boundary=\"Jö\"\r\n\r\n--Jö\r\n\r\nx\r\n--Jö--"},
	{"empty body", "Subject: hi\r\n\r\n"},
	{"empty body under an 8-bit header", "Subject: Grüße\r\n\r\n"},
	{"empty body with bare LF", "Subject: hi\n\n"},
	{"header only", "Subject: hi\r\n"},
	{"header only with no line break", "Subject: hi"},
	{"8-bit header only with no line break", "Subject: Grüße"},
	{"dot-stuffed lines", "Subject: hi\r\n\r\n.\r\n..\r\n.leading\r\nplain\r\n"},
	{"dot-stuffed lines with bare LF", "Subject: hi\n\n.\n..\n.leading\nplain\n"},
	{"dot-stuffed last line with no line break", "Subject: hi\r\n\r\n.last"},
	{"dot-stuffed under an 8-bit header", "Subject: Grüße\r\n\r\n.\r\n.leading\r\n"},
	{"only line breaks", "\n"},
	{"only a CR", "\r"},
}

// STAT, LIST and the RETR status line all report the octets RETR sends, before dot-stuffing
// and the final terminator (RFC 1939 sections 5 and 11), whatever line endings are stored.
func TestSizesMatchWhatRetrSends(t *testing.T) {
	for _, tc := range sizeShapes {
		for _, utf8 := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/utf8=%v", tc.name, utf8), func(t *testing.T) {
				c := dialStore(t, tc.raw)
				if utf8 {
					c.do("UTF8")
				}
				c.do("USER jorg@example.test")
				c.do("PASS pw")

				var stat, listed, listedAll, retr, count int
				if _, err := fmt.Sscanf(c.do("STAT"), "+OK 1 %d", &stat); err != nil {
					t.Fatalf("STAT: %v", err)
				}
				if _, err := fmt.Sscanf(c.do("LIST 1"), "+OK 1 %d", &listed); err != nil {
					t.Fatalf("LIST 1: %v", err)
				}
				if _, err := fmt.Sscanf(c.multi("LIST"), "1 %d\r\n", &listedAll); err != nil {
					t.Fatalf("LIST: %v", err)
				}
				if _, err := fmt.Fprintf(c.conn, "RETR 1\r\n"); err != nil {
					t.Fatal(err)
				}
				if _, err := fmt.Sscanf(c.line(), "+OK %d octets", &retr); err != nil {
					t.Fatalf("RETR: %v", err)
				}
				for {
					l := c.line()
					if l == ".\r\n" {
						break
					}
					count += len(strings.TrimPrefix(l, "."))
				}
				if listed != count || listedAll != count || retr != count || stat != count {
					t.Fatalf("stored %q: STAT %d, LIST 1 %d, LIST %d, RETR status %d, but RETR sent %d octets",
						tc.raw, stat, listed, listedAll, retr, count)
				}
			})
		}
	}
}

func retrieved(t *testing.T, raw string) (message string, listed, sent int) {
	t.Helper()
	return signedInRetrieve(t, dialStore(t, raw))
}

func signedInRetrieve(t *testing.T, c *pop3Client) (message string, listed, sent int) {
	t.Helper()
	c.do("USER jorg@example.test")
	c.do("PASS pw")
	if _, err := fmt.Sscanf(c.do("LIST 1"), "+OK 1 %d", &listed); err != nil {
		t.Fatalf("LIST: %v", err)
	}
	if _, err := fmt.Fprintf(c.conn, "RETR 1\r\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscanf(c.line(), "+OK %d octets", &sent); err != nil {
		t.Fatalf("RETR: %v", err)
	}
	var b strings.Builder
	for {
		l := c.line()
		if l == ".\r\n" {
			return b.String(), listed, sent
		}
		b.WriteString(strings.TrimPrefix(l, "."))
	}
}

func TestRetrSendsEveryLineInCRLF(t *testing.T) {
	for name, tc := range map[string]struct{ raw, sent, utf8 string }{
		"bare LF, ASCII header": {raw: "Subject: hi\n\nbody\nmore\n", sent: "Subject: hi\r\n\r\nbody\r\nmore\r\n"},
		"bare LF in the body":   {raw: "Subject: hi\r\n\r\nbody\nmore\r\n", sent: "Subject: hi\r\n\r\nbody\r\nmore\r\n"},
		"bare LF, 8-bit header": {
			raw:  "Subject: J\xc3\xb6\n\nbody\nmore\n",
			sent: "Subject: =?utf-8?q?J=C3=B6?=\r\n\r\nbody\r\nmore\r\n",
			utf8: "Subject: J\xc3\xb6\r\n\r\nbody\r\nmore\r\n",
		},
		"no last break": {raw: "Subject: hi\r\n\r\nbody", sent: "Subject: hi\r\n\r\nbody\r\n"},
		"no last break, 8-bit": {
			raw:  "Subject: J\xc3\xb6\r\n\r\nbody",
			sent: "Subject: =?utf-8?q?J=C3=B6?=\r\n\r\nbody\r\n",
			utf8: "Subject: J\xc3\xb6\r\n\r\nbody\r\n",
		},
		"a CR at the end": {raw: "Subject: hi\r\n\r\nbody\r", sent: "Subject: hi\r\n\r\nbody\r\r\n"},
		"a bare CR stays": {raw: "Subject: hi\r\n\r\na\rb\r\n", sent: "Subject: hi\r\n\r\na\rb\r\n"},
		"nothing to add":  {raw: "Subject: hi\r\n\r\nbody\r\n", sent: "Subject: hi\r\n\r\nbody\r\n"},
		"8-bit boundary, no last break": {
			raw:  "Subject: J\xc3\xb6\r\nContent-Type: multipart/mixed; boundary=\"\xc3\xb6\"\r\n\r\n--\xc3\xb6\r\n\r\nx\r\n--\xc3\xb6--",
			sent: "Subject: =?utf-8?q?J=C3=B6?=\r\nContent-Type: multipart/mixed;\r\n boundary*=UTF-8''%C3%B6\r\n\r\n--\xc3\xb6\r\n\r\nx\r\n--\xc3\xb6--\r\n",
			utf8: "Subject: J\xc3\xb6\r\nContent-Type: multipart/mixed; boundary=\"\xc3\xb6\"\r\n\r\n--\xc3\xb6\r\n\r\nx\r\n--\xc3\xb6--\r\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			message, listed, sent := retrieved(t, tc.raw)
			if message != tc.sent {
				t.Fatalf("RETR sent %q, want %q", message, tc.sent)
			}
			requireSameSize(t, message, listed, sent)

			want := tc.utf8
			if want == "" {
				want = tc.sent
			}
			c := dialStore(t, tc.raw)
			c.do("UTF8")
			message, listed, sent = signedInRetrieve(t, c)
			if message != want {
				t.Fatalf("after UTF8, RETR sent %q, want %q", message, want)
			}
			requireSameSize(t, message, listed, sent)
		})
	}
}

func requireSameSize(t *testing.T, message string, listed, sent int) {
	t.Helper()
	if listed != len(message) || sent != len(message) {
		t.Fatalf("LIST says %d and RETR %d, but RETR sends %d octets", listed, sent, len(message))
	}
}
