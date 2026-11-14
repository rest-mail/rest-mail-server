package rfctest

import (
	"mime"
	"strings"
	"testing"
)

// fetchMetadata answers FETCH item with no literal and nothing 8-bit: RFC 3501
// §4.3.1 lets 8-bit text travel only with its charset identified, and an ENVELOPE
// or BODYSTRUCTURE string has nowhere to name one.
func fetchMetadata(t *testing.T, c *client, item string) string {
	t.Helper()
	replies, tagged := fetchCommand(t, c, "FETCH 1 "+item)
	requireOK(t, tagged, "F1")
	if len(replies) != 1 || len(replies[0].literals) != 0 {
		t.Fatalf("%s sent a literal: %#v", item, replies)
	}
	require7bit(t, item, replies[0].framing)
	return replies[0].framing
}

func requireHolds(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("%q lacks %q", got, w)
		}
	}
}

// TestEnvelopeStringsSevenBit: a personal or group name decoded from an encoded
// word, and an 8-bit header at the top of the message, go out as encoded words in
// a session that has not enabled UTF-8.
//
// checklist: IMAP-INV-TEXT-01
func TestEnvelopeStringsSevenBit(t *testing.T) {
	const raw = "Date: Thu, 02 Jan 2020 03:04:05 +0000\r\n" +
		"Subject: Gr\xc3\xbc\xc3\x9fe\r\n" +
		"From: =?UTF-8?Q?J=C3=B6rg?= <j@example.com>\r\n" +
		"Reply-To: =?ISO-8859-1?Q?caf=E9?= <c@example.com>\r\n" +
		"To: =?UTF-8?Q?Gr=C3=BCppe?=: M\xc3\xbcller <m@example.com>;\r\n" +
		"Cc: Gr\xc3\xbcppe: g@example.com;\r\n" +
		"Bcc: \"M\xc3\xbcller, J\xc3\xb6rg\" <b@example.com>\r\n" +
		"\r\nbody\r\n"
	c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
	got := fetchMetadata(t, c, "ENVELOPE")
	requireHolds(t, got,
		`"Thu, 02 Jan 2020 03:04:05 +0000"`,
		`"=?UTF-8?Q?Gr=C3=BC=C3=9Fe?="`,
		`(("=?UTF-8?Q?J=C3=B6rg?=" NIL "j" "example.com"))`,
		`(("=?UTF-8?Q?caf=C3=A9?=" NIL "c" "example.com"))`,
		`((NIL NIL "=?UTF-8?Q?Gr=C3=BCppe?=" NIL)("=?UTF-8?Q?M=C3=BCller?=" NIL "m" "example.com")(NIL NIL NIL NIL))`,
		`((NIL NIL "=?UTF-8?Q?Gr=C3=BCppe?=" NIL)(NIL NIL "g" "example.com")(NIL NIL NIL NIL))`,
		`(("=?UTF-8?Q?M=C3=BCller=2C_J=C3=B6rg?=" NIL "b" "example.com"))`,
	)
}

// TestEnvelopeTextNotUTF8SevenBit: octets that are not UTF-8 are UNKNOWN-8BIT, so
// they survive and no charset is guessed.
//
// checklist: IMAP-INV-TEXT-01
func TestEnvelopeTextNotUTF8SevenBit(t *testing.T) {
	c := fetchClient(t, sevenBitBox("Subject: caf\xe9\r\nFrom: j@example.com\r\n\r\nbody\r\n"), "EXAMINE")
	requireHolds(t, fetchMetadata(t, c, "ENVELOPE"), `"=?UNKNOWN-8BIT?Q?caf=E9?="`)
}

// TestLongDecodedNameStaysOneString: a decoded name too long for one encoded word
// (RFC 2047 §2) is several in one quoted string, not a literal holding a fold, and
// it still decodes to itself with no character split across two (§5).
//
// checklist: IMAP-INV-TEXT-01
func TestLongDecodedNameStaysOneString(t *testing.T) {
	name := strings.TrimSpace(strings.Repeat("Grüße ", 20))
	raw := "From: " + mime.QEncoding.Encode("utf-8", name) + " <j@example.com>\r\n\r\nbody\r\n"
	c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
	got := fetchMetadata(t, c, "ENVELOPE")
	start := strings.Index(got, `(("=?UTF-8?Q?`)
	if start < 0 {
		t.Fatalf("no encoded personal name in %q", got)
	}
	start += len(`(("`)
	end := start + strings.IndexByte(got[start:], '"')
	words := got[start:end]
	for _, word := range strings.Fields(words) {
		if len(word) > 75 {
			t.Fatalf("encoded word of %d characters: %q", len(word), word)
		}
	}
	dec := new(mime.WordDecoder)
	if decoded, err := dec.DecodeHeader(words); err != nil || decoded != name {
		t.Fatalf("name decodes to %q (%v) from %q", decoded, err, words)
	}
}

// metadataStored holds every BODYSTRUCTURE string that can come from a decoder:
// RFC 2231 parameters in a part and a multipart, disposition parameters, and an
// encapsulated message whose envelope names people and a group.
const metadataStored = "Content-Type: multipart/mixed; boundary=B; note*=utf-8''caf%C3%A9\r\n\r\n" +
	"--B\r\n" +
	"Content-Type: text/plain; charset=utf-8; name*=utf-8''caf%C3%A9.txt\r\n" +
	"Content-Disposition: attachment; filename*=utf-8''Gr%C3%BC%C3%9Fe.txt\r\n" +
	"Content-Description: Gr\xc3\xbc\xc3\x9fe\r\n" +
	"\r\nHallo\r\n" +
	"--B\r\n" +
	"Content-Type: application/octet-stream; name*=utf-8''caf%E9.bin\r\n" +
	"Content-Disposition: attachment; filename=\"Gr\xc3\xbc\xc3\x9fe.bin\"\r\n" +
	"\r\ndata\r\n" +
	"--B\r\n" +
	"Content-Type: message/rfc822\r\n\r\n" +
	"From: =?UTF-8?Q?J=C3=B6rg?= <j@example.com>\r\n" +
	"Cc: =?UTF-8?Q?Gr=C3=BCppe?=: g@example.com;\r\n" +
	"To: j\xc3\xb6rg@ex\xc3\xa4mple.com\r\n" +
	"Content-Type: text/plain; name*=utf-8''inner%C3%A9\r\n" +
	"\r\ninner\r\n" +
	"--B--\r\n"

// TestBodyStructureStringsSevenBit: RFC 2231 values decoded to 8-bit, and names
// in an encapsulated message's envelope, go out as encoded words in BODY and
// BODYSTRUCTURE alike.
//
// checklist: IMAP-INV-TEXT-01
func TestBodyStructureStringsSevenBit(t *testing.T) {
	c := fetchClient(t, sevenBitBox(metadataStored), "EXAMINE")
	shared := []string{
		`("CHARSET" "utf-8" "NAME" "=?UTF-8?Q?caf=C3=A9=2Etxt?=")`,
		`"=?UTF-8?Q?Gr=C3=BC=C3=9Fe?="`,
		`("NAME" "=?UNKNOWN-8BIT?Q?caf=E9=2Ebin?=")`,
		`("=?UTF-8?Q?J=C3=B6rg?=" NIL "j" "example.com")`,
		`((NIL NIL "=?UTF-8?Q?Gr=C3=BCppe?=" NIL)(NIL NIL "g" "example.com")(NIL NIL NIL NIL))`,
		`("CHARSET" "US-ASCII" "NAME" "=?UTF-8?Q?inner=C3=A9?=")`,
	}
	requireHolds(t, fetchMetadata(t, c, "BODY"), shared...)
	requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), append(shared,
		`"NOTE" "=?UTF-8?Q?caf=C3=A9?="`,
		`("ATTACHMENT" ("FILENAME" "=?UTF-8?Q?Gr=C3=BC=C3=9Fe=2Etxt?="))`,
		`("ATTACHMENT" ("FILENAME" "=?UTF-8?Q?Gr=C3=BC=C3=9Fe=2Ebin?="))`,
	)...)
}

// TestAddressWithNoSevenBitFormIsNIL: an encoded word may not stand in an addr-spec
// (RFC 2047 §5), so without UTF-8 an 8-bit mailbox has no 7-bit form and its field
// reads NIL, at the top of the message and in an encapsulated one alike.
//
// checklist: IMAP-INV-TEXT-01
func TestAddressWithNoSevenBitFormIsNIL(t *testing.T) {
	const eightBit = "From: f@example.com\r\nTo: j\xc3\xb6rg@ex\xc3\xa4mple.com\r\nSubject: s\r\n\r\nbody\r\n"
	c := fetchClient(t, sevenBitBox(eightBit), "EXAMINE")
	const want = `"s" ((NIL NIL "f" "example.com")) ((NIL NIL "f" "example.com")) ((NIL NIL "f" "example.com")) NIL NIL NIL NIL NIL`
	requireHolds(t, fetchMetadata(t, c, "ENVELOPE"), want)

	c = fetchClient(t, sevenBitBox("Content-Type: message/rfc822\r\n\r\n"+eightBit), "EXAMINE")
	requireHolds(t, fetchMetadata(t, c, "BODYSTRUCTURE"), want)
}

// TestMetadataStringsInUTF8Session: after ENABLE UTF8=ACCEPT (RFC 6855) valid UTF-8
// goes out as UTF-8, the charset the session agreed, framed as a literal. Text that
// is not UTF-8 is not allowed there either, so it is still an encoded word.
//
// checklist: IMAP-INV-TEXT-01 IMAP-BS-13 IMAP-ENV-08 IMAP-ENV-13
func TestMetadataStringsInUTF8Session(t *testing.T) {
	const raw = "From: =?UTF-8?Q?J=C3=B6rg?= <j@example.com>\r\nTo: j\xc3\xb6rg@ex\xc3\xa4mple.com\r\n" +
		"Subject: caf\xe9\r\nMessage-ID: <\xc3\xa9@example.com>\r\n" +
		"Content-Type: application/octet-stream; name*=utf-8''caf%C3%A9; note*=utf-8''caf%E9\r\n\r\nbody\r\n"
	c := selectionClient(t, utf8FetchMailbox{sevenBitBox(raw)})
	_, tagged := c.command("E1", "ENABLE UTF8=ACCEPT")
	requireOK(t, tagged, "E1")
	_, tagged = c.command("S1", "EXAMINE INBOX")
	requireOK(t, tagged, "S1")

	replies, tagged := fetchCommand(t, c, "FETCH 1 (ENVELOPE BODYSTRUCTURE)")
	requireOK(t, tagged, "F1")
	if len(replies) != 1 {
		t.Fatalf("%#v", replies)
	}
	require7bit(t, "framing", replies[0].framing)
	requireHolds(t, replies[0].framing,
		`"=?UNKNOWN-8BIT?Q?caf=E9?=" (({5}`, // Subject, then the name From decodes to
		`"NOTE" "=?UNKNOWN-8BIT?Q?caf=E9?="`,
	)
	literals := strings.Join(replies[0].literals, "|")
	for _, want := range []string{"Jörg", "jörg", "exämple.com", "<é@example.com>", "café"} {
		if !strings.Contains(literals, want) {
			t.Fatalf("literals %q lack %q", literals, want)
		}
	}
}
