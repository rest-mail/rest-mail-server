package rfctest

import (
	"fmt"
	"slices"
	"testing"
)

// TestBodyStructureControlOctetPart describes every multipart child that
// section FETCH and SEARCH see, including one whose header holds a control
// octet (RFC 5322 §4.2 obs-utext) and every part after it
// (rest-mail/rest-mail-server#1863). The parts are split by the same code for
// all three, so they agree on the message's structure.
//
// checklist: IMAP-BS-24
func TestBodyStructureControlOctetPart(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\n\r\nfirst\r\n" +
		"--b\r\nContent-Description: a\x01b\r\nContent-Type: application/pdf\r\n\r\nneedle\r\n" +
		"--b\r\nContent-Type: image/png\r\n\r\nthird\r\n" +
		"--b--\r\n"
	// RFC 3501 TEXT-CHAR excludes only CR and LF, so the description's
	// control octet is legal in a quoted string.
	structureResponse(t, raw,
		`(("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 5 1)`+
			"(\"APPLICATION\" \"PDF\" NIL NIL \"a\x01b\" \"7BIT\" 6)"+
			`("IMAGE" "PNG" NIL NIL NIL "7BIT" 5) "MIXED")`,
		`(("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 5 1 NIL NIL NIL NIL)`+
			"(\"APPLICATION\" \"PDF\" NIL NIL \"a\x01b\" \"7BIT\" 6 NIL NIL NIL NIL)"+
			`("IMAGE" "PNG" NIL NIL NIL "7BIT" 5 NIL NIL NIL NIL) "MIXED" ("BOUNDARY" "b") NIL NIL NIL)`)

	for i, payload := range []string{"first", "needle", "third"} {
		section := fmt.Sprint(i + 1)
		c := fetchClient(t, depthMailbox(raw), "EXAMINE")
		replies, tagged := fetchCommand(t, c, "FETCH 1 BODY.PEEK["+section+"]")
		requireOK(t, tagged, "F1")
		if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{payload}) {
			t.Fatalf("BODY[%s] = %#v, want %q", section, replies, payload)
		}
	}
	c := fetchClient(t, depthMailbox(raw), "EXAMINE")
	requireSearch(t, c, `SEARCH BODY "needle"`, "* SEARCH 1")
}
