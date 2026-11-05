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

// TestFetchReplacesNUL sends a space for a NUL in a raw message, and for one decoded
// into an ENVELOPE or BODYSTRUCTURE string, in place of the CHAR8 that RFC 3501 §9
// excludes. Nothing is refused, flags follow the usual rules, and the stored octets
// stay as they were.
//
// checklist: IMAP-ENV-12
func TestFetchReplacesNUL(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"Subject: a\x00b\r\n\r\nbody", `"a b"`},
		{"To: =?utf-8?Q?a=00b?= <a@example.com>\r\n\r\nbody", `(("a b" NIL "a" "example.com"))`},
		{"To: =?utf-8?Q?=C3=A9=00?= <a@example.com>\r\n\r\nbody", `(("=?UTF-8?Q?=C3=A9_?=" NIL "a" "example.com"))`},
		{"Content-Type: text/plain; name*=utf-8''%C3%A9%00\r\n\r\nbody", `"NAME" "=?UTF-8?Q?=C3=A9_?=")`},
		{"Content-Type: text/plain; name*=utf-8''a%00b\r\n\r\nbody", `"NAME" "a b")`},
		{"Subject: header\r\n\r\na\x00b", `"header"`},
		{"To: =?utf-8?Q?a=00b?=:;\r\n\r\nbody", `"a b"`},
		{"Content-Type: message/rfc822\r\n\r\nTo: =?utf-8?Q?a=00b?= <a@example.com>\r\n\r\nbody", `(("a b" NIL "a" "example.com"))`},
	} {
		for _, selection := range []string{"SELECT", "EXAMINE"} {
			for _, prefix := range []string{"FETCH 1", "UID FETCH 10"} {
				t.Run(fmt.Sprintf("%q/%s/%s", tc.raw, selection, prefix), func(t *testing.T) {
					box := envelopeFixture(tc.raw)
					c := fetchClient(t, box, selection)
					replies, tagged := fetchCommand(t, c, prefix+" (BODY[] ENVELOPE BODYSTRUCTURE)")
					requireOK(t, tagged, "F1")
					if len(replies) != 1 || len(replies[0].literals) != 1 {
						t.Fatalf("NUL response: %#v", replies)
					}
					if !strings.Contains(replies[0].framing, tc.want) {
						t.Fatalf("%q lacks %q", replies[0].framing, tc.want)
					}
					if strings.IndexByte(replies[0].framing, 0) >= 0 {
						t.Fatalf("a NUL reached the client: %q", replies[0].framing)
					}
					if got, want := replies[0].literals[0], strings.ReplaceAll(tc.raw, "\x00", " "); got != want {
						t.Fatalf("BODY[] = %q, want %q", got, want)
					}
					if box.raw[10] != tc.raw {
						t.Fatalf("stored octets changed: %q", box.raw[10])
					}
					if read := len(box.updates) != 0; read != (selection == "SELECT") {
						t.Fatalf("%s set Seen: %v", selection, box.updates)
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
