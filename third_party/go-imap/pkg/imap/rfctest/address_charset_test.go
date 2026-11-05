package rfctest

import (
	"strings"
	"testing"
)

// fetchUTF8Metadata answers FETCH item in a session that enabled UTF8=ACCEPT
// (RFC 6855), returning the framing and any literals, with the framing still 7-bit.
func fetchUTF8Metadata(t *testing.T, raw, item string) (framing, literals string) {
	t.Helper()
	c := selectionClient(t, utf8FetchMailbox{sevenBitBox(raw)})
	_, tagged := c.command("E1", "ENABLE UTF8=ACCEPT")
	requireOK(t, tagged, "E1")
	_, tagged = c.command("S1", "EXAMINE INBOX")
	requireOK(t, tagged, "S1")
	replies, tagged := fetchCommand(t, c, "FETCH 1 "+item)
	requireOK(t, tagged, "F1")
	if len(replies) != 1 {
		t.Fatalf("%#v", replies)
	}
	require7bit(t, item, replies[0].framing)
	return replies[0].framing, strings.Join(replies[0].literals, "|")
}

// requireAllSessions proves the fields of raw in a top-level ENVELOPE and an
// encapsulated one, with and without UTF8=ACCEPT. Every string in want is 7-bit, so
// it reads the same whichever the session and needs no literal.
func requireAllSessions(t *testing.T, raw string, want ...string) {
	t.Helper()
	encapsulated := "Content-Type: message/rfc822\r\n\r\n" + raw

	c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
	requireHolds(t, fetchMetadata(t, c, "ENVELOPE"), want...)
	c = fetchClient(t, sevenBitBox(encapsulated), "EXAMINE")
	requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), want...)
	c = fetchClient(t, sevenBitBox(encapsulated), "EXAMINE")
	requireHolds(t, fetchMetadata(t, c, "BODY"), want...)

	framing, _ := fetchUTF8Metadata(t, raw, "ENVELOPE")
	requireHolds(t, framing, want...)
	framing, _ = fetchUTF8Metadata(t, encapsulated, "BODYSTRUCTURE")
	requireHolds(t, framing, want...)
}

// TestAddressNameInCharsetNotDecoded: a name encoded in a charset go-imap has no
// reader for stays the encoded word it was sent as, which already names its charset,
// so no byte is guessed. The address stays, the other addresses in its field stay,
// and Sender and Reply-To still take their From default.
//
// checklist: IMAP-ENV-13
func TestAddressNameInCharsetNotDecoded(t *testing.T) {
	for _, word := range []string{
		"=?windows-1252?Q?J=F6rg?=",
		"=?WINDOWS-1252?B?SvZyZw==?=",
		"=?ISO-8859-2?Q?=A3ukasz?=",
		"=?iso-8859-2?b?o3VrYXN6?=",
		"=?x-no-such-charset?Q?J=F6rg?=",
		"=?UNKNOWN-8BIT?Q?J=F6rg?=",
	} {
		t.Run(word, func(t *testing.T) {
			raw := "Subject: s\r\n" +
				"From: " + word + " <j@example.com>, Ann <a@example.com>\r\n" +
				"To: Bo <b@example.com>, " + word + " <t@example.com>\r\n" +
				"\r\nbody\r\n"
			from := `(("` + word + `" NIL "j" "example.com")("Ann" NIL "a" "example.com"))`
			to := `(("Bo" NIL "b" "example.com")("` + word + `" NIL "t" "example.com"))`
			requireAllSessions(t, raw, `"s" `+from+" "+from+" "+from+" "+to+" NIL NIL NIL NIL")
		})
	}
}

// TestAddressNameCharsetInEveryPosition: a group name, a comment naming the mailbox
// and adjacent encoded words are parsed by the same decoder as a personal name.
// Adjacent words stay separate, so each still reads as an encoded word (RFC 2047 §2).
//
// checklist: IMAP-ENV-13
func TestAddressNameCharsetInEveryPosition(t *testing.T) {
	raw := "Subject: s\r\n" +
		"From: =?windows-1252?Q?J=F6?= =?ISO-8859-2?B?o3U=?= <j@example.com>\r\n" +
		"Sender: s@example.com (=?windows-1252?Q?J=F6rg?=)\r\n" +
		"To: =?ISO-8859-2?Q?Gr=F9?=: =?x-no-such-charset?Q?J=F6rg?= <g@example.com>, h@example.com;, k@example.com\r\n" +
		"\r\nbody\r\n"
	requireAllSessions(t, raw,
		`(("=?windows-1252?Q?J=F6?= =?ISO-8859-2?B?o3U=?=" NIL "j" "example.com"))`,
		`(("=?windows-1252?Q?J=F6rg?=" NIL "s" "example.com"))`,
		`((NIL NIL "=?ISO-8859-2?Q?Gr=F9?=" NIL)`+
			`("=?x-no-such-charset?Q?J=F6rg?=" NIL "g" "example.com")`+
			`(NIL NIL "h" "example.com")(NIL NIL NIL NIL)(NIL NIL "k" "example.com"))`,
	)
}

// TestAddressNameMixesDecodedAndKeptWords: the text go-imap decoded follows the
// session's rule, and the word it kept is left alone in either session. Encoding
// that word again would make a client show its raw spelling.
//
// checklist: IMAP-ENV-13
func TestAddressNameMixesDecodedAndKeptWords(t *testing.T) {
	const raw = "Subject: s\r\n" +
		"From: =?UTF-8?Q?M=C3=BCller?= =?windows-1252?Q?J=F6rg?= Ann <m@example.com>\r\n" +
		"\r\nbody\r\n"
	c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
	requireHolds(t, fetchMetadata(t, c, "ENVELOPE"),
		`(("=?UTF-8?Q?M=C3=BCller?= =?windows-1252?Q?J=F6rg?= Ann" NIL "m" "example.com"))`)

	framing, literals := fetchUTF8Metadata(t, raw, "ENVELOPE")
	requireHolds(t, framing, `NIL "m" "example.com"`)
	requireHolds(t, literals, "Müller =?windows-1252?Q?J=F6rg?= Ann")
}

// TestAddressNameCharsetGoIMAPDecodes: a charset go-imap has a reader for is decoded
// under every name it knows, then follows the session's rule like any decoded name.
//
// checklist: IMAP-ENV-13
func TestAddressNameCharsetGoIMAPDecodes(t *testing.T) {
	for _, charset := range []string{"ISO-8859-1", "latin1", "iso8859-1", "L1"} {
		t.Run(charset, func(t *testing.T) {
			raw := "Subject: s\r\nFrom: =?" + charset + "?Q?caf=E9?= <c@example.com>\r\n\r\nbody\r\n"
			c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
			requireHolds(t, fetchMetadata(t, c, "ENVELOPE"), `(("=?UTF-8?Q?caf=C3=A9?=" NIL "c" "example.com"))`)

			_, literals := fetchUTF8Metadata(t, raw, "ENVELOPE")
			requireHolds(t, literals, "café")
		})
	}
}

// TestAddressNameOctetsNotUTF8: a raw name that isn't UTF-8 becomes an UNKNOWN-8BIT
// encoded word on the way out, which go-imap has no reader for. That word stays, so
// the address it names does too.
//
// checklist: IMAP-ENV-13
func TestAddressNameOctetsNotUTF8(t *testing.T) {
	const raw = "Subject: s\r\nFrom: J\xf6rg <j@example.com>, Ann <a@example.com>\r\n\r\nbody\r\n"
	const from = `(("=?UNKNOWN-8BIT?Q?J=F6rg?=" NIL "j" "example.com")("Ann" NIL "a" "example.com"))`
	requireAllSessions(t, raw, `"s" `+from+" "+from+" "+from)
}
