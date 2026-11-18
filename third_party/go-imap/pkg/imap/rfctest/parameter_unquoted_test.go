package rfctest

import (
	"strings"
	"testing"
)

// An unquoted 8-bit value is the case in these tests: a token may not hold an 8-bit
// octet (RFC 2045 §5.1), and mime.ParseMediaType refuses the whole field for it.
const unquotedFields = "Content-Type: application/pdf; name=J\xc3\xb6rg.pdf; other=keep\r\n" +
	"Content-Disposition: attachment; filename=J\xc3\xb6rg.pdf; size=3\r\n" +
	"Content-Transfer-Encoding: 8bit\r\n\r\ndata\xff\r\n"

// TestParameterUnquotedEightBit: the entity keeps its real type and its other
// parameters, and the value reads as if it were quoted, at every level of a message.
//
// checklist: IMAP-BS-13
func TestParameterUnquotedEightBit(t *testing.T) {
	const typed = `"APPLICATION" "PDF" ("NAME" "=?UTF-8?Q?J=C3=B6rg=2Epdf?=" "OTHER" "keep")`
	const disposition = `("ATTACHMENT" ("FILENAME" "=?UTF-8?Q?J=C3=B6rg=2Epdf?=" "SIZE" "3"))`
	for name, raw := range map[string]string{
		"top":          unquotedFields,
		"part":         "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + unquotedFields + "--B--\r\n",
		"encapsulated": "Content-Type: message/rfc822\r\n\r\n" + unquotedFields,
		"part in encapsulated": "Content-Type: message/rfc822\r\n\r\n" +
			"Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + unquotedFields + "--B--\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			requireHolds(t, fetchMetadata(t, c, "BODY"), typed)
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), typed, disposition)
		})
	}
}

// TestParameterUnquotedSections: sections describe the same type as BODYSTRUCTURE. The
// header keeps every parameter and spells the value in RFC 2231, and the 8-bit data of
// what is not text goes out BASE64 rather than as text naming no charset.
//
// checklist: IMAP-BS-13
func TestParameterUnquotedSections(t *testing.T) {
	const header = "Content-Type: application/pdf;\r\n name*=UTF-8''J%C3%B6rg.pdf;\r\n other=keep\r\n" +
		"Content-Disposition: attachment;\r\n filename*=UTF-8''J%C3%B6rg.pdf;\r\n size=3\r\n"
	raw := "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + unquotedFields + "--B--\r\n"

	c := fetchClient(t, sevenBitBox(unquotedFields), "EXAMINE")
	_, got := fetchLiteral(t, c, "BODY.PEEK[HEADER]")
	if want := header + "Content-Transfer-Encoding: base64\r\n\r\n"; got != want {
		t.Fatalf("HEADER = %q\n  want %q", got, want)
	}
	_, got = fetchLiteral(t, c, "BODY.PEEK[TEXT]")
	if want := "ZGF0Yf8NCg==\r\n"; got != want {
		t.Fatalf("TEXT = %q, want %q", got, want)
	}

	c = fetchClient(t, sevenBitBox(raw), "EXAMINE")
	_, got = fetchLiteral(t, c, "BODY.PEEK[1.MIME]")
	if want := header + "Content-Transfer-Encoding: base64\r\n\r\n"; got != want {
		t.Fatalf("1.MIME = %q\n  want %q", got, want)
	}
}

// TestParameterUnquotedSpellings: what an unquoted value can hold, and that a value
// not in UTF-8 goes out as UNKNOWN-8BIT like a quoted one.
//
// checklist: IMAP-BS-13
func TestParameterUnquotedSpellings(t *testing.T) {
	for _, tc := range []struct{ name, stored, header, structure string }{
		{"not UTF-8", "Content-Type: application/pdf; name=J\xf6rg.pdf",
			"Content-Type: application/pdf;\r\n name*=UNKNOWN-8BIT''J%F6rg.pdf",
			`"PDF" ("NAME" "=?UNKNOWN-8BIT?Q?J=F6rg=2Epdf?=")`},
		{"white space", "Content-Type: application/pdf; name=J\xc3\xb6rg M\xc3\xbcller.pdf; a=b",
			"Content-Type: application/pdf;\r\n name*=UTF-8''J%C3%B6rg%20M%C3%BCller.pdf;\r\n a=b",
			`("A" "b" "NAME" "=?UTF-8?Q?J=C3=B6rg_M=C3=BCller=2Epdf?=")`},
		{"backslash", "Content-Type: application/pdf; name=C:\\J\xc3\xb6rg.pdf",
			"Content-Type: application/pdf;\r\n name*=UTF-8''C%3A%5CJ%C3%B6rg.pdf",
			`("NAME" "=?UTF-8?Q?C=3A=5CJ=C3=B6rg=2Epdf?=")`},
		{"quoted part", "Content-Type: application/pdf; name=\"J\xc3\xb6rg\".pdf",
			"Content-Type: application/pdf;\r\n name*=UTF-8''%22J%C3%B6rg%22.pdf",
			`("NAME" "=?UTF-8?Q?=22J=C3=B6rg=22=2Epdf?=")`},
		{"comment after", "Content-Type: application/pdf; name=J\xc3\xb6rg.pdf (B\xc3\xbcro); a=b",
			"Content-Type: application/pdf;\r\n name*=UTF-8''J%C3%B6rg.pdf (=?UTF-8?Q?B=C3=BCro?=);\r\n a=b",
			`("A" "b" "NAME" "=?UTF-8?Q?J=C3=B6rg=2Epdf?=")`},
		{"beside its extended form", "Content-Type: application/pdf; name=J\xc3\xb6rg; name*=windows-1252''J%F6rg",
			"Content-Type: application/pdf;\r\n name=\"=?UTF-8?Q?J=C3=B6rg?=\";\r\n name*=windows-1252''J%F6rg",
			`("NAME" "=?windows-1252?Q?J=F6rg?=")`},
		{"extended", "Content-Type: application/pdf; name*=windows-1252''J\xf6rg.pdf",
			"Content-Type: application/pdf;\r\n name*=windows-1252''J%F6rg.pdf",
			`("NAME" "=?windows-1252?Q?J=F6rg=2Epdf?=")`},
		{"extended with white space", "Content-Type: application/pdf; name*=windows-1252''J\xf6 rg",
			"Content-Type: application/pdf;\r\n name*=\"windows-1252''J%F6 rg\"",
			`("NAME" "=?windows-1252?Q?J=F6_rg?=")`},
		{"first segment", "Content-Type: application/pdf; name*0=J\xc3\xb6; name*1=rg",
			"Content-Type: application/pdf;\r\n name*0*=UTF-8''J%C3%B6;\r\n name*1=rg",
			`("NAME" "=?UTF-8?Q?J=C3=B6rg?=")`},
		{"later segment", "Content-Type: application/pdf; name*0=J; name*1=\xc3\xb6rg",
			"Content-Type: application/pdf;\r\n name*0=J;\r\n name*1*=%C3%B6rg",
			`("NAME" "=?UTF-8?Q?J=C3=B6rg?=")`},
		{"no-break space at the end", "Content-Type: application/pdf; name=J\xc3\xb6rg\xc2\xa0",
			"Content-Type: application/pdf;\r\n name*=UTF-8''J%C3%B6rg%C2%A0",
			`("NAME" "=?UTF-8?Q?J=C3=B6rg=C2=A0?=")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.stored + "\r\n\r\nbody\r\n"
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			_, got := fetchLiteral(t, c, "BODY.PEEK[HEADER]")
			if got != tc.header+"\r\n\r\n" {
				t.Fatalf("HEADER = %q\n  want %q", got, tc.header+"\r\n\r\n")
			}
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), `"APPLICATION" "PDF"`, tc.structure)
		})
	}
}

// TestParameterUnquotedInUTF8Session: after UTF8=ACCEPT the field stays as stored and
// the structure holds the type, the other parameters and the value as a UTF-8 literal.
// A value that is not UTF-8 is converted as in any other session.
//
// checklist: IMAP-BS-13
func TestParameterUnquotedInUTF8Session(t *testing.T) {
	const stored = "Content-Type: application/pdf; name=J\xc3\xb6rg.pdf; other=keep\r\n\r\nbody\r\n"
	framing, literals := fetchUTF8Metadata(t, stored, "BODYSTRUCTURE")
	requireHolds(t, framing, `"APPLICATION" "PDF"`, `"OTHER" "keep"`)
	if !strings.Contains(literals, "J\xc3\xb6rg.pdf") {
		t.Fatalf("no UTF-8 literal in %q", literals)
	}
	if _, header := fetchUTF8Metadata(t, stored, "BODY.PEEK[HEADER]"); !strings.HasPrefix(stored, header) {
		t.Fatalf("HEADER = %q, want the stored field", header)
	}

	const latin = "Content-Type: application/pdf; name=J\xf6rg.pdf; other=keep\r\n\r\nbody\r\n"
	framing, _ = fetchUTF8Metadata(t, latin, "BODYSTRUCTURE")
	requireHolds(t, framing, `"PDF" ("NAME" "=?UNKNOWN-8BIT?Q?J=F6rg=2Epdf?=" "OTHER" "keep")`)
}
