package rfctest

import (
	"fmt"
	"strings"
	"testing"
)

// RFC 2045 §§1/6.1 apply RFC 822's lexical comments to the single,
// case-insensitive encoding token. These synthetic fixtures keep encoded octets
// opaque, including invalid payloads: metadata parsing is not body decoding.
//
// checklist: IMAP-CTE-01
func TestBodyStructureEncodingComments(t *testing.T) {
	for _, tc := range []struct{ value, token string }{
		{"7bit", "7BIT"}, {"8BiT", "8BIT"}, {"BiNaRy", "BINARY"},
		{"bAsE64 (transport)", "BASE64"},
		{"(leading)quoted-printable", "QUOTED-PRINTABLE"},
		{"(one)(two)\tbase64\t(three)", "BASE64"},
		{`(outer (inner) escaped \) and \()base64(end)`, "BASE64"},
		{"(folded\r\n\tcomment)\r\n\tQuOtEd-PrInTaBlE\r\n\t(tail)", "QUOTED-PRINTABLE"},
		{"(extension) x-example.v1+foo (opaque)", "X-EXAMPLE.V1+FOO"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			base := fmt.Sprintf(`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") "<id@x>" "note" "%s" 9 2`, tc.token)
			if tc.token == "X-EXAMPLE.V1+FOO" {
				base = fmt.Sprintf(`("APPLICATION" "OCTET-STREAM" NIL "<id@x>" "note" "%s" 9`, tc.token)
			}
			entity := "Content-Transfer-Encoding: " + tc.value + "\r\nContent-ID: <id@x>\r\nContent-Description: note\r\n\r\na=20b\r\ncd"
			structureResponse(t, entity, base+")", base+" NIL NIL NIL NIL)")
		})
	}
}

// Each entity owns its encoding; comments never propagate from a parent and
// raw multipart traversal must retain the original encoding and body dimensions.
//
// checklist: IMAP-CTE-01 IMAP-CTE-02
func TestBodyStructureEncodingCommentScope(t *testing.T) {
	entity := "Content-Transfer-Encoding: (child) quoted-printable (tail)\r\n\r\na=20b\r\ncd"
	part := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "QUOTED-PRINTABLE" 9 2`
	plain := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1`
	raw := "Content-Type: multipart/mixed; boundary=B\r\nContent-Transfer-Encoding: (parent) 7bit\r\n\r\n--B\r\n" + entity + "\r\n--B\r\n\r\nx\r\n--B--\r\n"
	structureResponse(t, raw, "("+part+")"+plain+`) "MIXED")`,
		"("+part+" NIL NIL NIL NIL)"+plain+` NIL NIL NIL NIL) "MIXED" ("BOUNDARY" "B") NIL NIL NIL)`)
	outer := fmt.Sprintf(`("MESSAGE" "RFC822" NIL NIL NIL "7BIT" %d (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL) `, len(entity))
	structureResponse(t, "Content-Type: message/rfc822\r\nContent-Transfer-Encoding: (outer) 7bit\r\n\r\n"+entity,
		outer+part+") 4)", outer+part+" NIL NIL NIL NIL) 4 NIL NIL NIL NIL)")
}

// Malformed fields keep the safely framed encoding value but use an opaque
// type. A comment splitting a token must not silently manufacture a recognised encoding. These robustness
// cases do not claim valid MIME or define a general malformed-header policy.
//
// checklist: IMAP-CTE-03
func TestBodyStructureMalformedEncodingComments(t *testing.T) {
	for _, value := range []string{"base(comment)64", "base 64", "base64 (unfinished", "base64 )", "(comment)", `"base64"`, "base64; x=y", "base64 (escaped\\)"} {
		t.Run(value, func(t *testing.T) {
			quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(strings.ToUpper(value))
			part := `("APPLICATION" "OCTET-STREAM" NIL NIL NIL "` + quoted + `" 1`
			structureResponse(t, "Content-Transfer-Encoding: "+value+"\r\n\r\nx", part+")", part+" NIL NIL NIL NIL)")
		})
	}
}
