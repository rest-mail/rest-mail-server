package rfctest

import (
	"fmt"
	"testing"
)

// Header extraction is metadata: no digest generation/verification or URI
// resolution is performed. The MD5 string is RFC 1864 §2's illustrative value;
// these synthetic bodies are deliberately not claimed to match its digest.
//
// checklist: IMAP-BS-17 IMAP-BS-18
// checklist: IMAP2-RESP-213
// checklist: IMAP2-SEC-221
func TestBodyStructureMD5Location(t *testing.T) {
	const base = `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1`
	for _, tc := range []struct{ name, headers, md5, location string }{
		{"absent", "", "NIL", "NIL"},
		{"empty", "Content-MD5:\r\nContent-Location:\r\n", "NIL", "NIL"},
		{"md5-only", "content-md5: Q2hlY2sgSW50ZWdyaXR5IQ==\r\n", `"Q2hlY2sgSW50ZWdyaXR5IQ=="`, "NIL"},
		{"relative-location", "Content-Location: images/a.gif\r\n", "NIL", `"images/a.gif"`},
		{"both", "Content-MD5: Q2hlY2sgSW50ZWdyaXR5IQ==\r\nContent-Location: https://example.com/a%20b?q=1\r\n", `"Q2hlY2sgSW50ZWdyaXR5IQ=="`, `"https://example.com/a%20b?q=1"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			structureResponse(t, tc.headers+"Content-Disposition: inline\r\n\r\nx", base+")",
				base+" "+tc.md5+` ("INLINE" NIL) NIL `+tc.location+")")
		})
	}
}

// Multipart location belongs to that entity, with no multipart MD5 slot.
// Children have independent extension values; absent fields do not inherit.
//
// checklist: IMAP-BS-17 IMAP-BS-18
// checklist: IMAP2-RESP-209
// checklist: IMAP2-SEC-222
func TestBodyStructureMultipartLocation(t *testing.T) {
	const base = `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1`
	raw := "Content-Type: multipart/mixed; boundary=B\r\nContent-Location: outer/\r\nContent-MD5: ignored-multipart\r\nContent-Disposition: inline\r\n\r\n" +
		"--B\r\nContent-MD5: Q2hlY2sgSW50ZWdyaXR5IQ==\r\nContent-Location: child.txt\r\n\r\nx\r\n" +
		"--B\r\n\r\ny\r\n--B--\r\n"
	body := "(" + base + ")" + base + `) "MIXED")`
	extended := "(" + base + ` "Q2hlY2sgSW50ZWdyaXR5IQ==" NIL NIL "child.txt")` +
		base + ` NIL NIL NIL NIL) "MIXED" ("BOUNDARY" "B") ("INLINE" NIL) NIL "outer/")`
	structureResponse(t, raw, body, extended)
}

// MESSAGE/RFC822's location follows its encapsulated envelope/body/line count;
// the encapsulated body independently obtains MD5/location from its own headers.
//
// checklist: IMAP-BS-17 IMAP-BS-18
func TestBodyStructureMessageLocation(t *testing.T) {
	const base = `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1`
	inner := "Content-MD5: Q2hlY2sgSW50ZWdyaXR5IQ==\r\nContent-Location: inner.txt\r\n\r\nx"
	message := fmt.Sprintf(`("MESSAGE" "RFC822" NIL NIL NIL "7BIT" %d (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL) `, len(inner))
	structureResponse(t, "Content-Type: message/rfc822\r\nContent-Location: attached.eml\r\n\r\n"+inner,
		message+base+") 4)", message+base+` "Q2hlY2sgSW50ZWdyaXR5IQ==" NIL NIL "inner.txt") 4 NIL NIL NIL "attached.eml")`)
}

// Malformed MIME header values are opaque metadata, but must still use valid
// IMAP string framing. These are robustness fixtures, not valid URI/MD5 vectors.
//
// checklist: IMAP-BS-19
func TestBodyStructureExtensionStrings(t *testing.T) {
	const base = `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1`
	for _, tc := range []struct {
		name, value, wire string
		literals          []string
	}{
		{"quoted", "a\"b\\c", `"a\"b\\c"`, nil},
		{"literal", "café", `{5}`, []string{"café", "café"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			structureResponse(t, "Content-MD5: "+tc.value+"\r\nContent-Location: "+tc.value+"\r\n\r\nx",
				base+")", base+" "+tc.wire+" NIL NIL "+tc.wire+")", tc.literals...)
			structureResponse(t, "Content-Type: multipart/mixed; boundary=B\r\nContent-Location: "+tc.value+"\r\n\r\n--B\r\n\r\nx\r\n--B--\r\n",
				"("+base+`) "MIXED")`, "("+base+` NIL NIL NIL NIL) "MIXED" ("BOUNDARY" "B") NIL NIL `+tc.wire+")", tc.literals[:len(tc.literals)/2]...)
		})
	}
}

// Content-Language is an RFC 3282 §2 Language-List: comma-separated tags with
// optional CFWS. "da" and "mi, en" are RFC 3282's examples. One tag is a
// string, several a parenthesized list, none NIL; tags keep their case.
//
// checklist: IMAP-BS-08
func TestBodyStructureLanguage(t *testing.T) {
	const base = `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1`
	for _, tc := range []struct{ name, headers, lang string }{
		{"absent", "", "NIL"},
		{"empty", "Content-Language:\r\n", "NIL"},
		{"only-cfws", "Content-Language: (none) , ,\r\n", "NIL"},
		{"single", "content-language: da\r\n", `"da"`},
		{"list", "Content-Language: mi, en\r\n", `("mi" "en")`},
		{"comments", "Content-Language: en (English),fr-CA (Canadian (Qu\\)bec) French)\r\n", `("en" "fr-CA")`},
		{"folded", "Content-Language: de-CH,\r\n\tEN-gb\r\n", `("de-CH" "EN-gb")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			structureResponse(t, tc.headers+"Content-Location: a.txt\r\n\r\nx", base+")",
				base+" NIL NIL "+tc.lang+` "a.txt")`)
		})
	}
}

// Multipart and MESSAGE/RFC822 entities carry their own language; children and
// encapsulated bodies do not inherit it.
//
// checklist: IMAP-BS-08
func TestBodyStructureNestedLanguage(t *testing.T) {
	const base = `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1`
	raw := "Content-Type: multipart/alternative; boundary=B\r\nContent-Language: en, fr\r\n\r\n" +
		"--B\r\nContent-Language: fr\r\n\r\nx\r\n--B\r\n\r\ny\r\n--B--\r\n"
	structureResponse(t, raw, "("+base+")"+base+`) "ALTERNATIVE")`,
		"("+base+` NIL NIL "fr" NIL)`+base+` NIL NIL NIL NIL) "ALTERNATIVE" ("BOUNDARY" "B") NIL ("en" "fr") NIL)`)

	inner := "Content-Language: de\r\n\r\nx"
	message := fmt.Sprintf(`("MESSAGE" "RFC822" NIL NIL NIL "7BIT" %d (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL) `, len(inner))
	structureResponse(t, "Content-Type: message/rfc822\r\nContent-Language: en\r\n\r\n"+inner,
		message+base+") 3)", message+base+` NIL NIL "de" NIL) 3 NIL NIL "en" NIL)`)
}

// Malformed language values are opaque but still use valid IMAP string framing.
//
// checklist: IMAP-BS-08
func TestBodyStructureLanguageStrings(t *testing.T) {
	const base = `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1`
	structureResponse(t, "Content-Language: a\"b\\c, x\r\n\r\nx", base+")", base+` NIL NIL ("a\"b\\c" "x") NIL)`)
	structureResponse(t, "Content-Language: café\r\n\r\nx", base+")", base+" NIL NIL {5} NIL)", "café")
}
