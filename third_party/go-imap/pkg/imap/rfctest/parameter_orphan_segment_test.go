package rfctest

import (
	"strings"
	"testing"
)

// TestParameterWithoutFirstSegment: mime.ParseMediaType drops a continued parameter
// whose first segment is missing, which leaves its text only in the header section.
// BODYSTRUCTURE keeps what was sent under the parameter's name, in segment order. Nothing
// stands in for what is missing: no charset is read from a later segment (RFC 2231 §4.1
// names one only in the first), so its text follows the same rule as a value naming none.
//
// checklist: IMAP-BS-13
func TestParameterWithoutFirstSegment(t *testing.T) {
	for _, tc := range []struct{ name, stored, structure string }{
		{"7-bit", "name*1=x; other=keep", `("NAME" "x" "OTHER" "keep")`},
		{"8-bit", "name*1=J\xc3\xb6; other=keep", `("NAME" "=?UTF-8?Q?J=C3=B6?=" "OTHER" "keep")`},
		{"extended", "name*1*=J%C3%B6; other=keep", `("NAME" "=?UTF-8?Q?J=C3=B6?=" "OTHER" "keep")`},
		{"not UTF-8", "name*1*=J%F6; other=keep", `("NAME" "=?UNKNOWN-8BIT?Q?J=F6?=" "OTHER" "keep")`},
		{"8-bit not UTF-8", "name*1=J\xf6; other=keep", `("NAME" "=?UNKNOWN-8BIT?Q?J=F6?=" "OTHER" "keep")`},
		{"a later segment names no charset", "name*1*=UTF-8''J%C3%B6", `("NAME" "=?UTF-8?Q?UTF-8=27=27J=C3=B6?=")`},
		{"later segments in order", "name*3=c; name*1=a; name*2=b", `("NAME" "abc")`},
		{"numbers compare as numbers", "name*10=b; name*9=a", `("NAME" "ab")`},
		{"a gap isn't filled", "name*1=a; name*4=d", `("NAME" "ad")`},
		{"plain and extended", "name*1=a; name*2*=%C3%B6", `("NAME" "=?UTF-8?Q?a=C3=B6?=")`},
		{"a number with a leading zero", "name*01=a; name*2=b", `("NAME" "ab")`},
		{"a number too long to parse", "name*99999999999999999999999=b; name*1=a", `("NAME" "ab")`},
		{"upper-case name", "NAME*1=x", `("NAME" "x")`},
		{"a name standing alone wins", "name=whole; name*1=x", `("NAME" "whole")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "Content-Type: application/pdf; " + tc.stored + "\r\n\r\nbody\r\n"
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), `"APPLICATION" "PDF" `+tc.structure)
			requireHolds(t, fetchMetadata(t, c, "BODY"), `"APPLICATION" "PDF" `+tc.structure)
		})
	}
}

// TestParameterWithoutFirstSegmentHeader: the header section reads as it did. A 7-bit
// field goes out as stored, and the 8-bit text of one that isn't stays a later segment,
// which can't name a charset.
//
// checklist: IMAP-BS-13
func TestParameterWithoutFirstSegmentHeader(t *testing.T) {
	for _, tc := range []struct{ name, stored, header string }{
		{"7-bit", "name*1=x; other=keep", "Content-Type: application/pdf; name*1=x; other=keep\r\n\r\n"},
		{"8-bit", "name*1=J\xc3\xb6; other=keep", "Content-Type: application/pdf;\r\n name*1*=J%C3%B6;\r\n other=keep\r\n\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "Content-Type: application/pdf; " + tc.stored + "\r\n\r\nbody\r\n"
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			if _, got := fetchLiteral(t, c, "BODY.PEEK[HEADER]"); got != tc.header {
				t.Fatalf("HEADER = %q\n  want %q", got, tc.header)
			}
		})
	}
}

// TestParameterWithoutFirstSegmentEverywhere: the same reading holds for a disposition,
// in a part, and in an encapsulated message.
//
// checklist: IMAP-BS-13
func TestParameterWithoutFirstSegmentEverywhere(t *testing.T) {
	const leaf = "Content-Type: application/pdf; name*1=J\xc3\xb6; other=keep\r\n\r\ndata\r\n"
	const typed = `"APPLICATION" "PDF" ("NAME" "=?UTF-8?Q?J=C3=B6?=" "OTHER" "keep")`
	for name, raw := range map[string]string{
		"part":         "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + leaf + "--B--\r\n",
		"encapsulated": "Content-Type: message/rfc822\r\n\r\n" + leaf,
	} {
		t.Run(name, func(t *testing.T) {
			requireHolds(t, fetchMetadata(t, fetchClient(t, sevenBitBox(raw), "EXAMINE"), "BODYSTRUCTURE"), typed)
		})
	}
	t.Run("disposition", func(t *testing.T) {
		raw := "Content-Type: text/plain\r\nContent-Disposition: attachment; filename*1=x; size=1\r\n\r\nbody\r\n"
		requireHolds(t, fetchMetadata(t, fetchClient(t, sevenBitBox(raw), "EXAMINE"), "BODYSTRUCTURE"),
			`("ATTACHMENT" ("FILENAME" "x" "SIZE" "1"))`)
	})
	t.Run("charset", func(t *testing.T) {
		raw := "Content-Type: text/plain; charset*1=x\r\n\r\nH\xc3\xb6\r\n"
		c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
		requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), `"TEXT" "PLAIN" ("CHARSET" "x")`)
		if _, body := fetchLiteral(t, c, "BODY.PEEK[TEXT]"); body != "H\xc3\xb6\r\n" {
			t.Fatalf("TEXT = %q", body)
		}
	})
	t.Run("UTF-8 session", func(t *testing.T) {
		framing, literals := fetchUTF8Metadata(t, "Content-Type: application/pdf; name*1=J\xc3\xb6; other=keep\r\n\r\nbody\r\n", "BODYSTRUCTURE")
		requireHolds(t, framing, `"APPLICATION" "PDF"`, `"OTHER" "keep"`)
		if !strings.Contains(literals, "J\xc3\xb6") {
			t.Fatalf("%q lacks the text", literals)
		}
	})
}

// TestBoundaryWithoutFirstSegment: a boundary isn't text to keep. Delimiter lines match
// its whole value, and a later segment is only part of one, so a multipart reads as one
// with no boundary (IMAP-BS-11) rather than splitting on a guess. On anything else it is
// a parameter like any other.
//
// checklist: IMAP-BS-13
func TestBoundaryWithoutFirstSegment(t *testing.T) {
	const body = "--x\r\nContent-Type: application/pdf\r\n\r\ndata\r\n--x--\r\n"
	got := fetchMetadata(t, fetchClient(t, sevenBitBox("Content-Type: multipart/mixed; boundary*1=x\r\n\r\n"+body), "EXAMINE"), "BODYSTRUCTURE")
	requireHolds(t, got, `("TEXT" "PLAIN" ("CHARSET" "US-ASCII")`)
	if strings.Contains(got, `"MIXED"`) {
		t.Fatalf("split on part of a boundary: %q", got)
	}
	got = fetchMetadata(t, fetchClient(t, sevenBitBox("Content-Type: application/pdf; boundary*1=x\r\n\r\ndata\r\n"), "EXAMINE"), "BODYSTRUCTURE")
	requireHolds(t, got, `"APPLICATION" "PDF" ("BOUNDARY" "x")`)
}
