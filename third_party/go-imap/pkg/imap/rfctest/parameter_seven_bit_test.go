package rfctest

import (
	"strings"
	"testing"
)

// parameterStructure is what a session is told about a message with this Content-Type.
func parameterStructure(t *testing.T, field string) string {
	t.Helper()
	raw := "Content-Type: " + field + "\r\n\r\nbody\r\n"
	return fetchMetadata(t, fetchClient(t, sevenBitBox(raw), "EXAMINE"), "BODYSTRUCTURE")
}

// TestParameterRepeatedName: RFC 2045 §5.1's grammar lets a name appear twice, so this
// isn't the syntax error §5.2 gives the default for, and the entity keeps its type. The
// first value stands, as it does in a field holding an 8-bit octet, and the header section
// still holds both, because a 7-bit message goes out byte for byte.
//
// checklist: IMAP-BS-13
func TestParameterRepeatedName(t *testing.T) {
	for _, tc := range []struct{ name, field, structure string }{
		{"two values", "application/pdf; a=1; a=2", `"APPLICATION" "PDF" ("A" "1")`},
		{"beside another", "application/pdf; a=1; other=keep; a=2", `"APPLICATION" "PDF" ("A" "1" "OTHER" "keep")`},
		{"in another case", "application/pdf; a=1; A=2", `"APPLICATION" "PDF" ("A" "1")`},
		{"three values", "application/pdf; a=1; a=2; a=3", `"APPLICATION" "PDF" ("A" "1")`},
		{"the same value", "application/pdf; a=1; a=1", `"APPLICATION" "PDF" ("A" "1")`},
		{"quoted", `application/pdf; a="x;y"; a="z"`, `"APPLICATION" "PDF" ("A" "x;y")`},
		{"around a comment", "application/pdf; a=1 (one); a=2 (two)", `"APPLICATION" "PDF" ("A" "1")`},
		{"charset", "text/plain; charset=utf-8; charset=latin1", `"TEXT" "PLAIN" ("CHARSET" "utf-8")`},
		{"a segment", "application/pdf; name*0=a; name*0=b; name*1=c", `"APPLICATION" "PDF" ("NAME" "ac")`},
		{"an extended value", "application/pdf; name*=UTF-8''a; name*=UTF-8''b", `"APPLICATION" "PDF" ("NAME" "a")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "Content-Type: " + tc.field + "\r\n\r\nbody\r\n"
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), tc.structure)
			requireHolds(t, fetchMetadata(t, c, "BODY"), tc.structure)
			if _, header := fetchLiteral(t, c, "BODY.PEEK[HEADER]"); header != "Content-Type: "+tc.field+"\r\n\r\n" {
				t.Fatalf("HEADER = %q", header)
			}
		})
	}
	t.Run("boundary", func(t *testing.T) {
		raw := "Content-Type: multipart/mixed; boundary=a; boundary=b\r\n\r\n--a\r\nContent-Type: application/pdf\r\n\r\nx\r\n--b\r\n\r\ny\r\n--a--\r\n"
		c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
		requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), `"APPLICATION" "PDF"`, `"MIXED" ("BOUNDARY" "a")`)
		if _, part := fetchLiteral(t, c, "BODY.PEEK[1]"); part != "x\r\n--b\r\n\r\ny" {
			t.Fatalf("part 1 = %q", part)
		}
	})
	t.Run("disposition", func(t *testing.T) {
		raw := "Content-Type: text/plain\r\nContent-Disposition: attachment; filename=a; size=1; filename=b\r\n\r\nx\r\n"
		requireHolds(t, fetchMetadata(t, fetchClient(t, sevenBitBox(raw), "EXAMINE"), "BODYSTRUCTURE"),
			`("ATTACHMENT" ("FILENAME" "a" "SIZE" "1"))`)
	})
	t.Run("in a part", func(t *testing.T) {
		raw := "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: application/pdf; a=1; a=2\r\n\r\nx\r\n--B--\r\n"
		requireHolds(t, fetchMetadata(t, fetchClient(t, sevenBitBox(raw), "EXAMINE"), "BODYSTRUCTURE"), `"APPLICATION" "PDF" ("A" "1")`)
	})
	t.Run("encapsulated", func(t *testing.T) {
		raw := "Content-Type: message/rfc822\r\n\r\nContent-Type: application/pdf; a=1; a=2\r\n\r\nx\r\n"
		requireHolds(t, fetchMetadata(t, fetchClient(t, sevenBitBox(raw), "EXAMINE"), "BODYSTRUCTURE"), `"APPLICATION" "PDF" ("A" "1")`)
	})
}

// TestParameterRepeatedNameIsNotTheOnlyFault: a repeat doesn't excuse what §5.2 does give
// the default for.
//
// checklist: IMAP-BS-13
func TestParameterRepeatedNameIsNotTheOnlyFault(t *testing.T) {
	for _, field := range []string{
		`application/pdf; a=1; a=2; b="unclosed`,
		`application/pdf; a=1; a=2; b=x"y`,
		`application/pdf; a=1; b; a=2`,
		`application/pdf; a=1; a=2; b c=3`,
		`application/pdf; a=1; a=2"b`,
		`application/pdf; a=1; a=x y`,
		`application/pdf; a=1; a=`,
		`application; a=1; a=2`,
	} {
		t.Run(field, func(t *testing.T) {
			requireHolds(t, parameterStructure(t, field), `("TEXT" "PLAIN" ("CHARSET" "US-ASCII")`)
		})
	}
}

// TestParameterSegmentGap: a segment missing after the first closes up, as one missing
// before it does (TestParameterWithoutFirstSegment), so the later text stays in
// BODYSTRUCTURE. The first segment still names the charset of the whole (RFC 2231 §4.1).
//
// checklist: IMAP-BS-13
func TestParameterSegmentGap(t *testing.T) {
	for _, tc := range []struct{ name, stored, structure string }{
		{"one missing", "name*0=a; name*2=c", `("NAME" "ac")`},
		{"several missing", "name*0=a; name*1=b; name*5=f; name*9=j", `("NAME" "abfj")`},
		{"out of order", "name*7=c; name*0=a; name*3=b", `("NAME" "abc")`},
		{"beside another", "name*0=a; other=keep; name*2=c", `("NAME" "ac" "OTHER" "keep")`},
		{"extended", "name*0*=UTF-8''a%C3%B6; name*2*=%C3%BC", `("NAME" "=?UTF-8?Q?a=C3=B6=C3=BC?=")`},
		{"extended first", "name*0*=UTF-8''a%C3%B6; name*2=c", `("NAME" "=?UTF-8?Q?a=C3=B6c?=")`},
		{"a charset go-imap can't decode", "name*0*=windows-1252''a%F6; name*2*=%FC", `("NAME" "=?windows-1252?Q?a=F6=FC?=")`},
		{"both forms of a segment", "name*0=a; name*0*=b", `("NAME" "ab")`},
		{"8-bit", "name*0=J\xc3\xb6; name*2=c", `("NAME" "=?UTF-8?Q?J=C3=B6c?=")`},
		{"no gap", "name*0=a; name*1=b; name*2=c", `("NAME" "abc")`},
		{"a first segment outranks a plain value", "name=whole; name*0=a; name*2=c", `("NAME" "ac")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireHolds(t, parameterStructure(t, "application/pdf; "+tc.stored), `"APPLICATION" "PDF" `+tc.structure)
		})
	}
	t.Run("disposition", func(t *testing.T) {
		raw := "Content-Type: text/plain\r\nContent-Disposition: attachment; filename*0=a; filename*2=c\r\n\r\nx\r\n"
		requireHolds(t, fetchMetadata(t, fetchClient(t, sevenBitBox(raw), "EXAMINE"), "BODYSTRUCTURE"), `("ATTACHMENT" ("FILENAME" "ac"))`)
	})
}

// TestBoundaryWithSegmentGap: delimiter lines match a boundary's whole value, so a multipart
// missing a segment of one reads as having no boundary (IMAP-BS-11), as with no first segment
// (TestBoundaryWithoutFirstSegment). Whole segments still read as one, and on anything but a
// multipart it is a parameter like any other.
//
// checklist: IMAP-BS-13
func TestBoundaryWithSegmentGap(t *testing.T) {
	const body = "\r\n\r\n--ac\r\nContent-Type: application/pdf\r\n\r\ndata\r\n--ac--\r\n"
	for name, field := range map[string]string{
		"a gap":             "multipart/mixed; boundary*0=a; boundary*2=c",
		"a gap and a value": "multipart/mixed; boundary=ac; boundary*0=a; boundary*2=c",
		"a segment doubled": "multipart/mixed; boundary*0=a; boundary*0*=c",
	} {
		t.Run(name, func(t *testing.T) {
			got := fetchMetadata(t, fetchClient(t, sevenBitBox("Content-Type: "+field+body), "EXAMINE"), "BODYSTRUCTURE")
			requireHolds(t, got, `("TEXT" "PLAIN" ("CHARSET" "US-ASCII")`)
			if strings.Contains(got, `"MIXED"`) {
				t.Fatalf("split on part of a boundary: %q", got)
			}
		})
	}
	for name, field := range map[string]string{
		"whole":                 "multipart/mixed; boundary*0=a; boundary*1=c",
		"in one extended value": "multipart/mixed; boundary*=UTF-8''ac",
	} {
		t.Run(name, func(t *testing.T) {
			got := fetchMetadata(t, fetchClient(t, sevenBitBox("Content-Type: "+field+body), "EXAMINE"), "BODYSTRUCTURE")
			requireHolds(t, got, `"APPLICATION" "PDF"`, `"MIXED" ("BOUNDARY" "ac")`)
		})
	}
	t.Run("not a multipart", func(t *testing.T) {
		requireHolds(t, parameterStructure(t, "application/pdf; boundary*0=a; boundary*2=c"), `"APPLICATION" "PDF" ("BOUNDARY" "ac")`)
	})
}

// TestParameterStarInName: '*' is a token character, so a name with one that isn't RFC 2231
// form is a parameter of that name (RFC 2045 §5.1). mime.ParseMediaType reads any '*' as
// RFC 2231, and dropped the parameter from BODYSTRUCTURE.
//
// checklist: IMAP-BS-13
func TestParameterStarInName(t *testing.T) {
	for _, tc := range []struct{ name, stored, structure string }{
		{"text after the star", "name*x=y; other=keep", `("NAME*X" "y" "OTHER" "keep")`},
		{"quoted", `name*x="a b"`, `("NAME*X" "a b")`},
		{"two stars", "name**=y", `("NAME**" "y")`},
		{"a star inside a number", "name*1*2=y", `("NAME*1*2" "y")`},
		{"no base", "*=y", `("*" "y")`},
		{"no base and a number", "*0=y", `("*0" "y")`},
		{"beside the real one", "name*x=y; name*0=a; name*1=b", `("NAME" "ab" "NAME*X" "y")`},
		{"beside a plain one", "name=a; name*x=y", `("NAME" "a" "NAME*X" "y")`},
		{"repeated", "name*x=y; name*x=z", `("NAME*X" "y")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireHolds(t, parameterStructure(t, "application/pdf; "+tc.stored), `"APPLICATION" "PDF" `+tc.structure)
		})
	}
	t.Run("disposition", func(t *testing.T) {
		raw := "Content-Type: text/plain\r\nContent-Disposition: attachment; file*x=y\r\n\r\nx\r\n"
		requireHolds(t, fetchMetadata(t, fetchClient(t, sevenBitBox(raw), "EXAMINE"), "BODYSTRUCTURE"), `("ATTACHMENT" ("FILE*X" "y"))`)
	})
}

// TestMalformedSevenBitFieldStaysDefault: what RFC 2045 §5.1 can't read does take the §5.2
// default in a 7-bit field, and the header section keeps its text byte for byte. An 8-bit
// octet is what IMAP-BS-13 reads differently, not its spelling.
//
// checklist: IMAP-BS-13
func TestMalformedSevenBitFieldStaysDefault(t *testing.T) {
	for _, field := range []string{
		`application/pdf; name=a"b; other=keep`,
		`application/pdf; name=Joe (1).pdf; other=keep`,
		`application/pdf; name=Joe Smith.pdf; other=keep`,
	} {
		t.Run(field, func(t *testing.T) {
			raw := "Content-Type: " + field + "\r\n\r\nbody\r\n"
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), `("TEXT" "PLAIN" ("CHARSET" "US-ASCII")`)
			if _, whole := fetchLiteral(t, c, "BODY.PEEK[]"); whole != raw {
				t.Fatalf("BODY[] = %q", whole)
			}
		})
	}
}
