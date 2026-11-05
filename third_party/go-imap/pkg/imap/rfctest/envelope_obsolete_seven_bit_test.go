package rfctest

import (
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap/wireform"
)

// requireToList proves the To field of a top-level and an encapsulated envelope, in
// a session that has not enabled UTF-8.
func requireToList(t *testing.T, to, want string) {
	t.Helper()
	raw := "To: " + to + "\r\nSubject: s\r\n\r\nbody\r\n"
	fields := `(NIL "s" NIL NIL NIL ` + want + ` NIL NIL NIL NIL)`
	c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
	requireHolds(t, fetchMetadata(t, c, "ENVELOPE"), fields)
	c = fetchClient(t, sevenBitBox("Content-Type: message/rfc822\r\n\r\n"+raw), "EXAMINE")
	requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), fields)
}

// TestObsoleteAddressNamesFollowTheSession: a mailbox only the obsolete-syntax reader
// can read has its name decoded and sent by the same rules as any other. Decoded
// text is an encoded word without UTF-8 and a literal with it, and a word in a
// charset go-imap has no reader for stays as it was sent.
//
// checklist: IMAP-ENV-14 IMAP-ENV-13
func TestObsoleteAddressNamesFollowTheSession(t *testing.T) {
	for _, tc := range []struct{ name, header, sevenBit, utf8 string }{
		{name: "8-bit name", header: "J. M\xc3\xbcller <\"a\" . b @ example . com>", sevenBit: `=?UTF-8?Q?J=2E_M=C3=BCller?=`, utf8: "J. Müller"},
		{name: "encoded word", header: `=?UTF-8?Q?J=C3=B6rg?= <"a".b@example.com>`, sevenBit: `=?UTF-8?Q?J=C3=B6rg?=`, utf8: "Jörg"},
		{name: "charset alias go-imap decodes", header: `=?latin1?Q?caf=E9?= <"a".b@example.com>`, sevenBit: `=?UTF-8?Q?caf=C3=A9?=`, utf8: "café"},
		{name: "charset kept", header: `=?windows-1252?Q?J=F6rg?= <"a".b@example.com>`, sevenBit: `=?windows-1252?Q?J=F6rg?=`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireToList(t, tc.header, `(("`+tc.sevenBit+`" NIL "a.b" "example.com"))`)

			framing, literals := fetchUTF8Metadata(t, "To: "+tc.header+"\r\n\r\nbody\r\n", "ENVELOPE")
			requireHolds(t, framing, `NIL "a.b" "example.com"`)
			if tc.utf8 == "" {
				requireHolds(t, framing, tc.sevenBit)
			} else if literals != tc.utf8 {
				t.Fatalf("literals = %q, want %q", literals, tc.utf8)
			}
		})
	}

	t.Run("group name", func(t *testing.T) {
		requireToList(t, "Gr\xc3\xbcppe: \"a\".b@example.com;",
			`((NIL NIL "=?UTF-8?Q?Gr=C3=BCppe?=" NIL)(NIL NIL "a.b" "example.com")(NIL NIL NIL NIL))`)
	})
}

// TestMailboxWithNoSevenBitFormIsLeftOut: WireForm makes an 8-bit word an encoded
// word, and white space or a period can separate that word from the rest of an
// addr-spec. An encoded word may not stand there (RFC 2047 §5), so neither reader
// takes it for a mailbox: it is left out, the others in the field stay, and a field
// with none left is NIL. With UTF-8 the same mailbox is read as sent.
//
// checklist: IMAP-ENV-14 IMAP-ENV-13
func TestMailboxWithNoSevenBitFormIsLeftOut(t *testing.T) {
	for _, tc := range []struct{ name, mailbox, literal string }{
		{name: "whole addr-spec", mailbox: "j\xc3\xb6rg@example.com", literal: "jörg"},
		{name: "local part alone", mailbox: "\xc3\xb6 @ example.com", literal: "ö"},
		{name: "word of a local part", mailbox: "j . \xc3\xb6 @ example . com", literal: "j.ö"},
		{name: "quoted word", mailbox: "\"a\xc3\xb6\".b @ example.com", literal: "aö.b"},
		{name: "angle address", mailbox: "<j . \xc3\xb6 @ example . com>", literal: "j.ö"},
		{name: "domain", mailbox: "a@ ex\xc3\xa4mple.com", literal: "exämple.com"},
		{name: "word of a domain", mailbox: "a @ ex\xc3\xa4mple . com", literal: "exämple.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireToList(t, tc.mailbox, "NIL")
			requireToList(t, "x@example.com, "+tc.mailbox+", y@example.com",
				`((NIL NIL "x" "example.com")(NIL NIL "y" "example.com"))`)

			_, literals := fetchUTF8Metadata(t, "To: "+tc.mailbox+"\r\n\r\nbody\r\n", "ENVELOPE")
			if literals != tc.literal {
				t.Fatalf("literals = %q, want %q", literals, tc.literal)
			}
		})
	}

	t.Run("octets that are not UTF-8", func(t *testing.T) {
		const raw = "To: x@example.com, j . \xf6 @ example . com\r\n\r\nbody\r\n"
		framing, literals := fetchUTF8Metadata(t, raw, "ENVELOPE")
		requireHolds(t, framing, `((NIL NIL "x" "example.com"))`)
		if literals != "" {
			t.Fatalf("literals = %q", literals)
		}
	})

	t.Run("stored as an encoded word", func(t *testing.T) {
		requireToList(t, "x@example.com, =?UTF-8?Q?j=C3=B6?=@example.com", `((NIL NIL "x" "example.com"))`)
	})
}

// TestControlOctetPartConverted: a part whose header holds a control octet is read
// by the same header reader for FETCH conversion as for BODYSTRUCTURE, so it goes out
// as what its own fields say and the structure describes the octets sent.
//
// checklist: IMAP-BS-24 IMAP-RESP-16
func TestControlOctetPartConverted(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Description: a\x01b\r\nContent-Type: application/pdf\r\n\r\n\xf6\xf7\r\n" +
		"--b\r\nX-Note: \xc3\xa9\x01\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nh\xc3\xa9\r\n" +
		"--b--\r\n"
	c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
	requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"),
		"(\"APPLICATION\" \"PDF\" NIL NIL \"a\x01b\" \"BASE64\" 4 ",
		`("TEXT" "PLAIN" ("CHARSET" "utf-8") NIL NIL "7BIT" 3 1 `)

	for section, want := range map[string]string{"1": "9vc=", "2": "h\xc3\xa9"} {
		c = fetchClient(t, sevenBitBox(raw), "EXAMINE")
		_, payload := fetchLiteral(t, c, "BODY.PEEK["+section+"]")
		if payload != want {
			t.Fatalf("BODY[%s] = %q, want %q", section, payload, want)
		}
	}

	c = fetchClient(t, sevenBitBox(raw), "EXAMINE")
	_, header := fetchLiteral(t, c, "BODY.PEEK[2.MIME]")
	require7bit(t, "BODY[2.MIME]", header)
	requireHolds(t, header, "X-Note: =?UTF-8?Q?=C3=A9=01?=")

	c = fetchClient(t, sevenBitBox(raw), "EXAMINE")
	_, whole := fetchLiteral(t, c, "BODY.PEEK[]")
	if whole != wireform.Convert(raw, false) {
		t.Fatalf("BODY[] = %q is not the message sizes describe, %q", whole, wireform.Convert(raw, false))
	}
}
