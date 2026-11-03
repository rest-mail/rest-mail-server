package rfctest

import (
	"fmt"
	"slices"
	"testing"
)

// RFC 3501 §9 requires body-type-mpart = 1*body SP media-subtype, so a
// multipart with no readable parts (no delimiter line, or only the close
// delimiter) cannot be described as an empty list. The server
// substitutes one empty part with RFC 2045 §5.2's default type,
// text/plain; charset=us-ascii. The substitute has no header, so it carries no
// content fields. A multipart/digest gets the same text/plain substitute: a
// zero-octet entity is not an RFC 5322 message, so the digest default of
// message/rfc822 (IMAP-BS-12) would describe something that is not there.
//
// checklist: IMAP-BS-11
func TestBodyStructureEmptyMultipartFallback(t *testing.T) {
	empty := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 0 0)`
	emptyExtended := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 0 0 NIL NIL NIL NIL)`
	for _, tc := range []struct {
		name, raw, subtype, params string
	}{
		{"close-delimiter-only", "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B--\r\n", "MIXED", `("BOUNDARY" "B")`},
		{"no-delimiter", "Content-Type: multipart/alternative; boundary=B\r\n\r\njust text\r\n", "ALTERNATIVE", `("BOUNDARY" "B")`},
		{"empty-body", "Content-Type: multipart/related; boundary=B\r\n\r\n", "RELATED", `("BOUNDARY" "B")`},
		{"digest", "Content-Type: multipart/digest; boundary=B\r\n\r\n--B--\r\n", "DIGEST", `("BOUNDARY" "B")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			structureResponse(t, tc.raw,
				"("+empty+` "`+tc.subtype+`")`,
				"("+emptyExtended+` "`+tc.subtype+`" `+tc.params+` NIL NIL NIL)`)
		})
	}
}

// When the header block cannot be read at all (here a header line with no
// colon, which RFC 5322 §2.2 does not allow), no Content-Type can be found. The
// RFC 2045 §5.2 default then describes the entire stored message as one
// text/plain; charset=us-ascii part. Its size and line count are those of the
// stored octets, so a BODY[TEXT] or RFC822.SIZE client still sees consistent
// numbers. A nested message/rfc822 part with an unreadable header gets the same
// treatment inside its encapsulating structure.
//
// checklist: IMAP-BS-11
func TestBodyStructureUnreadableHeaderFallback(t *testing.T) {
	raw := "not a header line\r\n\r\none\r\ntwo\r\n"
	base := fmt.Sprintf(`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" %d 4`, len(raw))
	t.Run("top-level", func(t *testing.T) {
		structureResponse(t, raw, base+")", base+" NIL NIL NIL NIL)")
	})
	t.Run("encapsulated", func(t *testing.T) {
		outer := "Content-Type: message/rfc822\r\n\r\n" + raw
		message := fmt.Sprintf(`("MESSAGE" "RFC822" NIL NIL NIL "7BIT" %d (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL) `, len(raw))
		structureResponse(t, outer, message+base+") 4)", message+base+" NIL NIL NIL NIL) 4 NIL NIL NIL NIL)")
	})
}

// RFC 2046 §5.1.1 makes the boundary parameter mandatory for every multipart
// subtype, so a multipart Content-Type without one is an invalid field. RFC 2045
// §5.2 then gives the entity the default type, text/plain; charset=us-ascii (or
// message/rfc822 inside a multipart/digest), rather than a leaf "MULTIPART"
// body that no client can open. FETCH sections agree: the entity has no parts,
// so part 1 of the message is its whole body.
//
// checklist: IMAP-BS-11
func TestBodyStructureMultipartWithoutBoundary(t *testing.T) {
	body := "--B\r\n\r\nx\r\n--B--\r\n"
	leaf := fmt.Sprintf(`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" %d 4`, len(body))
	for _, value := range []string{"multipart/mixed", "multipart/alternative; charset=utf-8", `multipart/related; boundary=""`} {
		t.Run(value, func(t *testing.T) {
			raw := "Content-Type: " + value + "\r\n\r\n" + body
			t.Run("top-level", func(t *testing.T) {
				structureResponse(t, raw, leaf+")", leaf+" NIL NIL NIL NIL)")
			})
			t.Run("mixed-child", func(t *testing.T) {
				outer := "Content-Type: multipart/mixed; boundary=O\r\n\r\n--O\r\n" + raw + "\r\n--O--\r\n"
				structureResponse(t, outer, "("+leaf+`) "MIXED")`,
					"("+leaf+` NIL NIL NIL NIL) "MIXED" ("BOUNDARY" "O") NIL NIL NIL)`)
			})
			t.Run("sections", func(t *testing.T) {
				for _, tc := range []struct{ item, payload string }{
					{"BODY.PEEK[1]", body},
					{"BODY.PEEK[2]", ""},
					{"BODY.PEEK[1.1]", ""},
				} {
					box := newFetchMailbox()
					box.raw[10] = raw
					c := fetchClient(t, box, "SELECT")
					replies, tagged := fetchCommand(t, c, "FETCH 1 "+tc.item)
					requireOK(t, tagged, "F1")
					if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{tc.payload}) {
						t.Fatalf("%s replies %#v, want payload %q", tc.item, replies, tc.payload)
					}
				}
			})
		})
	}
}
