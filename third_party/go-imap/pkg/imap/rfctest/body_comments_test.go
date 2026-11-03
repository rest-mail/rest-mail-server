package rfctest

import (
	"fmt"
	"testing"
)

// The first case is RFC 2045 §5.1's comment-bearing charset example.
// Further synthetic cases exercise RFC 822 §§3.4.2–3.4.3 lexical boundaries:
// comments nest and allow quoted-pairs, but never consume quoted parameter data.
//
// checklist: IMAP-BS-22
func TestBodyStructureContentTypeComments(t *testing.T) {
	for _, value := range []string{
		`text/plain; charset=us-ascii (Plain text)`,
		`(before) text (type) / (subtype) plain (after); (attribute) charset (equals) = (value) us-ascii (end)`,
		"text\t/\tplain;\r\n\tcharset=us-ascii (folded\r\n\tcomment)",
		`text/plain; charset=us-ascii (nested (inner) escaped \) \( \\ " ; / = text/html)`,
	} {
		t.Run(value, func(t *testing.T) {
			base := `("TEXT" "PLAIN" ("CHARSET" "us-ascii") NIL NIL "7BIT" 9 2`
			structureResponse(t, "Content-Type: "+value+"\r\n\r\na=20b\r\ncd", base+")", base+" NIL NIL NIL NIL)")
		})
	}
	for _, value := range []string{
		`text/html; name="a (literal); / = b" (ignored)`,
		`text (comment) / html; name="a (literal); / = b"`,
	} {
		t.Run(value, func(t *testing.T) {
			base := `("TEXT" "HTML" ("CHARSET" "US-ASCII" "NAME" "a (literal); / = b") NIL NIL "7BIT" 1 1`
			structureResponse(t, "Content-Type: "+value+"\r\n\r\nx", base+")", base+" NIL NIL NIL NIL)")
		})
	}
	t.Run("quoted-pairs", func(t *testing.T) {
		base := `("APPLICATION" "OCTET-STREAM" ("NAME" "a\"(b)\\c") NIL NIL "7BIT" 1`
		structureResponse(t, "Content-Type: application/octet-stream; name=\"a\\\"(b)\\\\c\" (ignored)\r\n\r\nx", base+")", base+" NIL NIL NIL NIL)")
	})
}

// Comments on a multipart boundary must not hide children. Each nested entity
// parses its own type; encoded octets, independent metadata and BODY extensions
// still have their normal meaning. The fixtures are synthetic.
//
// checklist: IMAP-BS-22
func TestBodyStructureNestedContentTypeComments(t *testing.T) {
	const leaf = `("TEXT" "HTML" ("CHARSET" "utf-8") "<child@x>" NIL "QUOTED-PRINTABLE" 9 2`
	entity := "Content-Type: text (type) / html; charset=utf-8 (child)\r\nContent-ID: <child@x>\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\na=20b\r\ncd"
	for _, boundary := range []string{`B (boundary)`, `"B" (boundary)`, `"B(c)" (boundary)`} {
		delimiter := "B"
		if boundary == `"B(c)" (boundary)` {
			delimiter = "B(c)"
		}
		raw := "Content-Type: multipart (outer) / mixed; boundary=" + boundary + "\r\n\r\n--" + delimiter + "\r\n" + entity + "\r\n--" + delimiter + "--\r\n"
		t.Run(boundary, func(t *testing.T) {
			structureResponse(t, raw, "("+leaf+`) "MIXED")`, "("+leaf+` NIL NIL NIL NIL) "MIXED" ("BOUNDARY" "`+delimiter+`") NIL NIL NIL)`)
		})
	}
	t.Run("encapsulated", func(t *testing.T) {
		message := fmt.Sprintf(`("MESSAGE" "RFC822" NIL NIL NIL "7BIT" %d (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL) `, len(entity))
		structureResponse(t, "Content-Type: message (outer) / rfc822\r\n\r\n"+entity,
			message+leaf+") 6)", message+leaf+" NIL NIL NIL NIL) 6 NIL NIL NIL NIL)")
	})
}

// Malformed comments and split tokens use the existing invalid-type fallback;
// removing a comment must not silently turn "ht(comment)ml" into "html".
// These are recovery tests, not valid MIME grammar examples.
//
// checklist: IMAP-BS-23
func TestBodyStructureInvalidContentTypeComments(t *testing.T) {
	for _, value := range []string{
		`text/ht(comment)ml; charset=utf-8`, `te(comment)xt/html; charset=utf-8`,
		`text/html; char(comment)set=utf-8`, `text/html; charset=utf(comment)-8`,
		`text/html (unclosed`, `text/html (trailing\`, `text/html )`,
		`text/html; name="unclosed (value)`,
	} {
		t.Run(value, func(t *testing.T) {
			base := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") "<part@x>" NIL "7BIT" 1 1`
			structureResponse(t, "Content-Type: "+value+"\r\nContent-ID: <part@x>\r\n\r\nx", base+")", base+" NIL NIL NIL NIL)")
		})
	}
}
