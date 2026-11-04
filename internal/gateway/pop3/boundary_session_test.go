package pop3

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
)

// boundaryParts is a multipart body with its delimiter left to fill in: a text part and
// a data part, each with 8-bit octets, and an epilogue that holds the stored delimiter.
const boundaryParts = "--%[1]s\r\nContent-Type: text/plain\r\n\r\nH\xc3\xb6\r\n--%[1]s\r\n" +
	"Content-Type: application/pdf\r\n\r\n\xff\xfe\r\n--%[1]s--\r\n"

// partsWant is what boundaryParts holds, as the stored message has it.
var partsWant = []string{"H\xc3\xb6", "\xff\xfe"}

// retrieved logs a session in, and returns what RETR sent and LIST and RETR reported.
func retrieved(t *testing.T, raw string) (message string, listed, sent int) {
	t.Helper()
	c := dialStore(t, raw)
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

// requireSameSize proves LIST and RETR report the octets RETR sends (RFC 1939 section 5).
func requireSameSize(t *testing.T, message string, listed, sent int) {
	t.Helper()
	if listed != len(message) || sent != len(message) {
		t.Fatalf("LIST says %d and RETR %d, but RETR sends %d octets", listed, sent, len(message))
	}
}

// splitParts reads message as a client does: the top-level header and its boundary, then
// the parts, with their octets undecoded. It fails on a header that isn't 7-bit.
func splitParts(t *testing.T, message string) []string {
	t.Helper()
	header, _, _ := strings.Cut(message, "\r\n\r\n")
	for i := range len(header) {
		if header[i] >= 0x80 {
			t.Fatalf("8-bit header: %q", header)
		}
	}
	parsed, err := mail.ReadMessage(strings.NewReader(message))
	if err != nil {
		t.Fatalf("read %q: %v", message, err)
	}
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("Content-Type %q: %v", parsed.Header.Get("Content-Type"), err)
	}
	if !strings.HasPrefix(mediaType, "multipart/") || params["boundary"] == "" {
		t.Fatalf("no boundary in %q", parsed.Header.Get("Content-Type"))
	}
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	var parts []string
	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			return parts
		}
		if err != nil {
			t.Fatalf("part %d of %q: %v", len(parts), message, err)
		}
		var data bytes.Buffer
		if _, err := io.Copy(&data, part); err != nil {
			t.Fatalf("part %d: %v", len(parts), err)
		}
		parts = append(parts, data.String())
	}
}

func requireParts(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("parts = %q, want %q", got, want)
	}
}

// RETR gives an 8-bit boundary one 7-bit spelling, in the header and on the delimiter
// lines, the spelling FETCH sends. A reader needs neither RFC 2231 nor a charset it
// knows to split the parts, each part keeps its octets, and LIST counts what RETR sends.
func TestRetrSplitsOnEightBitBoundary(t *testing.T) {
	for _, tc := range []struct{ name, stored, delimiter, field, spelled string }{
		{"quoted", `boundary="Jö"`, "Jö", `boundary="J=C3=B6"`, "J=C3=B6"},
		{"unquoted", "boundary=Jö", "Jö", `boundary="J=C3=B6"`, "J=C3=B6"},
		{"not UTF-8", "boundary=\"J\xf6\"", "J\xf6", `boundary="J=F6"`, "J=F6"},
		{"RFC 2231 beside 8-bit text", "boundary*=windows-1252''J%F6; name=Jörg", "J\xf6",
			"boundary=\"J=F6\";\r\n name*=UTF-8''J%C3%B6rg", "J=F6"},
		{"continued", "boundary*0=J; boundary*1*=%C3%B6; name=Jörg", "Jö",
			"boundary=\"J=C3=B6\";\r\n name*=UTF-8''J%C3%B6rg", "J=C3=B6"},
		{"folded", "\r\n boundary=\"Jö\"", "Jö", `boundary="J=C3=B6"`, "J=C3=B6"},
		{"other parameters", `charset=x; boundary="Jö"; other=keep`, "Jö",
			"charset=x;\r\n boundary=\"J=C3=B6\";\r\n other=keep", "J=C3=B6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "Subject: Grüße\r\nContent-Type: multipart/mixed; " + tc.stored + "\r\n\r\n" +
				fmt.Sprintf(boundaryParts, tc.delimiter)
			want := "Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\nContent-Type: multipart/mixed;\r\n " + tc.field + "\r\n\r\n" +
				fmt.Sprintf(boundaryParts, tc.spelled)
			got, listed, sent := retrieved(t, raw)
			if got != want {
				t.Fatalf("RETR\n got %q\nwant %q", got, want)
			}
			requireSameSize(t, got, listed, sent)
			requireParts(t, splitParts(t, got), partsWant)
		})
	}
}

// The new spelling can't split where the stored message doesn't, and what isn't a
// delimiter stays. A body line holding the spelling moves the boundary, and neither an
// epilogue's delimiter nor a line that only starts with the boundary is one.
func TestRetrBoundaryKeepsTheParts(t *testing.T) {
	const head = "Content-Type: multipart/mixed; boundary=\"Jö\"\r\n\r\n"

	t.Run("a line holds the new spelling", func(t *testing.T) {
		raw := head + "--Jö\r\n\r\nfirst\r\n--J=C3=B6\r\nsecond\r\n--Jö\r\n\r\nthird\r\n--Jö--\r\n"
		got, listed, sent := retrieved(t, raw)
		requireSameSize(t, got, listed, sent)
		requireParts(t, splitParts(t, got), []string{"first\r\n--J=C3=B6\r\nsecond", "third"})
		if strings.Contains(got, `boundary="J=C3=B6"`) {
			t.Fatalf("boundary collides with a body line: %q", got)
		}
	})
	t.Run("a line only starts with it", func(t *testing.T) {
		raw := head + "Präambel\r\n--Jö\r\n\r\n--Jöx\r\n--Jö--\r\nepilogue\r\n--Jö\r\n"
		want := "Content-Type: multipart/mixed;\r\n boundary=\"J=C3=B6\"\r\n\r\n" +
			"Präambel\r\n--J=C3=B6\r\n\r\n--Jöx\r\n--J=C3=B6--\r\nepilogue\r\n--Jö\r\n"
		got, listed, sent := retrieved(t, raw)
		if got != want {
			t.Fatalf("RETR\n got %q\nwant %q", got, want)
		}
		requireSameSize(t, got, listed, sent)
	})
	t.Run("too long to spell in full", func(t *testing.T) {
		long := strings.Repeat("ö", 40)
		got, listed, sent := retrieved(t, "Content-Type: multipart/mixed; boundary=\""+long+"\"\r\n\r\n"+fmt.Sprintf(boundaryParts, long))
		requireSameSize(t, got, listed, sent)
		requireParts(t, splitParts(t, got), partsWant)
	})
	t.Run("nested", func(t *testing.T) {
		// Only the top-level header is downgraded, so a part's header and its delimiter
		// lines stay as stored, and they still match each other.
		inner := "Content-Type: multipart/mixed; boundary=\"Ü\"\r\n\r\n--Ü\r\n\r\nx\r\n--Ü--"
		raw := head + "--Jö\r\n" + inner + "\r\n--Jö--\r\n"
		got, listed, sent := retrieved(t, raw)
		requireSameSize(t, got, listed, sent)
		requireParts(t, splitParts(t, got), []string{"--Ü\r\n\r\nx\r\n--Ü--"})
		if !strings.Contains(got, inner) {
			t.Fatalf("RETR %q lacks the stored part %q", got, inner)
		}
	})
	t.Run("stored with bare LF", func(t *testing.T) {
		// RETR sends every line with CRLF, so these are delimiter lines on the wire, and
		// the size counts that form.
		raw := strings.ReplaceAll(head+fmt.Sprintf(boundaryParts, "Jö"), "\r\n", "\n")
		got, listed, sent := retrieved(t, raw)
		requireSameSize(t, got, listed, sent)
		requireParts(t, splitParts(t, got), partsWant)
	})
}

// What needs no new boundary goes out as it did. The body of a message with a 7-bit
// boundary stays byte for byte, with only its header downgraded, and so does a boundary
// parameter on anything that has no delimiter lines.
func TestRetrLeavesOtherBodiesAlone(t *testing.T) {
	for _, tc := range []struct{ name, stored, want string }{
		{"7-bit boundary beside an 8-bit header",
			"Subject: Grüße\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" + fmt.Sprintf(boundaryParts, "B"),
			"Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" + fmt.Sprintf(boundaryParts, "B")},
		{"a boundary parameter on a leaf",
			"Content-Type: text/plain; boundary=\"Jö\"\r\n\r\n--Jö\r\n",
			"Content-Type: text/plain;\r\n boundary*=UTF-8''J%C3%B6\r\n\r\n--Jö\r\n"},
		{"a 7-bit header",
			"Content-Type: multipart/mixed; boundary=B\r\n\r\n" + fmt.Sprintf(boundaryParts, "B"),
			"Content-Type: multipart/mixed; boundary=B\r\n\r\n" + fmt.Sprintf(boundaryParts, "B")},
		{"a message already 7-bit",
			"Subject: hello\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n\r\nx\r\n--B--\r\n",
			"Subject: hello\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n\r\nx\r\n--B--\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, listed, sent := retrieved(t, tc.stored)
			if got != tc.want {
				t.Fatalf("RETR\n got %q\nwant %q", got, tc.want)
			}
			requireSameSize(t, got, listed, sent)
		})
	}
}

// After UTF8 the message is as stored, and TOP uses the same spelling as RETR.
func TestBoundaryInOtherPOP3Commands(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=\"Jö\"\r\n\r\n" + fmt.Sprintf(boundaryParts, "Jö")

	t.Run("after UTF8", func(t *testing.T) {
		c := dialStore(t, raw)
		c.do("UTF8")
		c.do("USER jorg@example.test")
		c.do("PASS pw")
		if got := c.multi("RETR 1"); got != raw {
			t.Fatalf("RETR\n got %q\nwant %q", got, raw)
		}
	})
	t.Run("TOP", func(t *testing.T) {
		c := dialStore(t, raw)
		c.do("USER jorg@example.test")
		c.do("PASS pw")
		want := "Content-Type: multipart/mixed;\r\n boundary=\"J=C3=B6\"\r\n\r\n--J=C3=B6\r\n"
		if got := c.multi("TOP 1 1"); got != want {
			t.Fatalf("TOP\n got %q\nwant %q", got, want)
		}
	})
}
