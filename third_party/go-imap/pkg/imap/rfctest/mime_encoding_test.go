package rfctest

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// encapsulatedMessage is the message a MESSAGE/RFC822 part carries in
// TestMessageRFC822Encoding. Its body text "needle" is what SEARCH looks for.
const encapsulatedMessage = "Subject: inner\r\n\r\nneedle\r\n"

// TestMessageRFC822Encoding checks that a MESSAGE/RFC822 entity with a
// Content-Transfer-Encoding RFC 2046 §5.2.1 prohibits there (base64,
// quoted-printable or an unknown token) is handled consistently (RFC 3501
// §7.4.2). Its stored octets are not a message, so BODY and BODYSTRUCTURE
// describe one opaque APPLICATION/OCTET-STREAM leaf that keeps its encoding,
// numeric FETCH sections stop at its octets, and SEARCH BODY matches the
// decoded leaf. 7bit, 8bit and binary encapsulations are still parsed.
//
// checklist: IMAP-MSG-09
func TestMessageRFC822Encoding(t *testing.T) {
	for _, tc := range []struct {
		cte, body string
		opaque    bool
	}{
		{"base64", base64.StdEncoding.EncodeToString([]byte(encapsulatedMessage)), true},
		{"Quoted-Printable", "Subject: inner=0D=0A=0D=0Ane=65dle=0D=0A", true},
		{"x-unknown", encapsulatedMessage, true},
		{"7bit", encapsulatedMessage, false},
		{"8BIT", encapsulatedMessage, false},
		{"binary", encapsulatedMessage, false},
	} {
		partHeader := "Content-Type: message/rfc822\r\nContent-Transfer-Encoding: " + tc.cte + "\r\n"
		// The part body ends before the CRLF that precedes the delimiter.
		partBody := strings.TrimSuffix(tc.body, "\r\n")

		t.Run("part/"+tc.cte, func(t *testing.T) {
			raw := "Subject: outer\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\n" +
				partHeader + "\r\n" + partBody + "\r\n--b--\r\n"
			encoding := strings.ToUpper(tc.cte)
			if tc.opaque {
				structureResponse(t, raw,
					fmt.Sprintf(`(("APPLICATION" "OCTET-STREAM" NIL NIL NIL "%s" %d) "MIXED")`, encoding, len(partBody)),
					fmt.Sprintf(`(("APPLICATION" "OCTET-STREAM" NIL NIL NIL "%s" %d NIL NIL NIL NIL) "MIXED" ("BOUNDARY" "b") NIL NIL NIL)`, encoding, len(partBody)))
			} else {
				c := fetchClient(t, depthMailbox(raw), "EXAMINE")
				replies, tagged := fetchCommand(t, c, "FETCH 1 BODYSTRUCTURE")
				requireOK(t, tagged, "F1")
				want := fmt.Sprintf(`(("MESSAGE" "RFC822" NIL NIL NIL "%s" %d (NIL "inner"`, encoding, len(partBody))
				if len(replies) != 1 || !strings.Contains(replies[0].framing, want) {
					t.Fatalf("BODYSTRUCTURE = %#v, want it to contain %q", replies, want)
				}
			}

			// Only a parsed encapsulation has a header, text and parts below it.
			innerHeader, innerText, innerPart := "Subject: inner\r\n\r\n", "needle", "needle"
			if tc.opaque {
				innerHeader, innerText, innerPart = "", "", ""
			}
			for _, sc := range []struct{ section, payload string }{
				{"1", partBody},
				{"1.MIME", partHeader + "\r\n"},
				{"1.HEADER", innerHeader},
				{"1.TEXT", innerText},
				{"1.1", innerPart},
			} {
				c := fetchClient(t, depthMailbox(raw), "EXAMINE")
				replies, tagged := fetchCommand(t, c, "FETCH 1 BODY.PEEK["+sc.section+"]")
				requireOK(t, tagged, "F1")
				label := fmt.Sprintf("BODY[%s] {%d}", sc.section, len(sc.payload))
				if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{sc.payload}) || !strings.Contains(replies[0].framing, label) {
					t.Fatalf("BODY[%s] = %#v, want %q with payload %q", sc.section, replies, label, sc.payload)
				}
			}

			// The leaf is decoded like any other, so its text stays searchable.
			c := fetchClient(t, depthMailbox(raw), "EXAMINE")
			requireSearch(t, c, `SEARCH BODY "needle"`, "* SEARCH 1")
		})

		t.Run("message/"+tc.cte, func(t *testing.T) {
			// The top-level message itself declares message/rfc822.
			raw := "Subject: outer\r\n" + partHeader + "\r\n" + tc.body
			c := fetchClient(t, depthMailbox(raw), "EXAMINE")
			replies, tagged := fetchCommand(t, c, "FETCH 1 BODY")
			requireOK(t, tagged, "F1")
			want := fmt.Sprintf(`BODY ("APPLICATION" "OCTET-STREAM" NIL NIL NIL "%s" %d)`, strings.ToUpper(tc.cte), len(tc.body))
			if !tc.opaque {
				want = `BODY ("MESSAGE" "RFC822" NIL NIL NIL`
			}
			if len(replies) != 1 || !strings.Contains(replies[0].framing, want) {
				t.Fatalf("BODY = %#v, want it to contain %q", replies, want)
			}
			requireSearch(t, c, `SEARCH BODY "needle"`, "* SEARCH 1")
		})
	}
}
