package rfctest

import (
	"fmt"
	"testing"
)

// digestInner is an encapsulated message carried by a digest part that has no
// Content-Type. Fixtures are synthetic; RFC 2046 §5.1.5 states the default but
// gives no raw digest with BODYSTRUCTURE output.
const digestInner = "Subject: one\r\n\r\nx\r\n"

// digestMessage returns the BODY and BODYSTRUCTURE forms of digestInner
// described as an implicit MESSAGE/RFC822 part.
func digestMessage() (body, extended string) {
	base := fmt.Sprintf(`("MESSAGE" "RFC822" NIL NIL NIL "7BIT" %d (NIL "one" NIL NIL NIL NIL NIL NIL NIL NIL) `, len(digestInner))
	body = base + `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 3 1) 3)`
	extended = base + `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 3 1 NIL NIL NIL NIL) 3 NIL NIL NIL NIL)`
	return body, extended
}

// TestBodyStructureDigestDefault checks that a multipart/digest part without
// Content-Type is MESSAGE/RFC822, while an explicit type still wins.
//
// checklist: IMAP-BS-12
func TestBodyStructureDigestDefault(t *testing.T) {
	msgBody, msgExtended := digestMessage()
	note := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 6 1)`
	noteExtended := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 6 1 NIL NIL NIL NIL)`
	for _, contentType := range []string{"multipart/digest", "Multipart/DIGEST"} {
		t.Run(contentType, func(t *testing.T) {
			raw := "Content-Type: " + contentType + "; boundary=D\r\n\r\n" +
				"--D\r\n\r\n" + digestInner + "\r\n" +
				"--D\r\nContent-Type: text/plain\r\n\r\nnote\r\n\r\n--D--\r\n"
			structureResponse(t, raw,
				"("+msgBody+note+` "DIGEST")`,
				"("+msgExtended+noteExtended+` "DIGEST" ("BOUNDARY" "D") NIL NIL NIL)`)
		})
	}
}

// TestBodyStructureDigestScope checks that the digest default applies only to
// the digest's direct children: the same untyped part is TEXT/PLAIN inside
// multipart/mixed, and a mixed part nested in a digest resets the default.
//
// checklist: IMAP-BS-12
func TestBodyStructureDigestScope(t *testing.T) {
	plain := fmt.Sprintf(`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" %d 3)`, len(digestInner))
	plainExtended := fmt.Sprintf(`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" %d 3 NIL NIL NIL NIL)`, len(digestInner))
	t.Run("mixed", func(t *testing.T) {
		raw := "Content-Type: multipart/mixed; boundary=M\r\n\r\n--M\r\n\r\n" + digestInner + "\r\n--M--\r\n"
		structureResponse(t, raw,
			"("+plain+` "MIXED")`,
			"("+plainExtended+` "MIXED" ("BOUNDARY" "M") NIL NIL NIL)`)
	})
	t.Run("nested", func(t *testing.T) {
		msgBody, msgExtended := digestMessage()
		raw := "Content-Type: multipart/mixed; boundary=O\r\n\r\n" +
			"--O\r\nContent-Type: multipart/digest; boundary=D\r\n\r\n" +
			"--D\r\n\r\n" + digestInner + "\r\n" +
			"--D\r\nContent-Type: multipart/mixed; boundary=M\r\n\r\n" +
			"--M\r\n\r\n" + digestInner + "\r\n--M--\r\n" +
			"\r\n--D--\r\n" +
			"\r\n--O--\r\n"
		inner := "(" + plain + ` "MIXED")`
		innerExtended := "(" + plainExtended + ` "MIXED" ("BOUNDARY" "M") NIL NIL NIL)`
		digest := "(" + msgBody + inner + ` "DIGEST")`
		digestExtended := "(" + msgExtended + innerExtended + ` "DIGEST" ("BOUNDARY" "D") NIL NIL NIL)`
		structureResponse(t, raw,
			"("+digest+` "MIXED")`,
			"("+digestExtended+` "MIXED" ("BOUNDARY" "O") NIL NIL NIL)`)
	})
}
