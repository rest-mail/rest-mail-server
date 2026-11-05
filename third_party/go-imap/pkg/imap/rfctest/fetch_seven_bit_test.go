package rfctest

import (
	"encoding/base64"
	"fmt"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/wireform"
)

// sevenBitBinary holds every octet value but NUL, so its encoded form proves the
// data arrives intact.
var sevenBitBinary = func() string {
	var b strings.Builder
	for i := 1; i < 256; i++ {
		b.WriteByte(byte(i))
	}
	return b.String()
}()

// sevenBitStored is a multipart message with 8-bit headers at every level: UTF-8
// text in a charset part, Latin-1 octets in a header, binary data, charsetless
// 8-bit text, and an encapsulated message.
var sevenBitStored = "Subject: Gr\xc3\xbc\xc3\x9fe aus K\xc3\xb6ln\r\n" +
	"X-Latin: caf\xe9\r\n" +
	"From: \"M\xc3\xbcller, J\xc3\xb6rg\" <joerg@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"B\"\r\n" +
	"\r\n" +
	"pre\xe9amble\r\n" +
	"--B\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: 8bit\r\n" +
	"Content-Description: Gr\xc3\xbc\xc3\x9fe\r\n" +
	"\r\n" +
	"Hallo J\xc3\xb6rg\r\n" +
	"--B\r\n" +
	"Content-Type: application/octet-stream\r\n" +
	"Content-Disposition: attachment; filename=\"Gr\xc3\xbc\xc3\x9fe.bin\"\r\n" +
	"Content-Transfer-Encoding: binary\r\n" +
	"\r\n" +
	sevenBitBinary + "\r\n" +
	"--B\r\n" +
	"Content-Type: text/plain\r\n" +
	"\r\n" +
	"no charset \xe9\r\n" +
	"--B\r\n" +
	"Content-Type: message/rfc822\r\n" +
	"\r\n" +
	"Subject: inner \xc3\xa9\r\n" +
	"\r\n" +
	"inner body\r\n" +
	"--B--\r\n"

func sevenBitBox(raw string) *fetchMailbox {
	box := newFetchMailbox()
	box.messages = []imap.Message{{UID: 10, Size: len(wireform.Convert(raw, false))}}
	box.raw = map[uint32]string{10: raw}
	return box
}

// fetchLiteral answers one FETCH item that is sent as a literal.
func fetchLiteral(t *testing.T, c *client, item string) (framing, payload string) {
	t.Helper()
	replies, tagged := fetchCommand(t, c, "FETCH 1 "+item)
	requireOK(t, tagged, "F1")
	if len(replies) != 1 || len(replies[0].literals) != 1 {
		t.Fatalf("%s: %#v", item, replies)
	}
	return replies[0].framing, replies[0].literals[0]
}

func require7bit(t *testing.T, what, data string) {
	t.Helper()
	for i := 0; i < len(data); i++ {
		if data[i] >= 0x80 || data[i] == 0 {
			t.Fatalf("%s holds octet %#x at %d: %q", what, data[i], i, data)
		}
	}
}

// TestFetchHeaderSectionsSevenBit: headers "MUST be 7-bit; 8-bit characters are
// not permitted in headers" (RFC 3501 §7.4.2), in HEADER, MIME and the header of
// an encapsulated message alike. Valid UTF-8 becomes encoded words in charset
// UTF-8; anything else uses UNKNOWN-8BIT, so the octets survive and no charset is
// guessed. Fields already 7-bit are untouched.
//
// checklist: IMAP-RESP-16 IMAP-INV-TEXT-02
func TestFetchHeaderSectionsSevenBit(t *testing.T) {
	c := fetchClient(t, sevenBitBox(sevenBitStored), "EXAMINE")
	for _, item := range []string{
		"BODY.PEEK[HEADER]", "RFC822.HEADER", "BODY.PEEK[HEADER.FIELDS (SUBJECT X-LATIN)]",
		"BODY.PEEK[HEADER.FIELDS.NOT (FROM)]", "BODY.PEEK[1.MIME]", "BODY.PEEK[2.MIME]",
		"BODY.PEEK[4.HEADER]", "BODY.PEEK[4.1.MIME]",
	} {
		_, payload := fetchLiteral(t, c, item)
		require7bit(t, item, payload)
	}

	_, header := fetchLiteral(t, c, "BODY.PEEK[HEADER]")
	msg, err := mail.ReadMessage(strings.NewReader(header))
	if err != nil {
		t.Fatalf("HEADER no longer parses: %v\n%s", err, header)
	}
	dec := new(mime.WordDecoder)
	if got, err := dec.DecodeHeader(msg.Header.Get("Subject")); err != nil || got != "Grüße aus Köln" {
		t.Fatalf("Subject decodes to %q (%v) from %q", got, err, msg.Header.Get("Subject"))
	}
	if !strings.Contains(header, "Subject: =?UTF-8?Q?Gr=C3=BC=C3=9Fe?= aus =?UTF-8?Q?K=C3=B6ln?=\r\n") {
		t.Fatalf("Subject is not an encoded word in charset UTF-8: %q", header)
	}
	if !strings.Contains(header, "X-Latin: =?UNKNOWN-8BIT?Q?caf=E9?=\r\n") {
		t.Fatalf("Latin-1 octets are not UNKNOWN-8BIT: %q", header)
	}
	addr, err := mail.ParseAddress(msg.Header.Get("From"))
	if err != nil || addr.Name != "Müller, Jörg" || addr.Address != "joerg@example.com" {
		t.Fatalf("From reads as %+v (%v) from %q", addr, err, msg.Header.Get("From"))
	}
	if !strings.Contains(header, "MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"B\"\r\n\r\n") {
		t.Fatalf("7-bit fields changed: %q", header)
	}

	_, disposition := fetchLiteral(t, c, "BODY.PEEK[2.MIME]")
	part, err := mail.ReadMessage(strings.NewReader(disposition))
	if err != nil {
		t.Fatalf("2.MIME: %v", err)
	}
	if _, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition")); err != nil || params["filename"] != "Grüße.bin" {
		t.Fatalf("filename reads as %q (%v) from %q", params["filename"], err, disposition)
	}
}

// TestFetchBodyDataSevenBit: "8-bit textual data is permitted if a [CHARSET]
// identifier is part of the body parameter parenthesized list"; "non-textual data
// such as binary data MUST be transfer encoded into a textual form, such as
// BASE64" (RFC 3501 §7.4.2). Text naming no charset is not identified, so it is
// transfer-encoded too, and decodes to the stored octets.
//
// checklist: IMAP-RESP-16 IMAP-INV-TEXT-01 IMAP-INV-TEXT-02
func TestFetchBodyDataSevenBit(t *testing.T) {
	c := fetchClient(t, sevenBitBox(sevenBitStored), "EXAMINE")

	_, text := fetchLiteral(t, c, "BODY.PEEK[1]")
	if text != "Hallo J\xc3\xb6rg" {
		t.Fatalf("text naming a charset changed: %q", text)
	}

	_, binaryHeader := fetchLiteral(t, c, "BODY.PEEK[2.MIME]")
	if !strings.Contains(binaryHeader, "Content-Transfer-Encoding: base64\r\n") || strings.Contains(strings.ToLower(binaryHeader), "encoding: binary") {
		t.Fatalf("binary part's encoding field: %q", binaryHeader)
	}
	_, binary := fetchLiteral(t, c, "BODY.PEEK[2]")
	require7bit(t, "BODY[2]", binary)
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(binary, "\r\n", ""))
	if err != nil || string(decoded) != sevenBitBinary {
		t.Fatalf("BASE64 decodes to %q (%v)", decoded, err)
	}

	_, bareHeader := fetchLiteral(t, c, "BODY.PEEK[3.MIME]")
	_, bare := fetchLiteral(t, c, "BODY.PEEK[3]")
	if !strings.Contains(bareHeader, "Content-Transfer-Encoding: quoted-printable\r\n") || bare != "no charset =E9" {
		t.Fatalf("charsetless 8-bit text: %q, %q", bareHeader, bare)
	}

	_, inner := fetchLiteral(t, c, "BODY.PEEK[4]")
	if inner != "Subject: inner =?UTF-8?Q?=C3=A9?=\r\n\r\ninner body" {
		t.Fatalf("encapsulated message: %q", inner)
	}

	// Only the 8-bit text that names its charset may remain in the whole message.
	_, whole := fetchLiteral(t, c, "BODY.PEEK[]")
	require7bit(t, "BODY[] without the charset part", strings.Replace(whole, "Hallo J\xc3\xb6rg", "", 1))
	if strings.Contains(whole, "pre\xe9amble") || !strings.Contains(whole, "pre=E9amble\r\n--B\r\n") {
		t.Fatalf("preamble: %q", whole)
	}
	// A MIME reader finds the same four parts in what is sent.
	mp := multipart.NewReader(strings.NewReader(mime7bitBody(t, whole)), "B")
	for i := 1; i <= 4; i++ {
		if _, err := mp.NextPart(); err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
	}
}

func mime7bitBody(t *testing.T, whole string) string {
	t.Helper()
	_, body, ok := strings.Cut(whole, "\r\n\r\n")
	if !ok {
		t.Fatalf("no body in %q", whole)
	}
	return body
}

// TestFetchEncodedBodyWithStray8bit: a body already declared base64 or
// quoted-printable keeps its encoding, so nothing is encoded twice. Octets the
// base64 alphabet lacks are ignored by readers (RFC 2045 §6.8); quoted-printable
// spells them. An unknown encoding cannot be removed, so the entity goes out as
// opaque BASE64 data (RFC 2045 §6.4).
//
// checklist: IMAP-INV-TEXT-01
func TestFetchEncodedBodyWithStray8bit(t *testing.T) {
	for _, tc := range []struct{ name, header, body, wantHeader, wantBody string }{
		{"base64", "Content-Type: image/png\r\nContent-Transfer-Encoding: base64\r\n", "aGk\xe9=\r\n",
			"Content-Type: image/png\r\nContent-Transfer-Encoding: base64\r\n", "aGk=\r\n"},
		{"quoted-printable", "Content-Type: image/png\r\nContent-Transfer-Encoding: quoted-printable\r\n", "h\xe9=20\r\n",
			"Content-Type: image/png\r\nContent-Transfer-Encoding: quoted-printable\r\n", "h=E9=20\r\n"},
		{"unknown encoding", "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: x-opaque\r\n", "\xe9",
			"Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n", "6Q=="},
		{"no type", "Content-Transfer-Encoding: 8bit\r\n", "\xe9\r\n",
			"Content-Transfer-Encoding: quoted-printable\r\n", "=E9\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fetchClient(t, sevenBitBox(tc.header+"\r\n"+tc.body), "EXAMINE")
			_, got := fetchLiteral(t, c, "BODY.PEEK[]")
			if want := tc.wantHeader + "\r\n" + tc.wantBody; got != want {
				t.Fatalf("BODY[] = %q, want %q", got, want)
			}
		})
	}
}

// TestFetchSizesDescribeSentOctets: RFC822.SIZE, partial offsets, literals and
// BODYSTRUCTURE's sizes and encodings all describe the octets the client gets,
// not the stored ones.
//
// checklist: IMAP-RESP-16
func TestFetchSizesDescribeSentOctets(t *testing.T) {
	box := sevenBitBox(sevenBitStored)
	c := fetchClient(t, box, "EXAMINE")
	replies, tagged := fetchCommand(t, c, "FETCH 1 (RFC822.SIZE BODY.PEEK[])")
	requireOK(t, tagged, "F1")
	whole := replies[0].literals[0]
	if len(whole) == len(sevenBitStored) {
		t.Fatal("conversion did not change the size, so this test proves nothing")
	}
	if want := fmt.Sprintf("RFC822.SIZE %d ", len(whole)); !strings.Contains(replies[0].framing, want) {
		t.Fatalf("framing %q lacks %q", replies[0].framing, want)
	}

	// The offset counts sent octets: the same window of what was sent.
	const start, count = 40, 25
	framing, partial := fetchLiteral(t, c, fmt.Sprintf("BODY.PEEK[]<%d.%d>", start, count))
	if partial != whole[start:start+count] || !strings.Contains(framing, fmt.Sprintf("BODY[]<%d> {%d}", start, count)) {
		t.Fatalf("partial %q %q, want %q", framing, partial, whole[start:start+count])
	}

	_, binary := fetchLiteral(t, c, "BODY.PEEK[2]")
	replies, tagged = fetchCommand(t, c, "FETCH 1 BODYSTRUCTURE")
	requireOK(t, tagged, "F1")
	if want := fmt.Sprintf(`"BASE64" %d`, len(binary)); !strings.Contains(replies[0].framing, want) {
		t.Fatalf("BODYSTRUCTURE %q lacks %q", replies[0].framing, want)
	}
	if strings.Contains(replies[0].framing, `"BINARY"`) {
		t.Fatalf("BODYSTRUCTURE describes the stored encoding: %q", replies[0].framing)
	}

	// The stored octets are not what was changed.
	if box.raw[10] != sevenBitStored {
		t.Fatal("conversion changed the stored message")
	}
}

// TestFetchSevenBitMessageByteForByte: a message already 7-bit goes out byte for
// byte, so a DKIM signature over it still verifies from FETCH output. Only a
// message that actually needed converting differs.
//
// checklist: IMAP-RESP-16 IMAP-INV-TEXT-02
func TestFetchSevenBitMessageByteForByte(t *testing.T) {
	const raw = "DKIM-Signature: v=1; a=rsa-sha256; d=example.com; s=s;\r\n" +
		"\th=from:subject; bh=abc=; b=def=\r\n" +
		"Subject:   =?UTF-8?Q?Gr=C3=BC=C3=9Fe?=  \r\n" +
		"Content-Type: multipart/mixed;\r\n boundary=B\r\n\r\n" +
		"preamble\r\n--B  \r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\nAAEC\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nplain   \r\n--B--\r\nepilogue\r\n"
	c := fetchClient(t, sevenBitBox(raw), "EXAMINE")
	if _, got := fetchLiteral(t, c, "BODY.PEEK[]"); got != raw {
		t.Fatalf("7-bit message changed:\n%q\nwant\n%q", got, raw)
	}
	if got := wireform.Convert(raw, false); got != raw {
		t.Fatalf("Convert changed a 7-bit message: %q", got)
	}
	// A single 8-bit header field leaves every other field and the body as stored.
	signed := "DKIM-Signature: v=1; b=def=\r\nX-Note: caf\xc3\xa9\r\nSubject: x\r\n\r\nbody\r\n"
	if got, want := wireform.Convert(signed, false), "DKIM-Signature: v=1; b=def=\r\nX-Note: =?UTF-8?Q?caf=C3=A9?=\r\nSubject: x\r\n\r\nbody\r\n"; got != want {
		t.Fatalf("Convert = %q, want %q", got, want)
	}
}

// TestFetchBinaryKeepsStoredOctets: RFC 3516 BINARY is the way to the octets, so
// it reads the stored form, and BINARY.SIZE counts it, whatever BODY sends.
//
// checklist: IMAP-RESP-16
func TestFetchBinaryKeepsStoredOctets(t *testing.T) {
	c := fetchClient(t, sevenBitBox(sevenBitStored), "EXAMINE")
	if _, got := fetchLiteral(t, c, "BINARY.PEEK[2]"); got != sevenBitBinary {
		t.Fatalf("BINARY[2] = %q", got)
	}
	replies, tagged := fetchCommand(t, c, "FETCH 1 BINARY.SIZE[2]")
	requireOK(t, tagged, "F1")
	if want := fmt.Sprintf("BINARY.SIZE[2] %d", len(sevenBitBinary)); !strings.Contains(replies[0].framing, want) {
		t.Fatalf("framing %q lacks %q", replies[0].framing, want)
	}
	if _, got := fetchLiteral(t, c, "BINARY.PEEK[3]"); got != "no charset \xe9" {
		t.Fatalf("BINARY[3] = %q", got)
	}

	// Binary data holding a NUL can be sent now that it is encoded, and BINARY
	// sends it as a literal8. A NUL in a header or in text goes out as a space
	// (IMAP-ENV-12).
	box := sevenBitBox("Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n\r\na\x00b\r\n")
	nul := fetchClient(t, box, "EXAMINE")
	if _, got := fetchLiteral(t, nul, "BODY.PEEK[TEXT]"); got != base64.StdEncoding.EncodeToString([]byte("a\x00b\r\n"))+"\r\n" {
		t.Fatalf("BODY[TEXT] of data with a NUL = %q", got)
	}
	replies, tagged = fetchCommand(t, nul, "FETCH 1 BINARY.PEEK[1]")
	requireOK(t, tagged, "F1")
	if len(replies) != 1 || !strings.Contains(replies[0].framing, "~{") {
		t.Fatalf("BINARY[1] of data with a NUL: %#v", replies)
	}
}

// utf8FetchMailbox is a fetch fixture on a backend that takes ENABLE UTF8=ACCEPT.
type utf8FetchMailbox struct{ *fetchMailbox }

func (utf8FetchMailbox) EnableUTF8() error { return nil }

// TestFetchUTF8SessionKeepsValidUTF8: after ENABLE UTF8=ACCEPT (RFC 6855), valid
// UTF-8 stays in headers and in text naming no charset. Other octets and
// non-textual data are converted as for any session.
//
// checklist: IMAP-RESP-16
func TestFetchUTF8SessionKeepsValidUTF8(t *testing.T) {
	const raw = "Subject: Gr\xc3\xbc\xc3\x9fe\r\nX-Latin: caf\xe9\r\n\r\nHallo J\xc3\xb6rg\r\n"
	box := sevenBitBox(raw)
	c := selectionClient(t, utf8FetchMailbox{box})
	_, tagged := c.command("E1", "ENABLE UTF8=ACCEPT")
	requireOK(t, tagged, "E1")
	_, tagged = c.command("S1", "EXAMINE INBOX")
	requireOK(t, tagged, "S1")

	const want = "Subject: Gr\xc3\xbc\xc3\x9fe\r\nX-Latin: =?UNKNOWN-8BIT?Q?caf=E9?=\r\n\r\nHallo J\xc3\xb6rg\r\n"
	if _, got := fetchLiteral(t, c, "BODY.PEEK[]"); got != want {
		t.Fatalf("BODY[] = %q, want %q", got, want)
	}
	if got := wireform.Convert(raw, true); got != want {
		t.Fatalf("Convert = %q, want %q", got, want)
	}
}

// TestEncodedWordsStayWithinLimits: an encoded word is at most 75 characters (RFC
// 2047 §2) and a UTF-8 character is never split across two (§5), so a long value
// still decodes to itself.
//
// checklist: IMAP-INV-TEXT-02
func TestEncodedWordsStayWithinLimits(t *testing.T) {
	long := strings.Repeat("Grüße ", 40)
	long = strings.TrimSpace(long)
	raw := "Subject: " + long + "\r\n\r\nbody\r\n"
	got := wireform.Convert(raw, false)
	require7bit(t, "converted", got)
	msg, err := mail.ReadMessage(strings.NewReader(got))
	if err != nil {
		t.Fatalf("%v\n%s", err, got)
	}
	dec := new(mime.WordDecoder)
	if decoded, err := dec.DecodeHeader(msg.Header.Get("Subject")); err != nil || decoded != long {
		t.Fatalf("decoded %q (%v), want %q", decoded, err, long)
	}
	for _, word := range strings.Fields(msg.Header.Get("Subject")) {
		if len(word) > 75 {
			t.Fatalf("word of %d characters: %q", len(word), word)
		}
	}
	header, _, _ := strings.Cut(got, "\r\n\r\n")
	for _, line := range strings.Split(header, "\r\n") {
		if len(line) > 76 {
			t.Fatalf("line of %d characters: %q", len(line), line)
		}
	}
}
