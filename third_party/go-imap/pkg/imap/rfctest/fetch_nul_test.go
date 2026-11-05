package rfctest

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/wireform"
)

// nulRaw holds a NUL in a top-level header, a part's MIME header, text naming a
// charset, a preamble and an epilogue. Part 2 is data, part 3 is text with no charset.
const nulRaw = "Subject: a\x00b\r\nFrom: j@example.com\r\n" +
	"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
	"pre\x00amble\r\n" +
	"--B\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Description: d\x00e\r\n\r\nHal\x00lo J\xc3\xb6rg\r\n" +
	"--B\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n\r\n\x00\x01\xff\x00data\r\n" +
	"--B\r\nContent-Type: text/plain\r\n\r\nplain\x00text\r\n" +
	"--B--\r\nepi\x00logue\r\n"

// A message holding a NUL reads through every section and partial, with a space for
// each NUL in a header or in text, and the size reported is the size sent.
//
// checklist: IMAP-ENV-12
func TestFetchNULSections(t *testing.T) {
	for _, session := range []string{"plain", "UTF8=ACCEPT"} {
		t.Run(session, func(t *testing.T) {
			sessionUTF8 := session != "plain"
			box := sevenBitBox(nulRaw)
			box.messages[0].Size = wireform.Size(nulRaw, sessionUTF8)
			var c *client
			if sessionUTF8 {
				c = utf8FetchClient(t, box)
			} else {
				c = fetchClient(t, box, "EXAMINE")
			}
			_, whole := fetchLiteral(t, c, "BODY.PEEK[]")
			if strings.IndexByte(whole, 0) >= 0 {
				t.Fatalf("BODY[] holds a NUL: %q", whole)
			}
			if got := len(whole); got != box.messages[0].Size {
				t.Fatalf("BODY[] sent %d octets, Size says %d", got, box.messages[0].Size)
			}
			end := strings.Index(whole, "\r\n\r\n") + 4
			for _, tc := range []struct{ item, want string }{
				{"RFC822", whole},
				{"RFC822.HEADER", whole[:end]},
				{"RFC822.TEXT", whole[end:]},
				{"BODY.PEEK[HEADER]", whole[:end]},
				{"BODY.PEEK[TEXT]", whole[end:]},
				{"BODY.PEEK[HEADER.FIELDS (SUBJECT)]", "Subject: a b\r\n\r\n"},
				{"BODY.PEEK[1.MIME]", "Content-Type: text/plain; charset=utf-8\r\nContent-Description: d e\r\n\r\n"},
				{"BODY.PEEK[1]", "Hal lo J\xc3\xb6rg"},
				{"BODY.PEEK[3]", "plain text"},
				{"BODY.PEEK[]<9.5>", whole[9:14]},
				{"BODY.PEEK[TEXT]<3.6>", whole[end+3 : end+9]},
				{"BODY.PEEK[1]<2.4>", "l lo"},
			} {
				if _, got := fetchLiteral(t, c, tc.item); got != tc.want {
					t.Errorf("%s = %q, want %q", tc.item, got, tc.want)
				}
			}
			for _, item := range []string{"RFC822.SIZE", "BODYSTRUCTURE", "BODY", "ENVELOPE"} {
				replies, tagged := fetchCommand(t, c, "FETCH 1 "+item)
				requireOK(t, tagged, "F1")
				if len(replies) != 1 || strings.IndexByte(replies[0].framing, 0) >= 0 {
					t.Fatalf("%s: %#v", item, replies)
				}
				if item == "RFC822.SIZE" && !strings.Contains(replies[0].framing, fmt.Sprintf("RFC822.SIZE %d", len(whole))) {
					t.Fatalf("%q does not report %d", replies[0].framing, len(whole))
				}
			}
			// BODYSTRUCTURE's size of the first part is the part as sent.
			_, part := fetchLiteral(t, c, "BODY.PEEK[1]")
			replies, _ := fetchCommand(t, c, "FETCH 1 BODYSTRUCTURE")
			if want := fmt.Sprintf(`"7BIT" %d `, len(part)); !strings.Contains(replies[0].framing, want) {
				t.Fatalf("%q lacks %q", replies[0].framing, want)
			}
			if box.raw[10] != nulRaw {
				t.Fatalf("stored octets changed: %q", box.raw[10])
			}
		})
	}
}

// A message holding a NUL no longer stops a FETCH of a range: the messages after it
// are sent, in FETCH and UID FETCH, and a metadata fetch never loads it.
//
// checklist: IMAP-ENV-12
func TestFetchNULKeepsARangeGoing(t *testing.T) {
	box := newFetchMailbox()
	box.messages = []imap.Message{{UID: 10}, {UID: 11}, {UID: 12}}
	box.raw = map[uint32]string{
		10: "Subject: first\r\n\r\none\r\n",
		11: "Subject: nul\x00led\r\n\r\ntw\x00o\r\n",
		12: "Subject: third\r\n\r\nthree\r\n",
	}
	c := fetchClient(t, box, "EXAMINE")
	want := []string{box.raw[10], "Subject: nul led\r\n\r\ntw o\r\n", box.raw[12]}
	for _, command := range []string{"FETCH 1:3 BODY.PEEK[]", "UID FETCH 10:12 BODY.PEEK[]", "FETCH 1:* BODY.PEEK[]"} {
		replies, tagged := fetchCommand(t, c, command)
		requireOK(t, tagged, "F1")
		if len(replies) != 3 {
			t.Fatalf("%s: %d replies: %#v", command, len(replies), replies)
		}
		for i, reply := range replies {
			if len(reply.literals) != 1 || reply.literals[0] != want[i] {
				t.Fatalf("%s message %d: %#v, want %q", command, i+1, reply, want[i])
			}
		}
	}
}

// A NUL that is data is encoded rather than replaced, whatever the part declares, and
// decoding what FETCH sent gives the exact octets back.
//
// checklist: IMAP-RESP-16
func TestFetchNULInDataIsEncoded(t *testing.T) {
	data := "\x00\x01\xff\xfe a\x00b\x00\r\n\x00end\x00"
	for _, tc := range []struct{ name, header string }{
		{"binary", "Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n"},
		{"8bit", "Content-Type: application/pdf\r\nContent-Transfer-Encoding: 8bit\r\n"},
		{"7bit", "Content-Type: image/png\r\nContent-Transfer-Encoding: 7bit\r\n"},
		{"no encoding", "Content-Type: application/zip\r\n"},
		{"unknown encoding", "Content-Type: text/plain\r\nContent-Transfer-Encoding: x-weird\r\n"},
		{"UTF-16 text", "Content-Type: text/plain; charset=UTF-16LE\r\nContent-Transfer-Encoding: binary\r\n"},
		{"UTF-32 text", "Content-Type: text/plain; charset=utf-32\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "Subject: a\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n--B\r\n" +
				tc.header + "\r\n" + data + "\r\n--B--\r\n"
			box := sevenBitBox(raw)
			c := fetchClient(t, box, "EXAMINE")

			_, sent := fetchLiteral(t, c, "BODY.PEEK[2]")
			require7bit(t, "BODY[2]", sent)
			decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(sent, "\r\n", ""))
			if err != nil {
				t.Fatalf("BODY[2] = %q: %v", sent, err)
			}
			if string(decoded) != data {
				t.Fatalf("BODY[2] decodes to %q, want %q", decoded, data)
			}
			_, mime := fetchLiteral(t, c, "BODY.PEEK[2.MIME]")
			if !strings.Contains(strings.ToLower(mime), "content-transfer-encoding: base64") {
				t.Fatalf("BODY[2.MIME] = %q", mime)
			}
			replies, tagged := fetchCommand(t, c, "FETCH 1 BODYSTRUCTURE")
			requireOK(t, tagged, "F1")
			if want := fmt.Sprintf(`"BASE64" %d`, len(sent)); len(replies) != 1 || !strings.Contains(replies[0].framing, want) {
				t.Fatalf("BODYSTRUCTURE %#v lacks %q", replies, want)
			}
			replies, tagged = fetchCommand(t, c, "FETCH 1 BINARY.PEEK[]")
			requireOK(t, tagged, "F1")
			if len(replies) != 1 || len(replies[0].literals) != 1 || replies[0].literals[0] != raw {
				t.Fatalf("BINARY[] = %#v", replies)
			}
			if box.raw[10] != raw {
				t.Fatalf("stored octets changed: %q", box.raw[10])
			}
		})
	}

	// Text in a charset with no NUL octets is not data, so that NUL is a space even when
	// it has to be encoded for another reason.
	for _, tc := range []struct{ name, raw, want string }{
		{"no charset, 8-bit", "Content-Type: text/plain\r\n\r\nJ\xf6rg\x00\r\n", "J=F6rg=20\r\n"},
		{"quoted-printable", "Content-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nJ\xf6rg\x00.\r\n", "J=F6rg .\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := textOf(t, tc.raw); got != tc.want {
				t.Fatalf("BODY[TEXT] = %q, want %q", got, tc.want)
			}
		})
	}
}

func textOf(t *testing.T, raw string) string {
	t.Helper()
	_, got := fetchLiteral(t, fetchClient(t, sevenBitBox(raw), "EXAMINE"), "BODY.PEEK[TEXT]")
	return got
}

// BINARY reads the stored octets, so it is the way to a NUL in a header or in text.
// Data holding a NUL comes as a literal8 (RFC 3516 §4.3).
//
// checklist: IMAP-BINARY-02
func TestBinaryKeepsNUL(t *testing.T) {
	box := sevenBitBox(nulRaw)
	c := fetchClient(t, box, "EXAMINE")
	for _, tc := range []struct{ item, want string }{
		{"BINARY.PEEK[]", nulRaw},
		{"BINARY.PEEK[1]", "Hal\x00lo J\xc3\xb6rg"},
		{"BINARY.PEEK[2]", "\x00\x01\xff\x00data"},
		{"BINARY.PEEK[3]", "plain\x00text"},
		{"BINARY.PEEK[2]<0.2>", "\x00\x01"},
	} {
		replies, tagged := fetchCommand(t, c, "FETCH 1 "+tc.item)
		requireOK(t, tagged, "F1")
		if len(replies) != 1 || len(replies[0].literals) != 1 || replies[0].literals[0] != tc.want {
			t.Fatalf("%s: %#v, want %q", tc.item, replies, tc.want)
		}
		if !strings.Contains(replies[0].framing, "~{") {
			t.Fatalf("%s is not a literal8: %q", tc.item, replies[0].framing)
		}
	}
	replies, tagged := fetchCommand(t, c, "FETCH 1 BINARY.SIZE[2]")
	requireOK(t, tagged, "F1")
	if want := "BINARY.SIZE[2] 8"; len(replies) != 1 || !strings.Contains(replies[0].framing, want) {
		t.Fatalf("%#v lacks %q", replies, want)
	}
}

// Whatever a message holds, a FETCH of it succeeds with no NUL on the wire outside
// BINARY, and RFC822.SIZE stays what BODY[] sends. The seeds put a NUL or a spelled one
// in each place a decoder reads, and the mutations move them about.
//
// checklist: IMAP-ENV-12
func TestFetchNULAcrossMessageShapes(t *testing.T) {
	seeds := []string{
		nulRaw,
		"Subject: nul\x00\xf6\r\nFrom: J\xf6\x00rg <j@example.com>\r\nX-Other: \x00\xc3\xa9\r\n\r\nb\r\n",
		"From: =?utf-8?Q?a=00b?= <a@example.com>\r\nCc: =?utf-8?Q?g=00=C3=A9?=: b@example.com;\r\nReply-To: =?iso-8859-1?Q?=00=E9?= <a@example.com>\r\n\r\nb",
		"Content-Type: application/x\x00y; name*=utf-8''a%00b; x*=iso-8859-1''%00%E9\r\nContent-Disposition: attachment; filename*0*=utf-8''a%00; filename*1=b\r\nContent-Language: e\x00n\r\nContent-Location: =?utf-8?Q?a=00?=\r\nContent-ID: <a\x00b>\r\n\r\n\x00",
		"Content-Type: message/rfc822\r\n\r\nSubject: a\x00\xf6\r\nTo: =?utf-8?Q?a=00b?=:;\r\nContent-Type: text/plain; charset=x\x00y\r\n\r\nb\x00\xf6\r\n",
		"Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\nAA\x00\xf6==\r\n",
		"Content-Type: multipart/digest; boundary=B\r\n\r\n--B\r\n\r\nSubject: \x00\r\n\r\n\x00\r\n--B\x00\r\n\r\nx\r\n--B--\r\n",
		"Content-Type: multipart/mixed; boundary=\"a\x00B\"\r\n\r\n--a\x00B\r\n\r\nx\r\n--a\x00B--\r\n",
		"Content-Type: multipart/mixed; boundary=\"\xf6\x00B\"\r\n\r\n--\xf6\x00B\r\n\r\nx\x00\r\n--\xf6\x00B--\r\n",
	}
	r := rand.New(rand.NewSource(1921))
	const alphabet = "\r\n\xf6\x00:;=\"\\ -B\tA"
	for i := range 600 {
		m := []byte(seeds[i%len(seeds)])
		if i >= len(seeds) {
			for k := r.Intn(4); k >= 0; k-- {
				m[r.Intn(len(m))] = alphabet[r.Intn(len(alphabet))]
			}
		}
		raw := string(m)
		for _, session := range []string{"plain", "UTF8=ACCEPT"} {
			sessionUTF8 := session != "plain"
			box := sevenBitBox(raw)
			box.messages[0].Size = wireform.Size(raw, sessionUTF8)
			var c *client
			if sessionUTF8 {
				c = utf8FetchClient(t, box)
			} else {
				c = fetchClient(t, box, "EXAMINE")
			}
			replies, tagged := fetchCommand(t, c, "FETCH 1 (RFC822.SIZE ENVELOPE BODYSTRUCTURE BODY.PEEK[])")
			requireOK(t, tagged, "F1")
			if len(replies) != 1 {
				t.Fatalf("%q: %#v", raw, replies)
			}
			reply := replies[0]
			sent := reply.literals[len(reply.literals)-1]
			if strings.IndexByte(reply.framing+strings.Join(reply.literals, ""), 0) >= 0 {
				t.Fatalf("%s %q: a NUL reached the client: %#v", session, raw, reply)
			}
			if want := fmt.Sprintf("RFC822.SIZE %d", len(sent)); !strings.Contains(reply.framing, want) {
				t.Fatalf("%s %q: %q lacks %q", session, raw, reply.framing, want)
			}
			if box.raw[10] != raw {
				t.Fatalf("stored octets changed: %q", box.raw[10])
			}
		}
		if got := wireform.SpaceNUL(raw); len(got) != len(raw) {
			t.Fatalf("SpaceNUL moved octets: %q from %q", got, raw)
		} else {
			for j := range got {
				if got[j] != raw[j] && (raw[j] != 0 || got[j] != ' ') {
					t.Fatalf("SpaceNUL changed octet %d: %q from %q", j, got, raw)
				}
			}
		}
	}
}

// A NUL in a boundary can't go out, so the field and the delimiter lines take one 7-bit
// spelling, and a client still splits the parts, in either kind of session.
func TestFetchNULInBoundary(t *testing.T) {
	for _, tc := range []struct{ name, boundary, spelled string }{
		{"7-bit", "a\x00B", "a=00B"},
		{"8-bit", "\xf6\x00B", "=F6=00B"},
		{"UTF-8", "\xc3\xa9\x00B", "=C3=A9=00B"},
	} {
		raw := "Content-Type: multipart/mixed; boundary=\"" + tc.boundary + "\"\r\n\r\n" +
			"--" + tc.boundary + "\r\n\r\none\r\n--" + tc.boundary + "\r\n\r\ntwo\x00\r\n--" + tc.boundary + "--\r\n"
		for _, session := range []string{"plain", "UTF8=ACCEPT"} {
			t.Run(tc.name+"/"+session, func(t *testing.T) {
				sessionUTF8 := session != "plain"
				box := sevenBitBox(raw)
				box.messages[0].Size = wireform.Size(raw, sessionUTF8)
				var c *client
				if sessionUTF8 {
					c = utf8FetchClient(t, box)
				} else {
					c = fetchClient(t, box, "EXAMINE")
				}
				replies, tagged := fetchCommand(t, c, "FETCH 1 (RFC822.SIZE BODY.PEEK[])")
				requireOK(t, tagged, "F1")
				if len(replies) != 1 {
					t.Fatalf("%#v", replies)
				}
				sent := replies[0].literals[len(replies[0].literals)-1]
				want := "Content-Type: multipart/mixed;\r\n boundary=\"" + tc.spelled + "\"\r\n\r\n" +
					"--" + tc.spelled + "\r\n\r\none\r\n--" + tc.spelled + "\r\n\r\ntwo \r\n--" + tc.spelled + "--\r\n"
				if sent != want {
					t.Errorf("sent %q, want %q", sent, want)
				}
				if size := fmt.Sprintf("RFC822.SIZE %d", len(sent)); !strings.Contains(replies[0].framing, size) {
					t.Errorf("%q lacks %q", replies[0].framing, size)
				}
			})
		}
	}
}

// A message with no NUL is sent as it was. SpaceNUL hands such a message back unchanged,
// and leaves data alone in one that holds a NUL elsewhere.
//
// checklist: IMAP-ENV-12
func TestSpaceNUL(t *testing.T) {
	for _, raw := range []string{"Subject: a\r\n\r\nb\r\n", "Subject: \xf6\r\n\r\nb\xf6\r\n", "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n\r\nx\r\n--B--\r\n"} {
		if got := string(wireform.SpaceNUL(raw)); got != raw {
			t.Fatalf("SpaceNUL(%q) = %q", raw, got)
		}
	}
	want := "Subject: a b\r\nFrom: j@example.com\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"pre amble\r\n" +
		"--B\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Description: d e\r\n\r\nHal lo J\xc3\xb6rg\r\n" +
		"--B\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n\r\n\x00\x01\xff\x00data\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nplain text\r\n" +
		"--B--\r\nepi logue\r\n"
	if got := string(wireform.SpaceNUL(nulRaw)); got != want {
		t.Fatalf("SpaceNUL = %q, want %q", got, want)
	}
	const utf16 = "Content-Type: text/plain; charset=utf-16le\r\n\r\na\x00b\x00"
	if got := string(wireform.SpaceNUL(utf16)); got != utf16 {
		t.Fatalf("SpaceNUL(UTF-16) = %q", got)
	}
}

// utf8FetchClient is a client that enabled UTF8=ACCEPT and examined box.
func utf8FetchClient(t *testing.T, box *fetchMailbox) *client {
	t.Helper()
	c := selectionClient(t, utf8FetchMailbox{box})
	_, tagged := c.command("E1", "ENABLE UTF8=ACCEPT")
	requireOK(t, tagged, "E1")
	_, tagged = c.command("S1", "EXAMINE INBOX")
	requireOK(t, tagged, "S1")
	return c
}
