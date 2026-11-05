package rfctest

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// twoParts is a multipart body with the given delimiter text: one text part and one
// PDF, so a reader that can't split it loses a type and a size.
func twoParts(delimiter string) string {
	return "--" + delimiter + "\r\nContent-Type: text/plain\r\n\r\nx\r\n" +
		"--" + delimiter + "\r\nContent-Type: application/pdf; name=a\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nQUJD\r\n" +
		"--" + delimiter + "--\r\n"
}

// splitWithReader splits a section the way a client does, with the boundary the
// header section names and nothing else, and returns each part's body.
func splitWithReader(t *testing.T, header, text string) (boundary string, parts []string) {
	t.Helper()
	field := regexp.MustCompile(`(?is)Content-Type:(.*?)\r\n(\r\n|[^ \t])`).FindStringSubmatch(header)
	if field == nil {
		t.Fatalf("no Content-Type in %q", header)
	}
	_, params, err := mime.ParseMediaType(strings.ReplaceAll(field[1], "\r\n", ""))
	if err != nil {
		t.Fatalf("header %q: %v", header, err)
	}
	boundary = params["boundary"]
	reader := multipart.NewReader(strings.NewReader(text), boundary)
	for {
		part, err := reader.NextRawPart()
		if err != nil {
			return boundary, parts
		}
		body, _ := io.ReadAll(part)
		parts = append(parts, string(body))
	}
}

// TestBoundaryEightBitSplits: the boundary the header names and every delimiter line
// match in the octets sent, however the boundary was stored, so the parts keep their
// type and size. BODY[], sections, partial offsets and RFC822.SIZE count one form.
//
// checklist: IMAP-BS-04
func TestBoundaryEightBitSplits(t *testing.T) {
	for _, tc := range []struct{ name, stored, delimiter, want string }{
		{"quoted", "boundary=\"J\xc3\xb6\"", "J\xc3\xb6", "J=C3=B6"},
		{"unquoted", "boundary=J\xc3\xb6", "J\xc3\xb6", "J=C3=B6"},
		{"not UTF-8", "boundary=\"J\xf6\"", "J\xf6", "J=F6"},
		{"RFC 2231", "boundary*=UTF-8''J%C3%B6", "J\xc3\xb6", "J=C3=B6"},
		{"RFC 2231 in ISO-8859-1", "boundary*=ISO-8859-1''J%F6", "J\xf6", "J=F6"},
		{"RFC 2231 in another charset", "boundary*=windows-1252''J%F6", "J\xf6", "J=F6"},
		{"RFC 2231 continued", "boundary*0*=UTF-8''J%C3; boundary*1*=%B6", "J\xc3\xb6", "J=C3=B6"},
		{"not a boundary character", "boundary=\"J\xc3\xb6 \\\"x\"", "J\xc3\xb6 \"x", "J=C3=B6=20=22x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "Content-Type: multipart/mixed; " + tc.stored + "\r\n\r\n" + twoParts(tc.delimiter)
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")

			_, header := fetchLiteral(t, c, "BODY.PEEK[HEADER]")
			if want := "Content-Type: multipart/mixed;\r\n boundary=\"" + tc.want + "\"\r\n\r\n"; header != want {
				t.Fatalf("HEADER = %q\n  want %q", header, want)
			}
			_, text := fetchLiteral(t, c, "BODY.PEEK[TEXT]")
			if want := twoParts(tc.want); text != want {
				t.Fatalf("TEXT = %q\n  want %q", text, want)
			}
			framing, whole := fetchLiteral(t, c, "(RFC822.SIZE BODY.PEEK[])")
			if whole != header+text {
				t.Fatalf("BODY[] = %q\n  want %q", whole, header+text)
			}
			require7bit(t, "BODY[]", whole)
			if want := "RFC822.SIZE " + strconv.Itoa(len(whole)); !strings.Contains(framing, want) {
				t.Fatalf("%q lacks %q", framing, want)
			}
			_, partial := fetchLiteral(t, c, "BODY.PEEK[]<20.30>")
			if partial != whole[20:50] {
				t.Fatalf("partial = %q, want %q", partial, whole[20:50])
			}

			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"),
				`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1`,
				`("APPLICATION" "PDF" ("NAME" "a") NIL NIL "BASE64" 4`,
				fmt.Sprintf(`"MIXED" ("BOUNDARY" "%s")`, tc.want))
			if _, got := fetchLiteral(t, c, "BODY.PEEK[1]"); got != "x" {
				t.Fatalf("part 1 = %q", got)
			}
			if _, got := fetchLiteral(t, c, "BODY.PEEK[2]"); got != "QUJD" {
				t.Fatalf("part 2 = %q", got)
			}
			if _, parts := splitWithReader(t, header, text); len(parts) != 2 {
				t.Fatalf("a reader found %d parts in %q", len(parts), text)
			}
		})
	}
}

// TestBoundaryEightBitKeepsOtherParameters: only the boundary changes, and a stored
// comment and an 8-bit neighbour follow the rules of any other field.
//
// checklist: IMAP-BS-04
func TestBoundaryEightBitKeepsOtherParameters(t *testing.T) {
	raw := "Content-Type: multipart/mixed; first=\"a b\"; boundary=\"J\xc3\xb6\" (B\xc3\xbcro); name=J\xc3\xb6rg\r\n\r\n" +
		twoParts("J\xc3\xb6")
	c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
	_, header := fetchLiteral(t, c, "BODY.PEEK[HEADER]")
	if want := "Content-Type: multipart/mixed;\r\n first=\"a b\";\r\n boundary=\"J=C3=B6\" (=?UTF-8?Q?B=C3=BCro?=);\r\n" +
		" name*=UTF-8''J%C3%B6rg\r\n\r\n"; header != want {
		t.Fatalf("HEADER = %q\n  want %q", header, want)
	}
	requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), `("APPLICATION" "PDF"`,
		`("BOUNDARY" "J=C3=B6" "FIRST" "a b" "NAME" "=?UTF-8?Q?J=C3=B6rg?=")`)
}

// TestBoundaryEightBitInPartAndEncapsulated: the same at every level of a message.
//
// checklist: IMAP-BS-04
func TestBoundaryEightBitInPartAndEncapsulated(t *testing.T) {
	inner := "Content-Type: multipart/mixed; boundary=\"J\xc3\xb6\"\r\n\r\n" + twoParts("J\xc3\xb6")
	for name, raw := range map[string]string{
		"part":         "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + inner + "\r\n--B--\r\n",
		"encapsulated": "Content-Type: message/rfc822\r\n\r\n" + inner,
	} {
		t.Run(name, func(t *testing.T) {
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			_, whole := fetchLiteral(t, c, "BODY.PEEK[]")
			require7bit(t, "BODY[]", whole)
			requireHolds(t, whole, "boundary=\"J=C3=B6\"", twoParts("J=C3=B6"))
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), `("APPLICATION" "PDF" ("NAME" "a") NIL NIL "BASE64" 4`)
			if _, got := fetchLiteral(t, c, "BODY.PEEK[1.2]"); got != "QUJD" {
				t.Fatalf("part 1.2 = %q", got)
			}
		})
	}
}

// TestBoundaryNeverCollides: the converted boundary, or a boundary that conversion
// makes collide, moves away from any line that would otherwise split where the stored
// message doesn't. The parts a client finds are the parts the stored message has.
//
// checklist: IMAP-BS-04
func TestBoundaryNeverCollides(t *testing.T) {
	text := func(body string) string { return "Content-Type: text/plain; charset=utf-8\r\n\r\n" + body }
	stored := func(boundary string, preamble string, parts ...string) string {
		var b strings.Builder
		b.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n" + preamble)
		for _, part := range parts {
			b.WriteString("--" + boundary + "\r\n" + part + "\r\n")
		}
		b.WriteString("--" + boundary + "--\r\n")
		return b.String()
	}
	const spelled = "--J=C3=B6"
	for _, tc := range []struct {
		name, raw string
		parts     int
	}{
		{"line in a part", stored("J\xc3\xb6", "", text(spelled+"\r\nkept"), text("y")), 2},
		{"close line in a part", stored("J\xc3\xb6", "", text(spelled+"--\r\nkept"), text("y")), 2},
		{"longer line in a part", stored("J\xc3\xb6", "", text(spelled+"x\r\nkept"), text("y")), 2},
		{"line in the preamble", stored("J\xc3\xb6", spelled+"\r\n", text("x"), text("y")), 2},
		{"line in quoted-printable", stored("J\xc3\xb6", "",
			"Content-Transfer-Encoding: quoted-printable\r\n\r\n"+spelled+"\r\nkept", text("y")), 2},
		{"inner multipart spelled the same", stored("J\xc3\xb6", "",
			"Content-Type: multipart/mixed; boundary=\"J=C3=B6\"\r\n\r\n--J=C3=B6\r\n\r\nz\r\n--J=C3=B6--", text("y")), 2},
		{"outer spelled as the inner converts", stored("J=C3=B6", "",
			"Content-Type: multipart/mixed; boundary=\"J\xc3\xb6\"\r\n\r\n--J\xc3\xb6\r\n\r\nz\r\n--J\xc3\xb6--", text("y")), 2},
		{"text converted into a delimiter", stored("J=C3=B6", "",
			"Content-Type: text/plain\r\n\r\n--J\xc3\xb6\r\nkept", text("y")), 2},
		{"preamble converted into a delimiter", stored("J=C3=B6", "--J\xc3\xb6\r\n", text("x"), text("y")), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fetchClient(t, sevenBitBox(tc.raw), "EXAMINE")
			_, header := fetchLiteral(t, c, "BODY.PEEK[HEADER]")
			_, body := fetchLiteral(t, c, "BODY.PEEK[TEXT]")
			require7bit(t, "HEADER", header)
			boundary, parts := splitWithReader(t, header, body)
			if len(parts) != tc.parts {
				t.Fatalf("boundary %q gave %d parts in %q", boundary, len(parts), body)
			}
			if strings.Count(body, "--"+boundary+"\r\n") != tc.parts || !strings.Contains(body, "--"+boundary+"--") {
				t.Fatalf("boundary %q, delimiters in %q", boundary, body)
			}
			if _, last := fetchLiteral(t, c, fmt.Sprintf("BODY.PEEK[%d]", tc.parts)); last != "y" {
				t.Fatalf("last part = %q in %q", last, body)
			}
			if _, none := fetchCommand(t, c, fmt.Sprintf("FETCH 1 BODY.PEEK[%d]", tc.parts+1)); !strings.Contains(none, "OK") {
				t.Fatalf("tagged %q", none)
			}
		})
	}
}

// TestBoundaryLongOrUnused: a long boundary stays within the 70 characters RFC 2046
// §5.1.1 gives one, and a stored message whose delimiter lines never match still reads
// as having no parts, even when the converted spelling appears in its body.
//
// checklist: IMAP-BS-04
func TestBoundaryLongOrUnused(t *testing.T) {
	long := strings.Repeat("\xc3\xb6", 40)
	c := fetchClient(t, sevenBitBox("Content-Type: multipart/mixed; boundary=\""+long+"\"\r\n\r\n"+twoParts(long)), "EXAMINE")
	_, header := fetchLiteral(t, c, "BODY.PEEK[HEADER]")
	_, body := fetchLiteral(t, c, "BODY.PEEK[TEXT]")
	boundary, parts := splitWithReader(t, header, body)
	if len(boundary) > 70 || len(parts) != 2 {
		t.Fatalf("boundary %q gave %d parts in %q", boundary, len(parts), body)
	}

	for name, unused := range map[string]string{
		"7-bit body": "--J=C3=B6\r\n\r\nz\r\n--J=C3=B6--\r\n",
		"8-bit body": "--J=C3=B6\r\n\r\n\xc3\xb6\r\n--J=C3=B6--\r\n",
		"no line":    "nothing here\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			c := fetchClient(t, sevenBitBox("Content-Type: multipart/mixed; boundary=\"J\xc3\xb6\"\r\n\r\n"+unused), "EXAMINE")
			_, header := fetchLiteral(t, c, "BODY.PEEK[HEADER]")
			_, body := fetchLiteral(t, c, "BODY.PEEK[TEXT]")
			require7bit(t, "HEADER", header)
			require7bit(t, "TEXT", body)
			if boundary, parts := splitWithReader(t, header, body); len(parts) != 0 {
				t.Fatalf("boundary %q gave %d parts in %q", boundary, len(parts), body)
			}
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), `(("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 0 0`)
		})
	}
}

// TestBoundaryInUTF8Session: after UTF8=ACCEPT a boundary in valid UTF-8 and its
// delimiter lines stay as stored, and a preamble still doesn't change how they match.
// Octets that aren't UTF-8 are converted, as in any other session.
//
// checklist: IMAP-BS-04
func TestBoundaryInUTF8Session(t *testing.T) {
	for name, field := range map[string]string{
		"plain":    "boundary=\"J\xc3\xb6\"",
		"RFC 2231": "boundary*=UTF-8''J%C3%B6",
	} {
		t.Run(name, func(t *testing.T) {
			raw := "Content-Type: multipart/mixed; " + field + "\r\n\r\n" + twoParts("J\xc3\xb6")
			if _, whole := fetchUTF8Metadata(t, raw, "BODY.PEEK[]"); whole != raw {
				t.Fatalf("BODY[] = %q\n  want %q", whole, raw)
			}
			framing, _ := fetchUTF8Metadata(t, raw, "BODYSTRUCTURE")
			requireHolds(t, framing, `("APPLICATION" "PDF" ("NAME" "a") NIL NIL "BASE64" 4`)
		})
	}

	raw := "Content-Type: multipart/mixed; boundary=\"J\xf6\"\r\n\r\n" + twoParts("J\xf6")
	want := "Content-Type: multipart/mixed;\r\n boundary=\"J=F6\"\r\n\r\n" + twoParts("J=F6")
	if _, whole := fetchUTF8Metadata(t, raw, "BODY.PEEK[]"); whole != want {
		t.Fatalf("BODY[] = %q\n  want %q", whole, want)
	}
	framing, _ := fetchUTF8Metadata(t, raw, "BODYSTRUCTURE")
	requireHolds(t, framing, `("APPLICATION" "PDF" ("NAME" "a") NIL NIL "BASE64" 4`, `("BOUNDARY" "J=F6")`)
}

// TestBoundaryStaysWhenNothingNeedsIt: a message already 7-bit goes out byte for byte,
// including an RFC 2231 boundary with 8-bit text that its delimiter lines don't match,
// and a 7-bit boundary is untouched beside 8-bit data.
//
// checklist: IMAP-BS-04
func TestBoundaryStaysWhenNothingNeedsIt(t *testing.T) {
	seven := "Content-Type: multipart/mixed; boundary*=UTF-8''J%C3%B6\r\n\r\n" + twoParts("J%C3%B6")
	c := fetchClient(t, sevenBitBox(seven), "EXAMINE")
	if _, whole := fetchLiteral(t, c, "BODY.PEEK[]"); whole != seven {
		t.Fatalf("BODY[] = %q\n  want %q", whole, seven)
	}

	raw := "Content-Type: multipart/mixed; boundary=\"B C\"\r\n\r\npre\xc3\xb6\r\n--B C\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n\xc3\xb6\r\n--B C--\r\n"
	c = fetchClient(t, sevenBitBox(raw), "EXAMINE")
	_, whole := fetchLiteral(t, c, "BODY.PEEK[]")
	want := "Content-Type: multipart/mixed; boundary=\"B C\"\r\n\r\npre=C3=B6\r\n--B C\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n\xc3\xb6\r\n--B C--\r\n"
	if whole != want {
		t.Fatalf("BODY[] = %q\n  want %q", whole, want)
	}
}

// TestBinaryReadsTheStoredBoundary: RFC 3516 BINARY still finds a part by the stored
// octets, so an 8-bit boundary doesn't move it.
//
// checklist: IMAP-BS-04
func TestBinaryReadsTheStoredBoundary(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=\"J\xc3\xb6\"\r\n\r\n" + twoParts("J\xc3\xb6")
	c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
	if _, got := fetchLiteral(t, c, "BINARY.PEEK[2]"); got != "ABC" {
		t.Fatalf("BINARY[2] = %q", got)
	}
}
