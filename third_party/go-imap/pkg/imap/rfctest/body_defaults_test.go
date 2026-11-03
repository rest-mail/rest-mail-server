package rfctest

import (
	"fmt"
	"testing"
)

// RFC 2045 §5.2's explicit default is text/plain; charset=us-ascii.
// These synthetic messages exercise that default with and without MIME-Version;
// neither an omitted type nor an omitted charset changes the stored body bytes.
//
// checklist: IMAP-BS-20
func TestBodyStructureMIMEDefaults(t *testing.T) {
	for _, headers := range []string{
		"",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=us-ascii\r\n",
		"Content-Type: text/plain\r\n",
		"Content-Type: text/html\r\n",
	} {
		t.Run(headers, func(t *testing.T) {
			subtype, charset := "PLAIN", "US-ASCII"
			if headers == "Content-Type: text/html\r\n" {
				subtype = "HTML"
			}
			if headers == "Content-Type: text/plain; charset=us-ascii\r\n" {
				charset = "us-ascii" // Explicit parameter values retain their case.
			}
			base := fmt.Sprintf(`("TEXT" "%s" ("CHARSET" "%s") NIL NIL "7BIT" 8 2`, subtype, charset)
			structureResponse(t, headers+"\r\none\r\ntwo", base+")", base+" NIL NIL NIL NIL)")
		})
	}
}

// RFC 2045 §5.1 requires a subtype; unlike a disposition token, a bare
// "application" is not a MIME media type. §5.2 recommends the text/plain default
// for invalid Content-Type fields. Invalid parameters must not leak a charset
// into the fallback, while unrelated content headers still describe the entity.
//
// checklist: IMAP-BS-21
func TestBodyStructureInvalidContentType(t *testing.T) {
	for _, value := range []string{
		"", "application", "message", "multipart", "text; charset=utf-8",
		"application; name=file.bin", "application/", "/plain", "text/plain/extra",
		"text/plain; charset", `text/plain; charset="unfinished`,
	} {
		t.Run(value, func(t *testing.T) {
			headers := "Content-Type: " + value + "\r\nContent-ID: <part@x>\r\n" +
				"Content-Description: note\r\nContent-Transfer-Encoding: quoted-printable\r\n" +
				"Content-Disposition: inline\r\nContent-Language: en\r\nContent-Location: part.txt\r\n"
			base := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") "<part@x>" "note" "QUOTED-PRINTABLE" 9 2`
			body := base + ")"
			extended := base + ` NIL ("INLINE" NIL) "en" "part.txt")`
			entity := headers + "\r\na=20b\r\ncd"
			t.Run("single", func(t *testing.T) {
				structureResponse(t, entity, body, extended)
			})
			t.Run("mixed-child", func(t *testing.T) {
				raw := "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n" + entity + "\r\n--B--\r\n"
				structureResponse(t, raw, "("+body+` "MIXED")`,
					"("+extended+` "MIXED" ("BOUNDARY" "B") NIL NIL NIL)`)
			})
			t.Run("encapsulated", func(t *testing.T) {
				message := fmt.Sprintf(`("MESSAGE" "RFC822" NIL NIL NIL "7BIT" %d (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL) `, len(entity))
				structureResponse(t, "Content-Type: message/rfc822\r\n\r\n"+entity,
					message+body+" 10)", message+extended+" 10 NIL NIL NIL NIL)")
			})
		})
	}
}

// Valid explicit media types and charsets win over the default. Non-text media
// types have neither an implicit charset nor a text line-count field. The
// quoted parameter is RFC 2045 §5.1's charset example, without its comment.
//
// checklist: IMAP-BS-20 IMAP-BS-21
func TestBodyStructureExplicitContentType(t *testing.T) {
	for _, tc := range []struct{ value, fields string }{
		{`TEXT/PLAIN; CHARSET="us-ascii"`, `"TEXT" "PLAIN" ("CHARSET" "us-ascii") NIL NIL "7BIT" 1 1`},
		{`text/html; charset=utf-8`, `"TEXT" "HTML" ("CHARSET" "utf-8") NIL NIL "7BIT" 1 1`},
		{`application/octet-stream`, `"APPLICATION" "OCTET-STREAM" NIL NIL NIL "7BIT" 1`},
		{`image/png; name=picture.png`, `"IMAGE" "PNG" ("NAME" "picture.png") NIL NIL "7BIT" 1`},
		{`application/x-example; charset=utf-8`, `"APPLICATION" "X-EXAMPLE" ("CHARSET" "utf-8") NIL NIL "7BIT" 1`},
	} {
		t.Run(tc.value, func(t *testing.T) {
			base := "(" + tc.fields
			structureResponse(t, "Content-Type: "+tc.value+"\r\n\r\nx", base+")", base+" NIL NIL NIL NIL)")
		})
	}
}
