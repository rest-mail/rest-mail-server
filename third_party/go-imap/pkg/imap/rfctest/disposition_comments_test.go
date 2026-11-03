package rfctest

import (
	"fmt"
	"testing"
)

// RFC 2183 §2 states Content-Disposition with RFC 822 lexical rules: comments
// and whitespace may separate the disposition type, parameter names and values.
// These synthetic fixtures cover nested and escaped comments, folding, and
// parentheses inside quoted parameter data, which are not comments.
//
// checklist: IMAP-CDP-01
func TestBodyStructureDispositionComments(t *testing.T) {
	plain := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1`
	for _, tc := range []struct{ value, wire string }{
		{`attachment; filename=doc.pdf (comment)`, `("ATTACHMENT" ("FILENAME" "doc.pdf"))`},
		{`(before) attachment (type); (attribute) filename (equals) = (value) doc.pdf (end)`, `("ATTACHMENT" ("FILENAME" "doc.pdf"))`},
		{"attachment;\r\n\tfilename=doc.pdf (folded\r\n\tcomment)", `("ATTACHMENT" ("FILENAME" "doc.pdf"))`},
		{`attachment; filename=doc.pdf (nested (inner) escaped \) \( \\ " ; / =)`, `("ATTACHMENT" ("FILENAME" "doc.pdf"))`},
		{`InLiNe (no parameters)`, `("INLINE" NIL)`},
		{`inline; filename="a (literal); = b" (ignored)`, `("INLINE" ("FILENAME" "a (literal); = b"))`},
		{"attachment; filename=\"a\\\"(b)\\\\c\" (ignored)", `("ATTACHMENT" ("FILENAME" "a\"(b)\\c"))`},
	} {
		t.Run(tc.value, func(t *testing.T) {
			structureResponse(t, "Content-Disposition: "+tc.value+"\r\n\r\nx", plain+")", plain+" NIL "+tc.wire+" NIL NIL)")
		})
	}
}

// Each entity reads its own Content-Disposition: a comment-bearing multipart
// disposition fills the multipart extension slot without leaking into children,
// and an encapsulated message's disposition belongs to the MESSAGE/RFC822 part.
//
// checklist: IMAP-CDP-02
func TestBodyStructureDispositionCommentScope(t *testing.T) {
	plain := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 1 1`
	child := "Content-Disposition: attachment (child); filename=a.txt (name)\r\n\r\nx"
	raw := "Content-Type: multipart/mixed; boundary=B\r\nContent-Disposition: (outer) inline\r\n\r\n--B\r\n" + child + "\r\n--B\r\n\r\nx\r\n--B--\r\n"
	structureResponse(t, raw, "("+plain+")"+plain+`) "MIXED")`,
		"("+plain+` NIL ("ATTACHMENT" ("FILENAME" "a.txt")) NIL NIL)`+plain+` NIL NIL NIL NIL) "MIXED" ("BOUNDARY" "B") ("INLINE" NIL) NIL NIL)`)

	message := fmt.Sprintf(`("MESSAGE" "RFC822" NIL NIL NIL "7BIT" %d (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL) `, len(child))
	structureResponse(t, "Content-Type: message/rfc822\r\nContent-Disposition: attachment (outer); filename=m.eml\r\n\r\n"+child,
		message+plain+") 3)", message+plain+` NIL ("ATTACHMENT" ("FILENAME" "a.txt")) NIL NIL) 3 NIL ("ATTACHMENT" ("FILENAME" "m.eml")) NIL NIL)`)
}

// Removing a comment must not join split tokens, and unbalanced comments or
// quotes, a type/subtype pair (the disposition type is one token) or an empty
// type leave the slot NIL. Independent fields such as Content-ID are retained.
// These are recovery tests, not valid MIME grammar examples.
//
// checklist: IMAP-CDP-03
func TestBodyStructureInvalidDispositionComments(t *testing.T) {
	for _, value := range []string{
		`attach(comment)ment; filename=a`, `attachment; file(comment)name=a`,
		`attachment; filename=a(comment)b`, `attachment (unclosed`,
		`attachment (trailing\`, `attachment )`, `attachment; filename="unclosed (value)`,
		`attachment/pdf; filename=a`, `(only a comment)`,
	} {
		t.Run(value, func(t *testing.T) {
			base := `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") "<part@x>" NIL "7BIT" 1 1`
			structureResponse(t, "Content-Disposition: "+value+"\r\nContent-ID: <part@x>\r\n\r\nx", base+")", base+" NIL NIL NIL NIL)")
		})
	}
}
