package rfctest

import (
	"strings"
	"testing"
)

// TestParameterMalformedSpellings: a field holding an 8-bit octet keeps its real type
// and every parameter's text, however its parameters are spelled. What can't stand as
// a value or name is spelled so it can: RFC 2231 for a value, %XX for a name, a
// quoted-string for 7-bit text, and an encoded word in a comment for what fits nowhere.
// The header section and BODYSTRUCTURE read the same.
//
// checklist: IMAP-BS-13
func TestParameterMalformedSpellings(t *testing.T) {
	for _, tc := range []struct{ name, stored, header, structure string }{
		{"parentheses in a value", "name=J\xc3\xb6rg (1).pdf; other=keep",
			"name*=UTF-8''J%C3%B6rg%20%281%29.pdf;\r\n other=keep",
			`("NAME" "=?UTF-8?Q?J=C3=B6rg_=281=29=2Epdf?=" "OTHER" "keep")`},
		{"parentheses at the start of a value", "name=(1) J\xc3\xb6rg.pdf",
			"name*=UTF-8''J%C3%B6rg.pdf (1)",
			`("NAME" "=?UTF-8?Q?J=C3=B6rg=2Epdf?=")`},
		{"parentheses at the end of a value", "name=J\xc3\xb6rg.pdf (1)",
			"name*=UTF-8''J%C3%B6rg.pdf (1)",
			`("NAME" "=?UTF-8?Q?J=C3=B6rg=2Epdf?=")`},
		{"8-bit parentheses in a value", "name=J (\xc3\xb6) rg.pdf",
			"name*=UTF-8''J%20%28%C3%B6%29%20rg.pdf",
			`("NAME" "=?UTF-8?Q?J_=28=C3=B6=29_rg=2Epdf?=")`},
		{"unbalanced quote", "name=J\xc3\xb6\"rg.pdf; other=keep",
			"name*=UTF-8''J%C3%B6%22rg.pdf;\r\n other=keep",
			`("NAME" "=?UTF-8?Q?J=C3=B6=22rg=2Epdf?=" "OTHER" "keep")`},
		{"opening quote that never closes", "name=\"J\xc3\xb6rg.pdf; other=keep",
			"name*=UTF-8''%22J%C3%B6rg.pdf;\r\n other=keep",
			`("NAME" "=?UTF-8?Q?=22J=C3=B6rg=2Epdf?=" "OTHER" "keep")`},
		{"unbalanced opening parenthesis", "name=J\xc3\xb6(rg.pdf; other=keep",
			"name*=UTF-8''J%C3%B6%28rg.pdf;\r\n other=keep",
			`("NAME" "=?UTF-8?Q?J=C3=B6=28rg=2Epdf?=" "OTHER" "keep")`},
		{"unbalanced closing parenthesis", "name=J\xc3\xb6)rg.pdf; other=keep",
			"name*=UTF-8''J%C3%B6%29rg.pdf;\r\n other=keep",
			`("NAME" "=?UTF-8?Q?J=C3=B6=29rg=2Epdf?=" "OTHER" "keep")`},
		{"7-bit neighbour with an unbalanced quote", "a=b\"c; name=J\xc3\xb6rg",
			"a=\"b\\\"c\";\r\n name*=UTF-8''J%C3%B6rg",
			`("A" "b\"c" "NAME" "=?UTF-8?Q?J=C3=B6rg?=")`},
		{"7-bit neighbour with text after a comment", "a=b(c)d; name=J\xc3\xb6rg",
			"a=\"b(c)d\";\r\n name*=UTF-8''J%C3%B6rg",
			`("A" "b(c)d" "NAME" "=?UTF-8?Q?J=C3=B6rg?=")`},
		{"empty value beside 8-bit", "a=; name=J\xc3\xb6rg",
			"a=\"\";\r\n name*=UTF-8''J%C3%B6rg",
			`("A" "" "NAME" "=?UTF-8?Q?J=C3=B6rg?=")`},
		{"8-bit parameter name", "n\xc3\xa4me=x; other=keep",
			"n%C3%A4me=x;\r\n other=keep",
			`("N%C3%A4ME" "x" "OTHER" "keep")`},
		{"8-bit name and value", "n\xc3\xa4me=J\xc3\xb6rg",
			"n%C3%A4me*=UTF-8''J%C3%B6rg",
			`("N%C3%A4ME" "=?UTF-8?Q?J=C3=B6rg?=")`},
		{"8-bit name in RFC 2231 form", "n\xc3\xa4me*=UTF-8''J%C3%B6rg",
			"n%C3%A4me*=UTF-8''J%C3%B6rg",
			`("N%C3%A4ME" "=?UTF-8?Q?J=C3=B6rg?=")`},
		{"8-bit name not UTF-8", "n\xe4me=x",
			"n%E4me=x",
			`("N%E4ME" "x")`},
		{"name that isn't a token", "n\xc3\xa4 \"me=x",
			"n%C3%A4%20%22me=x",
			`("N%C3%A4%20%22ME" "x")`},
		{"8-bit name repeated", "n\xc3\xa4me=1; n\xc3\xa4me=2; other=keep",
			"n%C3%A4me=1 (=?UTF-8?Q?n=C3=A4me=3D2?=);\r\n other=keep",
			`("N%C3%A4ME" "1" "OTHER" "keep")`},
		{"8-bit name beside its spelling", "n\xc3\xa4me=1; n%C3%A4me=2",
			"n%C3%A4me=1 (=?UTF-8?Q?n=25C3=25A4me=3D2?=)",
			`("N%C3%A4ME" "1")`},
		{"parameter with no value", "J\xc3\xb6rg; other=keep",
			"J%C3%B6rg=\"\";\r\n other=keep",
			`("J%C3%B6RG" "" "OTHER" "keep")`},
		{"parameter with no name", "=J\xc3\xb6rg; other=keep",
			"%3DJ%C3%B6rg=\"\";\r\n other=keep",
			`("%3DJ%C3%B6RG" "" "OTHER" "keep")`},
		{"comment alone between two", "a=b; (B\xc3\xbcro); other=keep",
			"a=b (=?UTF-8?Q?B=C3=BCro?=);\r\n other=keep",
			`("A" "b" "OTHER" "keep")`},
		{"7-bit parameter repeated", "a=1; a=2; name=J\xc3\xb6rg",
			"a=1 (=?UTF-8?Q?a=3D2?=);\r\n name*=UTF-8''J%C3%B6rg",
			`("A" "1" "NAME" "=?UTF-8?Q?J=C3=B6rg?=")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "Content-Type: application/pdf; " + tc.stored + "\r\n\r\nbody\r\n"
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			_, got := fetchLiteral(t, c, "BODY.PEEK[HEADER]")
			if want := "Content-Type: application/pdf;\r\n " + tc.header + "\r\n\r\n"; got != want {
				t.Fatalf("HEADER = %q\n  want %q", got, want)
			}
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), `"APPLICATION" "PDF" `+tc.structure)
			requireHolds(t, fetchMetadata(t, c, "BODY"), `"APPLICATION" "PDF" `+tc.structure)
		})
	}
}

// TestParameterMalformedType: an unbalanced delimiter after the type doesn't cost the
// type. Its text goes on as a comment that closes, since nothing can read it as more.
//
// checklist: IMAP-BS-13
func TestParameterMalformedType(t *testing.T) {
	for _, tc := range []struct{ name, stored, header string }{
		{"comment alone", "application/pdf; (B\xc3\xbcro); other=keep",
			"application/pdf (=?UTF-8?Q?B=C3=BCro?=);\r\n other=keep"},
		{"opening parenthesis", "application/pdf (B\xc3\xbc; other=keep",
			"application/pdf (=?UTF-8?Q?=28B=C3=BC?=);\r\n other=keep"},
		{"closing parenthesis", "application/pdf ) B\xc3\xbc; other=keep",
			"application/pdf (=?UTF-8?Q?=29_B=C3=BC?=);\r\n other=keep"},
		{"opening quote", "application/pdf \"B\xc3\xbc; other=keep",
			"application/pdf (=?UTF-8?Q?=22B=C3=BC?=);\r\n other=keep"},
		{"7-bit text beside an 8-bit value", "application/pdf (B; other=J\xc3\xb6",
			"application/pdf (=?UTF-8?Q?=28B?=);\r\n other*=UTF-8''J%C3%B6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fetchClient(t, sevenBitBox("Content-Type: "+tc.stored+"\r\n\r\nbody\r\n"), "EXAMINE")
			_, got := fetchLiteral(t, c, "BODY.PEEK[HEADER]")
			if want := "Content-Type: " + tc.header + "\r\n\r\n"; got != want {
				t.Fatalf("HEADER = %q\n  want %q", got, want)
			}
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), `"APPLICATION" "PDF" ("OTHER"`)
		})
	}
}

// TestParameterMalformedTypeStaysDefault: text that isn't a type, or a split one, still
// takes the default, as the same text does in a 7-bit field (IMAP-BS-21, IMAP-BS-23).
//
// checklist: IMAP-BS-13
func TestParameterMalformedTypeStaysDefault(t *testing.T) {
	for _, stored := range []string{
		"te(comment)xt/html; name=J\xc3\xb6", "text/plain/extra; name=J\xc3\xb6",
		"application; name=J\xc3\xb6", "(only a comment); name=J\xc3\xb6",
		"appl\xc3\xb6/pdf; name=x",
	} {
		t.Run(stored, func(t *testing.T) {
			c := fetchClient(t, sevenBitBox("Content-Type: "+stored+"\r\n\r\nbody\r\n"), "EXAMINE")
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), `("TEXT" "PLAIN" ("CHARSET" "US-ASCII")`)
			_, header := fetchLiteral(t, c, "BODY.PEEK[HEADER]")
			require7bit(t, "HEADER", header)
		})
	}
}

// TestParameterMalformedDisposition: Content-Disposition reads the same way.
//
// checklist: IMAP-BS-13
func TestParameterMalformedDisposition(t *testing.T) {
	for _, tc := range []struct{ name, stored, header, structure string }{
		{"parentheses in a value", "attachment; filename=J\xc3\xb6rg (1).pdf; size=3",
			"attachment;\r\n filename*=UTF-8''J%C3%B6rg%20%281%29.pdf;\r\n size=3",
			`("ATTACHMENT" ("FILENAME" "=?UTF-8?Q?J=C3=B6rg_=281=29=2Epdf?=" "SIZE" "3"))`},
		{"unbalanced quote", "attachment; filename=J\xc3\xb6\"rg.pdf; size=3",
			"attachment;\r\n filename*=UTF-8''J%C3%B6%22rg.pdf;\r\n size=3",
			`("ATTACHMENT" ("FILENAME" "=?UTF-8?Q?J=C3=B6=22rg=2Epdf?=" "SIZE" "3"))`},
		{"8-bit name", "attachment; f\xc3\xa4le=x; size=3",
			"attachment;\r\n f%C3%A4le=x;\r\n size=3",
			`("ATTACHMENT" ("F%C3%A4LE" "x" "SIZE" "3"))`},
		{"unbalanced after the type", "attachment (B\xc3\xbc; size=3",
			"attachment (=?UTF-8?Q?=28B=C3=BC?=);\r\n size=3",
			`("ATTACHMENT" ("SIZE" "3"))`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "Content-Disposition: " + tc.stored + "\r\n\r\nbody\r\n"
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			_, got := fetchLiteral(t, c, "BODY.PEEK[HEADER]")
			if want := "Content-Disposition: " + tc.header + "\r\n\r\n"; got != want {
				t.Fatalf("HEADER = %q\n  want %q", got, want)
			}
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), tc.structure)
		})
	}
}

// TestParameterMalformedAtEveryLevel: the same in a part, in an encapsulated message,
// and in a part of one, with sections and data converted for the real type.
//
// checklist: IMAP-BS-13
func TestParameterMalformedAtEveryLevel(t *testing.T) {
	const fields = "Content-Type: application/pdf; name=J\xc3\xb6\"rg (1).pdf; n\xc3\xa4me=x\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\ndata\xff\r\n"
	const typed = `"APPLICATION" "PDF" ("N%C3%A4ME" "x" "NAME" "=?UTF-8?Q?J=C3=B6=22rg_=281=29=2Epdf?=")`
	for name, raw := range map[string]string{
		"top":          fields,
		"part":         "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + fields + "--B--\r\n",
		"encapsulated": "Content-Type: message/rfc822\r\n\r\n" + fields,
		"part in encapsulated": "Content-Type: message/rfc822\r\n\r\n" +
			"Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + fields + "--B--\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), typed, `"BASE64"`)
			_, whole := fetchLiteral(t, c, "BODY.PEEK[]")
			require7bit(t, "BODY[]", whole)
			requireHolds(t, whole, "name*=UTF-8''J%C3%B6%22rg%20%281%29.pdf;\r\n n%C3%A4me=x\r\n", "\r\n\r\nZGF0Yf8")
		})
	}
}

// TestParameterMalformedInUTF8Session: after UTF8=ACCEPT the field stays as stored, and
// the structure holds the real type and the text as UTF-8 literals, name included. A
// value that isn't UTF-8 is converted as in any other session.
//
// checklist: IMAP-BS-13
func TestParameterMalformedInUTF8Session(t *testing.T) {
	const stored = "Content-Type: application/pdf; name=J\xc3\xb6\"rg (1).pdf; n\xc3\xa4me=x; other=keep\r\n\r\nbody\r\n"
	framing, literals := fetchUTF8Metadata(t, stored, "BODYSTRUCTURE")
	requireHolds(t, framing, `"APPLICATION" "PDF"`, `"OTHER" "keep"`)
	for _, want := range []string{"J\xc3\xb6\"rg (1).pdf", "N\xc3\xa4ME"} {
		if !strings.Contains(literals, want) {
			t.Fatalf("%q lacks %q", literals, want)
		}
	}
	if _, header := fetchUTF8Metadata(t, stored, "BODY.PEEK[HEADER]"); !strings.HasPrefix(stored, header) {
		t.Fatalf("HEADER = %q, want the stored field", header)
	}

	const latin = "Content-Type: application/pdf; name=J\xf6\"rg (1).pdf; n\xe4me=x\r\n\r\nbody\r\n"
	framing, _ = fetchUTF8Metadata(t, latin, "BODYSTRUCTURE")
	requireHolds(t, framing, `"PDF" ("N%E4ME" "x" "NAME" "=?UNKNOWN-8BIT?Q?J=F6=22rg_=281=29=2Epdf?=")`)
}
