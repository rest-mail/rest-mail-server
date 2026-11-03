package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestUnknownTransferEncoding checks RFC 2045 §6.4's mandatory opaque type
// override, even when a declared composite body looks parseable. Synthetic
// fixtures deliberately use readable octets under an unknown encoding: a
// receiver cannot assume their content has already been decoded.
//
// checklist: IMAP-CTE-04
func TestUnknownTransferEncoding(t *testing.T) {
	inner := "Subject: =?utf-8?b?bmVlZGxl?=\r\n" + depthLeafHeader + "\r\n" + depthLeafBody
	for _, tc := range []struct{ name, contentType, body string }{
		{"absent-type", "", "caf\xe9"},
		{"text-charset", "text/plain; charset=iso-8859-1", "caf\xe9"},
		{"application", "application/pdf; name=report.pdf", "caf\xe9"},
		{"invalid-type", "not-a-media-type", "caf\xe9"},
		{"message", "message/rfc822; name=forward.eml", inner},
		{"multipart", "multipart/mixed; boundary=B", "--B\r\n" + inner + "\r\n--B--"},
		{"digest", "multipart/digest; boundary=B", "--B\r\n\r\n" + inner + "\r\n--B--"},
	} {
		for _, encoding := range []string{"x-opaque", "(before) X-Opaque (after)", "future-encoding"} {
			t.Run(tc.name+"/"+encoding, func(t *testing.T) {
				header := "Content-Transfer-Encoding: " + encoding + "\r\nContent-ID: <id@x>\r\nContent-Description: opaque\r\n"
				if tc.contentType != "" {
					header += "Content-Type: " + tc.contentType + "\r\n"
				}
				raw := header + "\r\n" + tc.body
				token := "X-OPAQUE"
				if encoding == "future-encoding" {
					token = "FUTURE-ENCODING"
				}
				base := fmt.Sprintf(`("APPLICATION" "OCTET-STREAM" NIL "<id@x>" "opaque" "%s" %d`, token, len(tc.body))
				structureResponse(t, raw, base+")", base+" NIL NIL NIL NIL)")

				c := fetchClient(t, depthMailbox(raw), "EXAMINE")
				for _, sc := range []struct{ section, payload string }{
					{"", raw}, {"TEXT", tc.body}, {"1", tc.body}, {"1.MIME", header + "\r\n"},
					{"1.1", ""}, {"1.HEADER", ""}, {"1.TEXT", ""},
					{"1.HEADER.FIELDS (SUBJECT)", ""},
				} {
					replies, tagged := fetchCommand(t, c, "FETCH 1 BODY.PEEK["+sc.section+"]")
					requireOK(t, tagged, "F1")
					label := fmt.Sprintf("BODY[%s] {%d}", sc.section, len(sc.payload))
					if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{sc.payload}) || !strings.Contains(replies[0].framing, label) {
						t.Fatalf("BODY[%s] = %#v, want %q with payload %q", sc.section, replies, label, sc.payload)
					}
				}
				// A declared charset and encoded inner headers/bodies cannot
				// transform an unknown encoding. Stored octets remain searchable.
				requireSearch(t, c, `SEARCH BODY "needle"`, "* SEARCH")
				requireSearch(t, c, `SEARCH TEXT "needle"`, "* SEARCH")
				requireLiteralSearch(t, c, `SEARCH CHARSET UTF-8 BODY "café"`, "* SEARCH")
				stored := "caf"
				if strings.Contains(tc.body, depthLeafBody) {
					stored = depthLeafBody
				}
				requireSearch(t, c, `SEARCH BODY "`+stored+`"`, "* SEARCH 1")
			})
		}
	}
}

// The override belongs to the entity carrying the unknown encoding, not its
// parent or siblings. multipart/digest's implicit message/rfc822 default is
// overridden too. A following valid encoded sibling must still be decoded.
//
// checklist: IMAP-CTE-04
func TestUnknownTransferEncodingScope(t *testing.T) {
	for _, subtype := range []string{"mixed", "digest"} {
		t.Run(subtype, func(t *testing.T) {
			unknown := "Content-Transfer-Encoding: x-opaque\r\n\r\n" + depthLeafBody
			valid := depthLeafHeader + "\r\n" + "c2libGluZw==" // sibling
			raw := "Content-Type: multipart/" + subtype + "; boundary=B\r\nContent-Transfer-Encoding: (parent) 8bit\r\n\r\n--B\r\n" + unknown + "\r\n--B\r\n" + valid + "\r\n--B--\r\n"
			part := `("APPLICATION" "OCTET-STREAM" NIL NIL NIL "X-OPAQUE" 8`
			sibling := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "BASE64" 12 1`
			structureResponse(t, raw, "("+part+")"+sibling+`) "`+strings.ToUpper(subtype)+`")`,
				"("+part+" NIL NIL NIL NIL)"+sibling+` NIL NIL NIL NIL) "`+strings.ToUpper(subtype)+`" ("BOUNDARY" "B") NIL NIL NIL)`)
			c := fetchClient(t, depthMailbox(raw), "EXAMINE")
			requireSearch(t, c, `SEARCH BODY "needle"`, "* SEARCH")
			requireSearch(t, c, `SEARCH BODY "`+depthLeafBody+`"`, "* SEARCH 1")
			requireSearch(t, c, `SEARCH BODY "sibling"`, "* SEARCH 1")
		})
	}
}
