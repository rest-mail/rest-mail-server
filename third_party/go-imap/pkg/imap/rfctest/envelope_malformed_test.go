package rfctest

import (
	"fmt"
	"strings"
	"testing"
)

// TestEnvelopeMalformedHeaders never substitutes cached summary values for a
// header block that cannot be parsed, nor presents its partially parsed fields
// as a valid message envelope.
//
// checklist: IMAP-ENV-12
func TestEnvelopeMalformedHeaders(t *testing.T) {
	for _, raw := range []string{
		"not a header\r\n\r\nbody",
		"Subject: must not survive\r\ninvalid line\r\n\r\nbody",
		" orphan continuation\r\n\r\nbody",
	} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			requireEnvelope(t, raw, `(NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)`)
		})
	}
}

// TestFetchRejectsNUL refuses prohibited CHAR8 NUL in raw messages and in
// decoded envelope/MIME strings before any response fragments or Seen writes.
// Metadata-only FLAGS remains usable and a later valid raw-message fetch works.
//
// checklist: IMAP-ENV-12
func TestFetchRejectsNUL(t *testing.T) {
	for _, raw := range []string{
		"Subject: a\x00b\r\n\r\nbody",
		"To: =?utf-8?Q?a=00b?= <a@example.com>\r\n\r\nbody",
		"Content-Type: text/plain; name*=utf-8''a%00b\r\n\r\nbody",
		"Subject: header\r\n\r\na\x00b",
		"To: =?utf-8?Q?a=00b?=:;\r\n\r\nbody",
		"Content-Type: message/rfc822\r\n\r\nTo: =?utf-8?Q?a=00b?= <a@example.com>\r\n\r\nbody",
	} {
		for _, selection := range []string{"SELECT", "EXAMINE"} {
			for _, prefix := range []string{"FETCH 1", "UID FETCH 10"} {
				t.Run(fmt.Sprintf("%q/%s/%s", raw, selection, prefix), func(t *testing.T) {
					box := envelopeFixture(raw)
					c := fetchClient(t, box, selection)
					replies, tagged := fetchCommand(t, c, prefix+" (BODY[] ENVELOPE BODYSTRUCTURE)")
					if len(replies) != 0 || !strings.HasPrefix(tagged, "F1 NO ") {
						t.Fatalf("NUL response: %#v %q", replies, tagged)
					}
					if len(box.updates) != 0 {
						t.Fatalf("NUL set Seen: %v", box.updates)
					}
					lines, tagged := c.command("N1", "FETCH 1 FLAGS")
					requireOK(t, tagged, "N1")
					requireExactly(t, lines, "* 1 FETCH (UID 10 FLAGS ())")
					box.raw[10] = "Subject: recovered\r\n\r\nbody"
					replies, tagged = fetchCommand(t, c, prefix+" ENVELOPE")
					requireOK(t, tagged, "F1")
					if len(replies) != 1 || !strings.Contains(replies[0].framing, `"recovered"`) {
						t.Fatalf("recovery: %#v", replies)
					}
				})
			}
		}
	}
}

// TestMessageStructureMalformedEnvelope preserves MESSAGE/RFC822's required
// ten-field envelope shape on an unparseable inner header block, in both basic
// and extended bodies, at the top level and in a multipart attachment.
//
// checklist: IMAP-MSG-08
func TestMessageStructureMalformedEnvelope(t *testing.T) {
	for _, inner := range []string{
		"not a header\r\n\r\nbody",
		"Subject: partial\r\ninvalid line\r\n\r\nbody",
		" orphan continuation\r\n\r\nbody",
	} {
		t.Run(fmt.Sprintf("%q", inner), func(t *testing.T) {
			// Malformed headers are retained as opaque TEXT/PLAIN source octets.
			// The fixtures specify the physical line count independently of MIME parsing.
			lines := 3
			if strings.HasPrefix(inner, "Subject:") {
				lines = 4
			}
			body := fmt.Sprintf(`("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" %d %d)`, len(inner), lines)
			extended := strings.TrimSuffix(body, ")") + " NIL NIL NIL NIL)"
			messageStructureResponse(t, inner, `(NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)`, body, extended, lines)
		})
	}
}
